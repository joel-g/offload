// Package config reads an explicitly selected logging policy. It never discovers
// configuration from repositories, environment variables, or home directories.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

const MaxBytes = 65536

var ErrConfig = errors.New("invalid or unreadable logging configuration")

type Content struct {
	Enabled bool   `json:"enabled"`
	File    string `json:"file"`
}
type Policy struct {
	UsageFile string                     `json:"usage_file"`
	Content   Content                    `json:"content_logging"`
	Request   map[string]json.RawMessage `json:"request"`
	Client    Client                     `json:"client"`
}

type Client struct {
	APIKeyEnv        *string `json:"api_key_env"`
	Endpoint         *string `json:"endpoint"`
	Timeout          *string `json:"timeout"`
	AllowNonlocal    *bool   `json:"allow_nonlocal"`
	AllowRedirects   *bool   `json:"allow_redirects"`
	MaxInputBytes    *int    `json:"max_input_bytes"`
	MaxOutputBytes   *int    `json:"max_output_bytes"`
	MaxResponseBytes *int64  `json:"max_response_bytes"`
}

func Load(path string) (Policy, error) {
	var p Policy
	f, err := os.Open(path)
	if err != nil {
		return p, ErrConfig
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return p, ErrConfig
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil || len(b) > MaxBytes || !utf8.Valid(b) || !uniqueObject(b, false) {
		return p, ErrConfig
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil {
		return Policy{}, ErrConfig
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Policy{}, ErrConfig
	}
	// Paths in the file are anchored to that file; CLI overrides remain cwd-relative.
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return Policy{}, ErrConfig
	}
	for _, value := range []*string{&p.UsageFile, &p.Content.File} {
		if *value != "" && !filepath.IsAbs(*value) {
			*value = filepath.Join(base, *value)
		}
	}
	return p, nil
}

// Reject duplicate keys and null values rather than accepting ambiguous policy.
func uniqueObject(b []byte, allowNull bool) bool {
	d := json.NewDecoder(bytes.NewReader(b))
	var object func() bool
	object = func() bool {
		token, err := d.Token()
		if err != nil || token != json.Delim('{') {
			return false
		}
		seen := map[string]bool{}
		for d.More() {
			token, err = d.Token()
			if err != nil {
				return false
			}
			key, ok := token.(string)
			if !allowNull {
				key = strings.ToLower(key)
			}
			if !ok || seen[key] {
				return false
			}
			seen[key] = true
			var raw json.RawMessage
			if d.Decode(&raw) != nil || (!allowNull && bytes.Equal(bytes.TrimSpace(raw), []byte("null"))) {
				return false
			}
			if len(raw) > 0 && raw[0] == '{' && !uniqueObject(raw, allowNull || key == "request") {
				return false
			}
		}
		token, err = d.Token()
		return err == nil && token == json.Delim('}')
	}
	return object()
}

// SamePath also detects existing hard links and symlinked parent aliases.
func SamePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ai, ae := os.Stat(a)
	bi, be := os.Stat(b)
	if ae == nil && be == nil && os.SameFile(ai, bi) {
		return true
	}
	canonical := func(s string) string {
		abs, err := filepath.Abs(s)
		if err != nil {
			return s
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			return resolved
		}
		if parent, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
			return filepath.Join(parent, filepath.Base(abs))
		}
		return abs
	}
	a, b = canonical(a), canonical(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func (p Policy) Validate(configPath, inputPath string) error {
	if p.Content.Enabled && p.Content.File == "" {
		return ErrConfig
	}
	paths := []string{p.UsageFile}
	if p.Content.Enabled {
		paths = append(paths, p.Content.File)
	}
	for i, path := range paths {
		if SamePath(path, configPath) || SamePath(path, inputPath) {
			return ErrConfig
		}
		for _, other := range paths[:i] {
			if SamePath(path, other) {
				return ErrConfig
			}
		}
	}
	return nil
}
