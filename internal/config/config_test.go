package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	for _, tc := range []struct {
		body           string
		enabled        bool
		usage, content string
	}{
		{`{}`, false, "", ""},
		{`{"usage_file":"usage.jsonl","content_logging":{"enabled":true,"file":"content.jsonl"}}`, true, filepath.Join(dir, "usage.jsonl"), filepath.Join(dir, "content.jsonl")},
		{`{"content_logging":{"file":"content.jsonl"}}`, false, "", filepath.Join(dir, "content.jsonl")},
	} {
		if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if got.UsageFile != tc.usage || got.Content.Enabled != tc.enabled || got.Content.File != tc.content {
			t.Fatalf("bad policy: %+v", got)
		}
	}
}

func TestInvalidConfigsRedacted(t *testing.T) {
	for _, body := range []string{
		`not-private-json`, `null`, `[]`, `{} {}`, `{"unknown":"private-value"}`,
		`{"content_logging":{"enabled":"private-value"}}`, `{"usage_file":null}`,
		`{"content_logging":{"enabled":true,"Enabled":false}}`,
		`{"usage_file":"a","usage_file":"b"}`, `{"content_logging":null}`,
		strings.Repeat(" ", MaxBytes) + `{}`, string([]byte{'{', '"', 0xff, '"', ':', '0', '}'}),
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err != ErrConfig || strings.Contains(err.Error(), "private") {
			t.Fatalf("accepted or exposed config: %v", err)
		}
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing")); err != ErrConfig {
		t.Fatal("missing config accepted")
	}
	if _, err := Load(t.TempDir()); err != ErrConfig {
		t.Fatal("directory accepted")
	}
}

func TestDistinctPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same.jsonl")
	for _, p := range []Policy{
		{Content: Content{Enabled: true}},
		{UsageFile: path, Content: Content{Enabled: true, File: path}},
	} {
		if p.Validate("", "") == nil {
			t.Fatal("invalid policy accepted")
		}
	}
	p := Policy{UsageFile: path}
	if p.Validate(path, "") == nil || p.Validate("", path) == nil {
		t.Fatal("source collision accepted")
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.jsonl")
	if err := os.Link(path, alias); err != nil {
		t.Skip("hard links unavailable")
	}
	p.Content = Content{Enabled: true, File: alias}
	if p.Validate("", "") == nil {
		t.Fatal("hard-linked logs accepted")
	}
}
