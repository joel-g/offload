package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const completed = `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`

func code(t *testing.T, err error, want int) {
	t.Helper()
	e, ok := err.(*Error)
	if !ok || e.Code != want {
		t.Fatalf("error = %v, want code %d", err, want)
	}
}
func client(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(Config{Endpoint: server.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
func generate(c *Client) (string, error) {
	text, _, err := c.Generate(context.Background(), "test-model", "instruction", "private-input", 42, 100)
	return text, err
}

func TestRequestShape(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("incorrect route or headers")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 6 || body["store"] != false || body["stream"] != false || body["input"] != "private-input" || body["instructions"] != "instruction" || body["model"] != "test-model" || body["max_output_tokens"] != float64(42) {
			t.Errorf("incorrect request fields: %v", body)
		}
		fmt.Fprint(w, completed)
	})
	got, err := generate(c)
	if err != nil || got != "answer" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestResponseParsingAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		status, want int
	}{
		{"status", "private-remote-body", 401, 4},
		{"invalid", "private-remote-body", 200, 5},
		{"error", `{"status":"failed","error":{"message":"private-remote-body"}}`, 200, 4},
		{"incomplete", `{"status":"incomplete","output":[]}`, 200, 5},
		{"empty", `{"status":"completed","output":[]}`, 200, 5},
		{"tool", `{"status":"completed","output":[{"type":"function_call","arguments":"private-remote-body"}]}`, 200, 5},
		{"refusal", `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"private-remote-body"}]}]}`, 200, 5},
		{"oversized", strings.Repeat("x", MaxResponseBytes+1), 200, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := client(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) })
			got, err := generate(c)
			code(t, err, tc.want)
			if got != "" || strings.Contains(err.Error(), "private") {
				t.Fatal("response leaked")
			}
		})
	}
}
func TestOutputBoundAndParts(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"completed","output":[{"type":"reasoning","content":[{"type":"output_text","text":"hidden"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"one"},{"type":"output_text","text":"two"}]}]}`)
	})
	got, err := generate(c)
	if got != "one\ntwo" || err != nil {
		t.Fatalf("%q %v", got, err)
	}
	_, _, err = c.Generate(context.Background(), "m", "i", "x", 1, 6)
	code(t, err, 6)
}
func TestCancelAndTimeout(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Models(ctx)
	code(t, err, 130)
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = c.Models(ctx)
	code(t, err, 124)
	c.http.Timeout = 20 * time.Millisecond
	_, err = c.Models(context.Background())
	code(t, err, 124)
}

func TestCancelActiveRequest(t *testing.T) {
	started := make(chan struct{})
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Models(ctx); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		code(t, err, 130)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not complete")
	}
}

func TestBodyReadTimeout(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	c.http.Timeout = 20 * time.Millisecond
	_, err := c.Models(context.Background())
	code(t, err, 124)
}
func TestEndpointPolicy(t *testing.T) {
	for _, endpoint := range []string{"https://example.com", "http://127.0.0.1.example.com", "file:///tmp/input", "http://user:secret@127.0.0.1", "http://127.0.0.1?secret=x", "http://127.0.0.1/#secret", ":bad"} {
		_, err := New(Config{Endpoint: endpoint, Timeout: time.Second})
		code(t, err, 2)
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("URL leaked")
		}
	}
	for _, endpoint := range []string{"http://127.0.0.1", "http://[::1]", "http://localhost"} {
		c, err := New(Config{Endpoint: endpoint, Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	c, err := New(Config{Endpoint: "https://example.com", AllowNonlocal: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}
func TestRedirectPolicy(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, completed) }))
	defer target.Close()
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	_, err := generate(c)
	code(t, err, 3)
	if calls != 0 {
		t.Fatal("followed denied redirect")
	}
	allowed, err := New(Config{Endpoint: c.base.String(), AllowRedirects: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer allowed.Close()
	_, err = generate(allowed)
	if err != nil || calls != 1 {
		t.Fatalf("redirect opt-in failed: %v", err)
	}
	remote, _ := http.NewRequest("POST", "https://example.com", nil)
	if allowed.http.CheckRedirect(remote, nil) == nil {
		t.Fatal("redirect escaped loopback policy")
	}
}
func TestNoAmbientProxyAndModels(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/models" {
			t.Error("invalid models request")
		}
		fmt.Fprint(w, `{"data":[{"id":"test-model"}]}`)
	})
	if c.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("ambient proxy enabled")
	}
	got, err := c.Models(context.Background())
	if err != nil || len(got) != 1 || got[0] != "test-model" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestServerUsage(t *testing.T) {
	for _, status := range []string{"completed", "incomplete", "failed"} {
		t.Run(status, func(t *testing.T) {
			c := client(t, func(w http.ResponseWriter, r *http.Request) {
				body := strings.Replace(completed, `"completed"`, fmt.Sprintf("%q", status), 1)
				fmt.Fprint(w, strings.TrimSuffix(body, "}")+`,"model":"private-server-model","usage":{"input_tokens":12,"output_tokens":0,"total_tokens":"private-invalid"}}`)
			})
			_, usage, err := c.Generate(context.Background(), "test-model", "instruction", "input", 10, 100)
			if usage.Input == nil || *usage.Input != 12 || usage.Output == nil || *usage.Output != 0 || usage.Total != nil {
				t.Fatal("incorrect usage parsing")
			}
			if status == "completed" && err != nil {
				t.Fatal(err)
			}
			if status != "completed" && err == nil {
				t.Fatal("expected generation failure")
			}
		})
	}
}
