package metrics

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func number(n int64) *int64 { return &n }
func sample() Event {
	return Event{Version: 1, Timestamp: time.Now().UTC(), DurationMS: 10, Command: "ask", Model: "local-model", Scope: "loopback_only", Attempted: true, Outcome: "success"}
}

func TestParseUsage(t *testing.T) {
	for _, raw := range []string{"", "null", `[]`, `"secret"`, `{"input_tokens":-1,"output_tokens":1.2,"total_tokens":1000000001}`, `{"input_tokens":"12","output_tokens":true,"total_tokens":1e3}`} {
		got := ParseUsage(json.RawMessage(raw))
		if got.Input != nil || got.Output != nil || got.Total != nil {
			t.Fatalf("accepted invalid usage %s", raw)
		}
	}
	u := ParseUsage(json.RawMessage(`{"input_tokens":0,"output_tokens":4,"ignored":"private-metadata"}`))
	if u.Input == nil || *u.Input != 0 || u.Output == nil || *u.Output != 4 || u.Total != nil {
		t.Fatal("missing and zero conflated")
	}
}

func TestModelValidation(t *testing.T) {
	for _, s := range []string{"", strings.Repeat("a", 129), "model\nsecret", "https://example.com/model", "a b", "../model", "/private/file", "a\\b", "a\x1b[1m", "é"} {
		if ValidModel(s) {
			t.Fatalf("accepted %q", s)
		}
	}
	for _, s := range []string{"org/model-1.0:Q4_K", "model", strings.Repeat("a", 128)} {
		if !ValidModel(s) {
			t.Fatalf("rejected %q", s)
		}
	}
}

func TestAggregation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	e := sample()
	e.Usage = Usage{number(10), number(0), number(10)}
	if err := Append(path, e); err != nil {
		t.Fatal(err)
	}
	e.Outcome = "response_error"
	e.Usage = Usage{Input: number(5)}
	if err := Append(path, e); err != nil {
		t.Fatal(err)
	}
	e.Outcome = "timeout"
	e.Usage = Usage{}
	if err := Append(path, e); err != nil {
		t.Fatal(err)
	}
	e.Attempted = false
	e.Outcome = "invalid_input"
	if err := Append(path, e); err != nil {
		t.Fatal(err)
	}
	e = sample()
	e.Scope = "nonlocal_allowed"
	e.Usage = Usage{Total: number(99)}
	if err := Append(path, e); err != nil {
		t.Fatal(err)
	}
	e = sample()
	e.Command = "health"
	e.Model = ""
	if err := Append(path, e); err != nil {
		t.Fatal(err)
	}
	s, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Loopback
	if s.Events != 6 || s.Commands["ask"] != 5 || c.Invocations != 4 || c.Attempted != 3 || c.Succeeded != 1 || c.Failed != 2 || c.NotAttempted != 1 || s.Checks.Attempted != 1 {
		t.Fatalf("incorrect counts: %+v", s)
	}
	if *c.Tokens.Input.KnownSum != 15 || c.Tokens.Input.KnownCalls != 2 || c.Tokens.Input.UnknownCalls != 1 || *c.Tokens.Output.KnownSum != 0 || c.Tokens.Output.UnknownCalls != 2 || *c.Tokens.Total.KnownSum != 10 || *s.Nonlocal.Tokens.Total.KnownSum != 99 {
		t.Fatalf("incorrect tokens: %+v", c.Tokens)
	}
	if s.Nonlocal.Tokens.Input.KnownSum != nil {
		t.Fatal("unknown became zero")
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("incorrect permissions")
	}
}

func TestMalformedAndPartialLogs(t *testing.T) {
	for _, raw := range []string{"not json\n", "{}\n", strings.Repeat("x", MaxRecordBytes+1) + "\n"} {
		path := filepath.Join(t.TempDir(), "usage.jsonl")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(path); err != ErrLog {
			t.Fatal("malformed log accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	if err := Append(path, sample()); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"private":"partial`)
	_ = f.Close()
	s, err := Read(path)
	if err != nil || !s.PartialTail || s.Events != 1 {
		t.Fatalf("partial tail: %+v %v", s, err)
	}
	if err := Append(path, sample()); err != ErrLog {
		t.Fatal("appended to partial record")
	}
}

func TestUntrustedRecords(t *testing.T) {
	for _, change := range []func(*Event){
		func(e *Event) { e.Model = "secret\nmodel" }, func(e *Event) { e.Outcome = "raw private error" },
		func(e *Event) { e.Command = "unknown" }, func(e *Event) { e.Usage.Input = number(-1) },
		func(e *Event) { e.Attempted = false; e.Usage.Total = number(10) }, func(e *Event) { e.Scope = "https://example.com" },
	} {
		e := sample()
		change(&e)
		if err := Append(filepath.Join(t.TempDir(), "usage.jsonl"), e); err != ErrLog {
			t.Fatal("invalid event accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	b, _ := json.Marshal(sample())
	b = append(b[:len(b)-1], []byte(`,"prompt":"private"}`+"\n")...)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err != ErrLog || strings.Contains(err.Error(), "private") {
		t.Fatal("unknown fields accepted or leaked")
	}
}

// The subprocess branch exercises the same lock across actual processes.
func TestConcurrentProcesses(t *testing.T) {
	if path := os.Getenv("OFFLOAD_TEST_USAGE_FILE"); path != "" {
		for i := 0; i < 5; i++ {
			if err := Append(path, sample()); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestConcurrentProcesses$")
			cmd.Env = append(os.Environ(), "OFFLOAD_TEST_USAGE_FILE="+path)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("child failed: %v %s", err, out)
			}
		}()
	}
	wg.Wait()
	s, err := Read(path)
	if err != nil || s.Events != 20 {
		t.Fatalf("lost records: %d %v", s.Events, err)
	}
}

func TestConcurrentReadersAndWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	if err := Append(path, sample()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if err := Append(path, sample()); err != nil {
					t.Error(err)
				}
				if _, err := Read(path); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	s, err := Read(path)
	if err != nil || s.Events != 41 {
		t.Fatalf("lost records: %d %v", s.Events, err)
	}
}

func TestUnsafeFullAndLockedLogs(t *testing.T) {
	dir := t.TempDir()
	if err := Append(dir, sample()); err != ErrLog {
		t.Fatal("directory accepted")
	}
	path := filepath.Join(dir, "usage.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := Append(path, sample()); err != ErrLog {
		t.Fatal("oversized file accepted")
	}
	if _, err := Read(path); err != ErrLog {
		t.Fatal("oversized file read")
	}
	locked := filepath.Join(dir, "locked.jsonl")
	if err := os.Mkdir(locked+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	if err := Append(locked, sample()); err != ErrLog {
		t.Fatal("stale lock bypassed")
	}
	if runtime.GOOS != "windows" {
		broad := filepath.Join(dir, "broad.jsonl")
		if err := os.WriteFile(broad, nil, 0600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(broad, 0644)
		if err := Append(broad, sample()); err != ErrLog {
			t.Fatal("broad permissions accepted")
		}
		link := filepath.Join(dir, "link.jsonl")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if err := Append(link, sample()); err != ErrLog {
			t.Fatal("symlink accepted")
		}
	}
}
