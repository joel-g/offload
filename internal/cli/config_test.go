package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joel-g/offload/internal/contentlog"
	"github.com/joel-g/offload/internal/metrics"
)

const reply = `{"id":"response-example","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ordinary answer"}]}]}`

func configFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAPIDefaultsAndExplicitFields(t *testing.T) {
	var received map[string]json.RawMessage
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/responses" || r.Method != "POST" {
			t.Error("unexpected model-management request")
		}
		received = nil
		_ = json.NewDecoder(r.Body).Decode(&received)
		fmt.Fprint(w, reply)
	}))
	defer server.Close()
	status, _, stderr := run([]string{"ask", "--stdin", "--endpoint", server.URL}, "")
	if status != 0 {
		t.Fatal(stderr)
	}
	if len(received) != 1 || string(received["input"]) != `""` {
		t.Fatalf("API defaults were overridden: %s", received)
	}
	model := "provider/model with spaces"
	status, _, stderr = run([]string{"ask", "--stdin", "--endpoint", server.URL, "--model", model, "--store=false", "--max-output-tokens", "90000", "--instructions", "", "--set", "temperature=0.25", "--set", `reasoning={"effort":"low"}`}, "question")
	if status != 0 {
		t.Fatal(stderr)
	}
	var gotModel string
	_ = json.Unmarshal(received["model"], &gotModel)
	if gotModel != model || string(received["store"]) != "false" || string(received["max_output_tokens"]) != "90000" || string(received["instructions"]) != `""` || string(received["temperature"]) != "0.25" {
		t.Fatalf("explicit fields changed: %s", received)
	}
	if _, ok := received["stream"]; ok {
		t.Fatal("injected stream default")
	}
	if calls != 2 {
		t.Fatal("unexpected extra API calls")
	}
	status, _, stderr = run([]string{"ask", "--stdin", "--endpoint", server.URL, "--store=true"}, "question")
	if status != 0 || string(received["store"]) != "true" {
		t.Fatalf("explicit storage policy not forwarded: %d %s", status, stderr)
	}
}

func TestRequestConfigPrecedenceAndTools(t *testing.T) {
	var received map[string]json.RawMessage
	toolReply := `{"status":"completed","output":[{"type":"function_call","name":"example_tool","call_id":"call-example","arguments":"{\"value\":1}"}],"usage":{"output_tokens":4}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = nil
		_ = json.NewDecoder(r.Body).Decode(&received)
		fmt.Fprint(w, toolReply)
	}))
	defer server.Close()
	cfg := configFile(t, `{"request":{"model":"config-model","store":true,"temperature":0.5,"instructions":"configured"},"client":{"endpoint":"http://127.0.0.1:1","max_output_bytes":1}}`)
	input := `{"model":"body-model","input":[{"role":"user","content":"question"}],"tools":[{"type":"function","name":"example_tool","parameters":{"type":"object"}}],"previous_response_id":"previous-example","temperature":0.6}`
	args := []string{"responses", "--config", cfg, "--stdin", "--endpoint", server.URL, "--max-output-bytes", "4096", "--set", `model="set-model"`, "--set", "temperature=0.7", "--model", "flag-model", "--store=false"}
	status, out, stderr := run(args, input)
	if status != 0 || stderr != "" || !json.Valid([]byte(out)) || !strings.Contains(out, `"function_call"`) {
		t.Fatalf("tool result lost: %d %s %s", status, out, stderr)
	}
	if string(received["model"]) != `"flag-model"` || string(received["store"]) != "false" || string(received["temperature"]) != "0.7" || string(received["instructions"]) != `"configured"` || received["tools"] == nil || received["previous_response_id"] == nil {
		t.Fatalf("precedence or passthrough incorrect: %s", received)
	}
	status, out, stderr = run([]string{"ask", "--stdin", "--endpoint", server.URL, "--json"}, "question")
	if status != 0 || !strings.Contains(out, `"function_call"`) {
		t.Fatalf("JSON helper discarded tool calls: %d %s %s", status, out, stderr)
	}
	status, out, stderr = run([]string{"ask", "--stdin", "--endpoint", server.URL}, "question")
	if status != 5 || out != "" || !strings.Contains(stderr, "structured output") {
		t.Fatalf("text projection: %d %s %s", status, out, stderr)
	}
}

func TestPartialResponsePreserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}]}`)
	}))
	defer server.Close()
	log := filepath.Join(t.TempDir(), "usage.jsonl")
	status, out, stderr := run([]string{"responses", "--stdin", "--endpoint", server.URL, "--usage-file", log}, `{"input":"question"}`)
	if status != 0 || stderr != "" || !strings.Contains(out, `"incomplete"`) || !strings.Contains(out, "partial") {
		t.Fatalf("partial response lost: %d %s %s", status, out, stderr)
	}
	summary, err := metrics.Read(log)
	if err != nil || summary.ResponseStatuses["incomplete"] != 1 {
		t.Fatalf("response status missing from metrics: %+v %v", summary, err)
	}
}

