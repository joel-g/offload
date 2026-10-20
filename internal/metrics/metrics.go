// Package metrics stores opt-in, content-free usage records on the local filesystem.
package metrics

import (
	"bufio"
	"bytes"
	"encoding/json"
	"github.com/joel-g/offload/internal/logfile"
	"io"
	"os"

	"strings"
	"time"
)

const MaxFileBytes = logfile.MaxFileBytes
const MaxRecordBytes = 4096
const MaxTokens = int64(1_000_000_000)

var ErrLog = logfile.ErrLog

// Usage preserves missing or invalid server counts as null, independently per field.
type Usage struct {
	Input  *int64 `json:"input_tokens"`
	Output *int64 `json:"output_tokens"`
	Total  *int64 `json:"total_tokens"`
}

// ParseUsage accepts only bounded, nonnegative JSON integers. Never infer totals.
func ParseUsage(raw json.RawMessage) Usage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return Usage{}
	}
	read := func(key string) *int64 {
		var n int64
		v := fields[key]
		if len(v) == 0 || string(v) == "null" || json.Unmarshal(v, &n) != nil || n < 0 || n > MaxTokens {
			return nil
		}
		return &n
	}
	return Usage{read("input_tokens"), read("output_tokens"), read("total_tokens")}
}

// ValidModel restricts identifiers to a small printable alphabet, excluding URLs.
func ValidModel(s string) bool {
	if len(s) < 1 || len(s) > 128 || strings.Contains(s, ":/") {
		return false
	}
	for i, c := range s {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || !strings.ContainsRune("/._:-", c)) {
			return false
		}
	}
	return true
}

type Event struct {
	Version        int       `json:"version"`
	Timestamp      time.Time `json:"timestamp"`
	DurationMS     int64     `json:"duration_ms"`
	Command        string    `json:"command"`
	Model          string    `json:"model,omitempty"`
	Scope          string    `json:"scope"`
	Attempted      bool      `json:"attempted"`
	Outcome        string    `json:"outcome"`
	ResponseStatus string    `json:"response_status,omitempty"`
	Usage          Usage     `json:"usage"`
}

func Inference(command string) bool {
	return command == "ask" || command == "summarize" || command == "review" || command == "responses"
}

// ResponseStatus accepts only the documented response-state vocabulary.
func ResponseStatus(s string) string {
	switch s {
	case "completed", "failed", "incomplete", "in_progress", "queued", "cancelled":
		return s
	}
	return ""
}

func valid(e Event) bool {
	if e.Version != 1 || e.Timestamp.IsZero() || e.DurationMS < 0 || e.DurationMS > int64((365*24*time.Hour)/time.Millisecond) {
		return false
	}
	if !Inference(e.Command) && e.Command != "health" && e.Command != "models" {
		return false
	}
	if e.ResponseStatus != "" && (ResponseStatus(e.ResponseStatus) == "" || !Inference(e.Command) || !e.Attempted) {
		return false
	}
	if e.Scope != "loopback_only" && e.Scope != "nonlocal_allowed" {
		return false
	}
	switch e.Outcome {
	case "success", "invalid_input", "transport_or_output_error", "server_error", "response_error", "bounds_error", "timeout", "canceled":
	default:
		return false
	}
	if e.Model != "" && !ValidModel(e.Model) {
		return false
	}
	if e.Outcome == "success" && !e.Attempted {
		return false
	}
	if !Inference(e.Command) && e.Model != "" {
		return false
	}
	for _, n := range []*int64{e.Usage.Input, e.Usage.Output, e.Usage.Total} {
		if n != nil && (*n < 0 || *n > MaxTokens || !e.Attempted || !Inference(e.Command)) {
			return false
		}
	}
	return true
}

// Append writes one complete event under a lock, with no content-bearing errors.
func Append(path string, event Event) error {
	if !valid(event) {
		return ErrLog
	}
	b, err := json.Marshal(event)
	if err != nil {
		return ErrLog
	}
	return logfile.AppendLine(path, b, MaxRecordBytes)
}

type Count struct {
	Invocations  int64 `json:"invocations"`
	Attempted    int64 `json:"attempted"`
	Succeeded    int64 `json:"succeeded"`
	Failed       int64 `json:"failed"`
	NotAttempted int64 `json:"not_attempted"`
}
type TokenCount struct {
	KnownSum     *int64 `json:"known_sum"`
	KnownCalls   int64  `json:"known_calls"`
	UnknownCalls int64  `json:"unknown_calls"`
}
type Tokens struct {
	Input  TokenCount `json:"input"`
	Output TokenCount `json:"output"`
	Total  TokenCount `json:"total"`
}
type InferenceCount struct {
	Count
	Tokens Tokens `json:"tokens"`
}
type Summary struct {
	Events           int64            `json:"events"`
	Commands         map[string]int64 `json:"commands"`
	ResponseStatuses map[string]int64 `json:"response_statuses"`
	Loopback         InferenceCount   `json:"loopback_inference"`
	Nonlocal         InferenceCount   `json:"nonlocal_allowed_inference"`
	Checks           Count            `json:"service_checks"`
	PartialTail      bool             `json:"partial_tail_ignored"`
}

func (c *Count) add(e Event) {
	c.Invocations++
	if !e.Attempted {
		c.NotAttempted++
		return
	}
	c.Attempted++
	if e.Outcome == "success" {
		c.Succeeded++
	} else {
		c.Failed++
	}
}
func (c *TokenCount) add(n *int64) {
	if n == nil {
		c.UnknownCalls++
		return
	}
	if c.KnownSum == nil {
		c.KnownSum = new(int64)
	}
	*c.KnownSum += *n
	c.KnownCalls++
}
func (s *Summary) add(e Event) {
	s.Events++
	s.Commands[e.Command]++
	if e.ResponseStatus != "" {
		s.ResponseStatuses[e.ResponseStatus]++
	}
	if !Inference(e.Command) {
		s.Checks.add(e)
		return
	}
	c := &s.Loopback
	if e.Scope == "nonlocal_allowed" {
		c = &s.Nonlocal
	}
	c.Count.add(e)
	if e.Attempted {
		c.Tokens.Input.add(e.Usage.Input)
		c.Tokens.Output.add(e.Usage.Output)
		c.Tokens.Total.add(e.Usage.Total)
	}
}

// Read uses the writer lock for a stable snapshot. Complete malformed records fail
// closed. An unterminated final fragment is ignored and explicitly reported.
func Read(path string) (Summary, error) {
	summary := Summary{Commands: make(map[string]int64), ResponseStatuses: make(map[string]int64)}
	err := logfile.WithLock(path, func() error {
		if _, err := logfile.CheckFile(path); err != nil {
			return ErrLog
		}
		f, err := os.Open(path)
		if err != nil {
			return ErrLog
		}
		defer f.Close()
		reader := bufio.NewReaderSize(io.LimitReader(f, MaxFileBytes+1), MaxRecordBytes)
		var size int64
		for {
			line, err := reader.ReadSlice('\n')
			size += int64(len(line))
			if size > MaxFileBytes || len(line) > MaxRecordBytes {
				return ErrLog
			}
			if err == io.EOF {
				summary.PartialTail = len(line) != 0
				return nil
			}
			if err != nil {
				return ErrLog
			}
			var event Event
			decoder := json.NewDecoder(bytes.NewReader(line))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&event) != nil || !valid(event) {
				return ErrLog
			}
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				return ErrLog
			}
			summary.add(event)
		}
	})
	if err != nil {
		return Summary{}, ErrLog
	}
	return summary, nil
}
