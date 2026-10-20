package contentlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRedaction(t *testing.T) {
	for _, text := range []string{
		"Authorization: Bearer example-value", "proxy-authorization: Basic example-value",
		`{"api_key":"example-value"}`, "password =\nexample-value", "accessToken=example-value",
		"-----BEGIN RSA PRIVATE KEY-----\nexample-value\n-----END RSA PRIVATE KEY-----",
		"https://name:example-value@example.invalid/path", "Bearer example-value",
		strings.Repeat("a", MaxTextBytes+1) + "\nclient_secret=example-value",
	} {
		got, truncated := scrub(text)
		if got != Redacted || truncated {
			t.Fatalf("credential indicator not withheld: %q", got)
		}
	}
	if got, truncated := scrub("ordinary question"); got != "ordinary question" || truncated {
		t.Fatal("normal text changed")
	}
}

func TestAppendAndBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.jsonl")
	response := "ordinary answer"
	e := Event{Version: 1, Timestamp: time.Now().UTC(), Command: "ask", Model: "local-model", Outcome: "success", Prompt: "ordinary question", Response: &response}
	if err := Append(path, e); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Event
	if json.Unmarshal(b, &got) != nil || got.Prompt != e.Prompt || got.Response == nil || *got.Response != response {
		t.Fatalf("bad content event: %s", b)
	}
	e.Prompt = "Authorization: Bearer example-value"
	response = "password=example-value"
	if err := Append(path, e); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "example-value") {
		t.Fatal("secret copied to file")
	}
	text, truncated := scrub(strings.Repeat("é", MaxTextBytes))
	if !truncated || len(text) > MaxTextBytes || !utf8.ValidString(text) {
		t.Fatal("invalid truncation")
	}
	e.Prompt = strings.Repeat("\x01", MaxTextBytes+1)
	response = strings.Repeat("\x01", MaxTextBytes+1)
	if err := Append(path, e); err != nil {
		t.Fatal("bounded escaped record did not fit", err)
	}
	b, _ = os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if json.Unmarshal([]byte(lines[2]), &got) != nil || !got.PromptTruncated || !got.ResponseTruncated {
		t.Fatal("truncation flags missing")
	}
}

func TestErrorsAndMissingResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.jsonl")
	e := Event{Version: 1, Timestamp: time.Now().UTC(), Command: "ask", Model: "local-model", Outcome: "timeout", Prompt: "question"}
	if err := Append(path, e); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"response":null`) {
		t.Fatal("missing response not null")
	}
	e.Outcome = "private raw error"
	if err := Append(path, e); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("invalid outcome accepted or exposed")
	}
}