func TestConfigLoggingAndDisableOverrides(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Authorization", "Bearer transport-secret")
		fmt.Fprint(w, reply)
	}))
	defer server.Close()
	cfg := configFile(t, `{"usage_file":"usage.jsonl","content_logging":{"enabled":true,"file":"content.jsonl"}}`)
	args := []string{"ask", "--stdin", "--endpoint", server.URL, "--config", cfg}
	status, out, stderr := run(args, "ordinary question")
	if status != 0 || out != "ordinary answer\n" || stderr != "" {
		t.Fatalf("logging failed: %d %s", status, stderr)
	}
	dir := filepath.Dir(cfg)
	usage, err := os.ReadFile(filepath.Join(dir, "usage.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "content.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(usage), "ordinary") || !strings.Contains(string(content), "ordinary question") || !strings.Contains(string(content), "ordinary answer") {
		t.Fatal("usage and content mixed")
	}
	if strings.Contains(string(content), "transport-secret") || strings.Contains(string(content), server.URL) {
		t.Fatal("transport metadata logged")
	}
	disabled := append(append([]string{}, args...), "--log-content=false", "--usage-file=")
	if status, _, err := run(disabled, "not retained"); status != 0 {
		t.Fatal(err)
	}
	usageAfter, _ := os.ReadFile(filepath.Join(dir, "usage.jsonl"))
	contentAfter, _ := os.ReadFile(filepath.Join(dir, "content.jsonl"))
	if string(usageAfter) != string(usage) || string(contentAfter) != string(content) {
		t.Fatal("false/empty overrides failed")
	}
	status, stats, stderr := run([]string{"stats", "--config", cfg, "--json"}, "")
	if status != 0 || !strings.Contains(stats, `"events":1`) {
		t.Fatalf("stats config: %d %s %s", status, stats, stderr)
	}
}

func TestContentOptInAndRedaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Replace(reply, "ordinary answer", "Authorization: Bearer response-secret", 1))
	}))
	defer server.Close()
	cfg := configFile(t, `{"content_logging":{"file":"content.jsonl"}}`)
	args := []string{"ask", "--stdin", "--endpoint", server.URL, "--config", cfg}
	if status, _, err := run(args, "ordinary question"); status != 0 {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(cfg), "content.jsonl")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file path enabled logging")
	}
	args = append(args, "--log-content")
	status, out, stderr := run(args, "password=request-secret")
	if status != 0 || !strings.Contains(out, "response-secret") {
		t.Fatalf("logging altered response: %d %s %s", status, out, stderr)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "request-secret") || strings.Contains(string(b), "response-secret") || !strings.Contains(string(b), contentlog.Redacted) {
		t.Fatal("content credentials retained")
	}
}

func TestConfigAndContentErrors(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, reply) }))
	defer server.Close()
	for _, body := range []string{`{"unknown":"private-value"}`, `{"content_logging":{"enabled":true}}`, `{"usage_file":"same","content_logging":{"enabled":true,"file":"same"}}`} {
		cfg := configFile(t, body)
		status, out, stderr := run([]string{"ask", "--stdin", "--endpoint", server.URL, "--config", cfg}, "question")
		if status != 2 || out != "" || strings.Contains(stderr, "private-value") {
			t.Fatalf("bad config: %d %s", status, stderr)
		}
	}
	if calls != 0 {
		t.Fatal("invalid configuration reached server")
	}
	cfg := configFile(t, `{"content_logging":{"enabled":true,"file":"missing/content.jsonl"}}`)
	status, out, stderr := run([]string{"ask", "--stdin", "--endpoint", server.URL, "--config", cfg}, "question")
	if status != 7 || out != "ordinary answer\n" || !strings.Contains(stderr, "cannot record content") || strings.Contains(stderr, "missing/") {
		t.Fatalf("content error: %d %s %s", status, out, stderr)
	}
	for _, args := range [][]string{{"responses", "--stdin"}, {"ask", "--stdin", "--set", "private-invalid"}, {"ask", "--stdin", "--set", "stream=true"}} {
		input := "null"
		if args[0] == "ask" {
			input = "question"
		}
		status, _, stderr = run(append(args, "--endpoint", server.URL), input)
		if status != 2 || strings.Contains(stderr, "private-invalid") {
			t.Fatalf("invalid request: %d %s", status, stderr)
		}
	}
	if calls != 1 {
		t.Fatal("invalid request reached server")
	}
}

func TestCredentialEnvironmentNeverLogged(t *testing.T) {
	const secret = "example-opaque-value"
	t.Setenv("OFFLOAD_TEST_CREDENTIAL", secret)
	cfg := configFile(t, `{"client":{"api_key_env":"OFFLOAD_TEST_CREDENTIAL"},"usage_file":"usage.jsonl","content_logging":{"enabled":true,"file":"content.jsonl"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("credential not selected")
		}
		fmt.Fprint(w, strings.Replace(reply, "ordinary answer", secret, 1))
	}))
	defer server.Close()
	status, out, stderr := run([]string{"ask", "--stdin", "--config", cfg, "--endpoint", server.URL}, "question")
	if status != 0 || !strings.Contains(out, secret) || strings.Contains(stderr, secret) {
		t.Fatalf("credential flow: %d %s", status, stderr)
	}
	for _, name := range []string{"usage.jsonl", "content.jsonl"} {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(cfg), name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), secret) || strings.Contains(string(b), "OFFLOAD_TEST_CREDENTIAL") {
			t.Fatal("credential or configuration logged")
		}
	}
	status, _, stderr = run([]string{"ask", "--stdin", "--api-key-env", "OFFLOAD_TEST_MISSING_CREDENTIAL", "--endpoint", server.URL}, "question")
	if status != 2 || strings.Contains(stderr, "OFFLOAD_TEST_MISSING_CREDENTIAL") {
		t.Fatalf("credential error: %d %s", status, stderr)
	}
}
