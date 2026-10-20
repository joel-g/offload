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
		if body["store"] != false || body["input"] != "supplied input" {
			t.Error("input or no-store mismatch")
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
		{[]string{"ask", "--stdin"}, "text", 2},
		{[]string{"ask", "--model", "m"}, "text", 2},
		{[]string{"ask", "--model", "m", "--stdin", "--file", "private-file"}, "text", 2},
		{[]string{"ask", "--model", "m", "--file", "private-missing"}, "", 2},
		{[]string{"ask", "--model", "m", "--stdin", "--max-input-bytes", "2"}, "text", 6},
		{[]string{"ask", "--model", "m", "--stdin", "--max-output-tokens", "0"}, "text", 2},
		{[]string{"ask", "--model", "m", "--stdin"}, " ", 2},
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
