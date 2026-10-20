// Package contentlog implements explicitly enabled, redacted content logging.
// Transport headers and configuration are never accepted by this package.
package contentlog

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/joel-g/offload/internal/logfile"
	"github.com/joel-g/offload/internal/metrics"
)

const MaxTextBytes = 65536
const MaxRecordBytes = 1 << 20
const Redacted = "[REDACTED: credential-like content]"

// ModelLabel limits diagnostic metadata without restricting the API request ID.
func ModelLabel(model string, knownSecrets ...string) string {
	for _, secret := range knownSecrets {
		if secret != "" && strings.Contains(model, secret) {
			return ""
		}
	}
	if !metrics.ValidModel(model) || credentials.MatchString(model) {
		return ""
	}
	return model
}

// Withhold an entire field if credential indicators occur, including multiline
// assignments and key blocks. This is deliberately conservative, not a complete
// secret detector. Unlabelled/encoded secrets still require caller judgment.
var credentials = regexp.MustCompile(`(?i)(authorization|password|passwd|api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|secret[_-]?access[_-]?key|private[_ -]?key|\bcredential|\b(bearer|basic)\s+|\btoken\b\s*["']?\s*[:=]|\bsk-[a-z0-9_-]{8,}|\bgh[pousr]_[a-z0-9]{8,}|[a-z][a-z0-9+.-]*://[^\s/]*@)`)

type Event struct {
	Version           int       `json:"version"`
	Timestamp         time.Time `json:"timestamp"`
	Command           string    `json:"command"`
	Model             string    `json:"model"`
	Outcome           string    `json:"outcome"`
	Prompt            string    `json:"prompt"`
	Response          *string   `json:"response"`
	PromptTruncated   bool      `json:"prompt_truncated"`
	ResponseTruncated bool      `json:"response_truncated"`
}

func scrub(text string, knownSecrets ...string) (string, bool) {
	for _, secret := range knownSecrets {
		if secret != "" && strings.Contains(text, secret) {
			return Redacted, false
		}
	}
	if credentials.MatchString(text) {
		return Redacted, false
	}
	if len(text) <= MaxTextBytes {
		return text, false
	}
	text = text[:MaxTextBytes]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text, true
}

// Append redacts before truncation and serialization. Only selected input and
// validated assistant output are included; never raw requests or error bodies.
func Append(path string, event Event, knownSecrets ...string) error {
	if event.Version != 1 || event.Timestamp.IsZero() || !metrics.Inference(event.Command) || (event.Model != "" && !metrics.ValidModel(event.Model)) {
		return logfile.ErrLog
	}
	switch event.Outcome {
	case "success", "invalid_input", "transport_or_output_error", "server_error", "response_error", "bounds_error", "timeout", "canceled":
	default:
		return logfile.ErrLog
	}
	if credentials.MatchString(event.Model) {
		event.Model = "redacted"
	}
	event.Prompt, event.PromptTruncated = scrub(event.Prompt, knownSecrets...)
	if event.Response != nil {
		text, truncated := scrub(*event.Response, knownSecrets...)
		event.Response = &text
		event.ResponseTruncated = truncated
	}
	b, err := json.Marshal(event)
	if err != nil || strings.ContainsRune(string(b), '\n') {
		return logfile.ErrLog
	}
	return logfile.AppendLine(path, b, MaxRecordBytes)
}
