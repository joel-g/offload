package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joel-g/offload/internal/metrics"
)

func run(args []string, input string) (int, string, string) {
	var out, err bytes.Buffer
	status := Execute(context.Background(), args, strings.NewReader(input), &out, &err)
	return status, out.String(), err.String()
}
func TestCommands(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"data":[{"id":"test-model"}]}`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, exists := body["store"]; exists || body["input"] != "supplied input" {
			t.Error("input mismatch or unexpected store default")
		}
		fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"result"}]}]}`)
	}))
	defer server.Close()
	for _, command := range []string{"ask", "summarize", "review", "models", "health"} {
		for _, machine := range []bool{false, true} {
			args := []string{command, "--endpoint", server.URL}
			if command != "models" && command != "health" {
				args = append(args, "--stdin", "--model", "test-model")
			}
			if machine {
				args = append(args, "--json")
			}
			status, out, err := run(args, "supplied input")
			if status != 0 || err != "" {
				t.Fatalf("%s: %d %s", command, status, err)
			}
			if machine {
				var value map[string]any
				if json.Unmarshal([]byte(out), &value) != nil || value["command"] != command {
					t.Fatalf("invalid JSON: %s", out)
				}
			} else {
				want := "result\n"
				if command == "models" {
					want = "test-model\n"
				}
				if command == "health" {
					want = "ok\n"
				}
				if out != want {
					t.Fatalf("got %q want %q", out, want)
				}
			}
		}
	}
}
func TestValidationAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		input string
		code  int
	}{
		{[]string{"ask", "--model", "m"}, "text", 2},
		{[]string{"ask", "--model", "m", "--stdin", "--file", "private-file"}, "text", 2},
		{[]string{"ask", "--model", "m", "--file", "private-missing"}, "", 2},
		{[]string{"ask", "--model", "m", "--stdin", "--max-input-bytes", "2"}, "text", 6},
		{[]string{"ask", "--model", "m", "--stdin", "--max-input-bytes", "0"}, "text", 2},
		{[]string{"ask", "--model", "m", "--stdin"}, string([]byte{0xff}), 2},
		{[]string{"ask", "private-argument"}, "", 2},
		{[]string{"--private-flag"}, "", 2},
		{[]string{"health", "--timeout", "private-duration"}, "", 2},
	} {
		status, out, err := run(tc.args, tc.input)
		if status != tc.code || out != "" || strings.Contains(err, "private") {
			t.Fatalf("code=%d stdout=%q stderr=%q", status, out, err)
		}
	}
	status, out, err := run([]string{"ask", "--json"}, "")
	if status != 2 || out != "" || !json.Valid([]byte(err)) {
		t.Fatalf("%d %q %q", status, out, err)
	}
}
func TestFileInput(t *testing.T) {
	file := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(file, []byte("file input"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["input"] != "file input" {
			t.Error("file contents missing")
		}
		fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"result"}]}]}`)
	}))
	defer server.Close()
	status, _, err := run([]string{"ask", "--file", file, "--model", "m", "--endpoint", server.URL}, "ignored")
	if status != 0 {
		t.Fatal(err)
	}
}

func TestUsageOptInAndPrivacy(t *testing.T) {
	log := filepath.Join(t.TempDir(), "usage.jsonl")
	t.Setenv("OFFLOAD_USAGE_FILE", log) // No ambient opt-in is supported.
	file := filepath.Join(t.TempDir(), "private-input-name.txt")
	if err := os.WriteFile(file, []byte("private-input-content"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Private", "private-header")
		fmt.Fprint(w, `{"status":"completed","model":"private-server-identity","usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13,"extra":"private-metadata"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"private-response-content"}]}]}`)
	}))
	defer server.Close()
	args := []string{"ask", "--file", file, "--model", "local-model", "--endpoint", server.URL}
	if status, _, err := run(args, ""); status != 0 {
		t.Fatal(err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("metrics enabled by default or environment")
	}
	args = append(args, "--usage-file", log)
	if status, _, err := run(args, ""); status != 0 {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private") || strings.Contains(string(b), server.URL) || strings.Contains(string(b), file) {
		t.Fatal("private content reached log")
	}
	var fields map[string]any
	if json.Unmarshal(b, &fields) != nil || len(fields) != 10 || fields["model"] != "local-model" {
		t.Fatalf("unexpected event schema: %s", b)
	}
	s, err := metrics.Read(log)
	if err != nil || s.Events != 1 || *s.Loopback.Tokens.Total.KnownSum != 13 {
		t.Fatalf("bad metrics: %+v %v", s, err)
	}
	status, out, stderr := run([]string{"stats", "--usage-file", log, "--json"}, "")
	if status != 0 || stderr != "" || !json.Valid([]byte(out)) || !strings.Contains(out, `"known_sum":13`) {
		t.Fatalf("stats: %d %s %s", status, out, stderr)
	}
	status, out, stderr = run([]string{"stats", "--usage-file", log}, "")
	if status != 0 || stderr != "" || !strings.Contains(out, "total tokens: 13") {
		t.Fatalf("plain stats: %d %s %s", status, out, stderr)
	}
	after, _ := os.ReadFile(log)
	if !bytes.Equal(b, after) {
		t.Fatal("stats appended an event")
	}
}

func TestUsageFailuresAndChecks(t *testing.T) {
	log := filepath.Join(t.TempDir(), "usage.jsonl")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		w.WriteHeader(500)
		fmt.Fprint(w, "private-server-error")
	}))
	defer server.Close()
	args := []string{"ask", "--model", "local-model", "--stdin", "--usage-file", log, "--endpoint", server.URL}
	status, _, stderr := run(args, "private-input")
	if status != 4 || strings.Contains(stderr, "private") {
		t.Fatalf("failure: %d %s", status, stderr)
	}
	status, _, stderr = run([]string{"ask", "--model", "private\nmodel", "--usage-file", log}, "input")
	if status != 2 || strings.Contains(stderr, "private") {
		t.Fatalf("model validation: %d %s", status, stderr)
	}
	status, _, stderr = run([]string{"health", "--usage-file", log, "--endpoint", server.URL}, "")
	if status != 0 {
		t.Fatal(stderr)
	}
	s, err := metrics.Read(log)
	if err != nil || s.Loopback.Failed != 1 || s.Loopback.NotAttempted != 1 || s.Checks.Succeeded != 1 || s.Loopback.Tokens.Total.UnknownCalls != 1 || s.Loopback.Tokens.Total.KnownSum != nil {
		t.Fatalf("counts: %+v %v", s, err)
	}
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "private") {
		t.Fatal("failure leaked private content")
	}
}

func TestLoggingFailuresVisible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-missing-dir", "private-log.jsonl")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[]}`) }))
	defer server.Close()
	status, out, stderr := run([]string{"health", "--usage-file", path, "--endpoint", server.URL, "--json"}, "")
	if status != 7 || !json.Valid([]byte(out)) || !json.Valid([]byte(stderr)) || !strings.Contains(stderr, "cannot record usage") || strings.Contains(stderr, "private") {
		t.Fatalf("logging failure: %d %s %s", status, out, stderr)
	}
	status, _, stderr = run([]string{"ask", "--usage-file", path}, "")
	if status != 2 || !strings.Contains(stderr, "cannot record usage") || !strings.Contains(stderr, "choose exactly one") || strings.Contains(stderr, "private") {
		t.Fatalf("dual failure: %d %s", status, stderr)
	}
	status, _, stderr = run([]string{"stats", "--usage-file", path}, "")
	if status != 7 || strings.Contains(stderr, "private") {
		t.Fatalf("stats error: %d %s", status, stderr)
	}
	status, _, _ = run([]string{"stats"}, "")
	if status != 2 {
		t.Fatal("stats did not require explicit file")
	}
}
