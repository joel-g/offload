// Package cli provides the harness-independent command interface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/joel-g/offload/internal/api"
	"github.com/joel-g/offload/internal/config"
	"github.com/joel-g/offload/internal/contentlog"
	"github.com/joel-g/offload/internal/metrics"
	"github.com/spf13/cobra"
)

type options struct {
	apiKeyEnv                                           string
	configFile, usageFile, contentFile                  string
	endpoint, model, file, instructions                 string
	stdin, json, nonlocal, redirects, logContent, store bool
	timeout                                             time.Duration
	inputBytes, outputBytes, tokens                     int
	responseBytes                                       int64
	set                                                 []string
	request                                             map[string]json.RawMessage
}
type result struct {
	Command  string          `json:"command"`
	Text     string          `json:"text,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
	Models   *[]string       `json:"models,omitempty"`
	Healthy  bool            `json:"healthy,omitempty"`
}

func Execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	o := &options{}
	var event *metrics.Event
	var contentEvent *contentlog.Event
	var started time.Time
	var apiCredential string
	root := &cobra.Command{Use: "offload", Short: "Call a compatible Responses API with explicit input", SilenceErrors: true, SilenceUsage: true}
	root.SetArgs(args)
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)
	root.CompletionOptions.DisableDefaultCmd = true
	flags := root.PersistentFlags()
	flags.StringVar(&o.endpoint, "endpoint", "http://127.0.0.1:1234", "Server base URL (without /v1)")
	flags.StringVar(&o.apiKeyEnv, "api-key-env", "", "Read a bearer credential from this environment variable (never persisted)")
	flags.BoolVar(&o.json, "json", false, "Emit structured results and errors")
	flags.DurationVar(&o.timeout, "timeout", 2*time.Minute, "Client HTTP deadline")
	flags.BoolVar(&o.nonlocal, "allow-nonlocal", false, "Allow sending data to non-loopback destinations")
	flags.BoolVar(&o.redirects, "allow-redirects", false, "Allow redirects (destination policy still applies)")
	flags.Int64Var(&o.responseBytes, "max-response-bytes", api.MaxResponseBytes, "Client HTTP response byte limit")
	flags.StringVar(&o.usageFile, "usage-file", "", "Local usage JSONL path; empty disables metrics; stats reads it")
	flags.StringVar(&o.configFile, "config", "", "Read this explicit JSON configuration file")
	flags.BoolVar(&o.logContent, "log-content", false, "Opt in to redacted input/output logging")
	flags.StringVar(&o.contentFile, "content-file", "", "Separate content log path (does not enable logging by itself)")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error { return configure(cmd, o) }
	for _, name := range []string{"ask", "summarize", "review", "responses", "models", "health"} {
		name := name
		cmd := &cobra.Command{Use: name, Args: cobra.NoArgs, Short: map[string]string{
			"ask": "Send text input to the Responses API", "summarize": "Text helper with an overridable summary instruction", "review": "Text helper with an overridable review instruction", "responses": "Send a JSON request object and return the full JSON response", "models": "List advertised model IDs", "health": "Check the model-list endpoint without inference",
		}[name]}
		if metrics.Inference(name) {
			cmd.Flags().StringVar(&o.model, "model", "", "API model ID override (otherwise omitted or read from request/config)")
			cmd.Flags().StringVar(&o.file, "file", "", "Read this input file (JSON object for responses, text for helpers)")
			cmd.Flags().BoolVar(&o.stdin, "stdin", false, "Read input from stdin")
			cmd.Flags().IntVar(&o.inputBytes, "max-input-bytes", 65536, "Client input byte limit")
			cmd.Flags().IntVar(&o.outputBytes, "max-output-bytes", 65536, "Client rendered output byte limit")
			cmd.Flags().IntVar(&o.tokens, "max-output-tokens", 0, "Set API max_output_tokens (omitted unless specified)")
			cmd.Flags().BoolVar(&o.store, "store", false, "Set API store true/false (omitted unless specified)")
			cmd.Flags().StringVar(&o.instructions, "instructions", "", "Set API instructions, including an explicit empty string")
			cmd.Flags().StringArrayVar(&o.set, "set", nil, "Set any top-level API field as name=JSON; repeat for multiple fields")
		}
		cmd.RunE = func(cmd *cobra.Command, _ []string) error {
			started = time.Now()
			event = &metrics.Event{Version: 1, Timestamp: started.UTC(), Command: name, Scope: "loopback_only"}
			if o.nonlocal {
				event.Scope = "nonlocal_allowed"
			}
			apiKey := ""
			if o.apiKeyEnv != "" {
				var ok bool
				apiKey, ok = os.LookupEnv(o.apiKeyEnv)
				if !ok || apiKey == "" {
					return api.Fail(2, "configured credential environment variable is missing or empty")
				}
			}
			apiCredential = apiKey
			client, err := api.New(api.Config{Endpoint: o.endpoint, AllowNonlocal: o.nonlocal, AllowRedirects: o.redirects, Timeout: o.timeout, MaxResponseBytes: o.responseBytes, APIKey: apiKey})
			if err != nil {
				return err
			}
			defer client.Close()
			r := result{Command: name}
			if !metrics.Inference(name) {
				event.Attempted = true
				models, err := client.Models(cmd.Context())
				if err != nil {
					return err
				}
				if name == "models" {
					r.Models = &models
				} else {
					r.Healthy = true
				}
			} else {
				body, err := requestBody(cmd, o, in, name)
				if err != nil {
					return err
				}
				var model string
				_ = json.Unmarshal(body["model"], &model)
				event.Model = contentlog.ModelLabel(model, apiCredential)
				if stream, ok := body["stream"]; ok && string(stream) != "false" && string(stream) != "null" {
					return api.Fail(2, "streaming is not supported; omit stream or set false")
				}
				event.Attempted = true
				if o.logContent {
					prompt := string(body["input"])
					var text string
					if json.Unmarshal(body["input"], &text) == nil {
						prompt = text
					}
					contentEvent = &contentlog.Event{Version: 1, Timestamp: event.Timestamp, Command: name, Model: event.Model, Prompt: prompt}
				}
				r.Response, event.Usage, err = client.CreateResponse(cmd.Context(), body)
				if err != nil {
					return err
				}
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(r.Response, &fields)
				var status string
				_ = json.Unmarshal(fields["status"], &status)
				event.ResponseStatus = metrics.ResponseStatus(status)
				if contentEvent != nil {
					if output, ok := fields["output"]; ok {
						text := string(output)
						contentEvent.Response = &text
					}
				}
				if name != "responses" && !o.json {
					r.Text, err = api.Text(r.Response, o.outputBytes)
					if err != nil {
						return err
					}
				}
			}
			var rendered []byte
			if name == "responses" {
				rendered = r.Response
			} else if o.json {
				rendered, err = json.Marshal(r)
			} else if r.Models != nil {
				rendered = []byte(strings.Join(*r.Models, "\n"))
			} else if r.Healthy {
				rendered = []byte("ok")
			} else {
				rendered = []byte(r.Text)
			}
			if err != nil {
				return api.Fail(5, "cannot encode output")
			}
			if metrics.Inference(name) && len(rendered) > o.outputBytes {
				return api.Fail(6, "output exceeds byte limit")
			}
			if len(rendered) > 0 {
				if _, err = fmt.Fprintln(out, string(rendered)); err != nil {
					return api.Fail(3, "cannot write output")
				}
			}
			return nil
		}
		root.AddCommand(cmd)
	}
	root.AddCommand(&cobra.Command{Use: "stats", Short: "Summarize local usage without inference", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if o.usageFile == "" {
			return api.Fail(2, "stats requires a usage file from --usage-file or config")
		}
		summary, err := metrics.Read(o.usageFile)
		if err != nil {
			return api.Fail(7, "cannot read usage log: unavailable, unsafe, oversized, or malformed")
		}
		if o.json {
			err = json.NewEncoder(out).Encode(summary)
		} else {
			err = printStats(out, summary)
		}
		if err != nil {
			return api.Fail(3, "cannot write output")
		}
		return nil
	}})
	err := root.ExecuteContext(ctx)
	invocationOutcome := outcome(err)
	recordFailure := func(message string) {
		loggingError := &api.Error{Code: 7, Message: message}
		if err == nil {
			err = loggingError
		} else {
			report(errOut, o.json, loggingError)
		}
	}
	if event != nil && o.usageFile != "" {
		event.DurationMS = time.Since(started).Milliseconds()
		event.Outcome = invocationOutcome
		if metrics.Append(o.usageFile, *event) != nil {
			recordFailure("cannot record usage: log unavailable, unsafe, full, or incomplete; event may be missing")
		}
	}
	if contentEvent != nil {
		contentEvent.Outcome = invocationOutcome
		if contentlog.Append(o.contentFile, *contentEvent, apiCredential) != nil {
			recordFailure("cannot record content: log unavailable, unsafe, full, or incomplete; event may be missing")
		}
	}
	if err == nil {
		return 0
	}
	safe := &api.Error{Code: 2, Message: "invalid command or arguments; use --help"}
	var known *api.Error
	if errors.As(err, &known) {
		safe = known
	}
	report(errOut, o.json, safe)
	return safe.Code
}

func configure(cmd *cobra.Command, o *options) error {
	var p config.Policy
	if o.configFile != "" {
		var err error
		p, err = config.Load(o.configFile)
		if err != nil {
			return api.Fail(2, "cannot read config: expected a bounded JSON object with valid fields")
		}
	}
	changed := cmd.Flags().Changed
	if changed("usage-file") {
		p.UsageFile = o.usageFile
	}
	if changed("log-content") {
		p.Content.Enabled = o.logContent
	}
	if changed("content-file") {
		p.Content.File = o.contentFile
	}
	if p.Validate(o.configFile, o.file) != nil {
		return api.Fail(2, "logging policy requires a content file when enabled and distinct log, config, and input files")
	}
	o.usageFile, o.contentFile, o.logContent = p.UsageFile, p.Content.File, p.Content.Enabled
	o.request = p.Request
	if p.Client.APIKeyEnv != nil && !changed("api-key-env") {
		o.apiKeyEnv = *p.Client.APIKeyEnv
	}
	if p.Client.Endpoint != nil && !changed("endpoint") {
		o.endpoint = *p.Client.Endpoint
	}
	if p.Client.Timeout != nil && !changed("timeout") {
		v, err := time.ParseDuration(*p.Client.Timeout)
		if err != nil {
			return api.Fail(2, "invalid configured HTTP timeout")
		}
		o.timeout = v
	}
	if p.Client.AllowNonlocal != nil && !changed("allow-nonlocal") {
		o.nonlocal = *p.Client.AllowNonlocal
	}
	if p.Client.AllowRedirects != nil && !changed("allow-redirects") {
		o.redirects = *p.Client.AllowRedirects
	}
	if p.Client.MaxInputBytes != nil && !changed("max-input-bytes") {
		o.inputBytes = *p.Client.MaxInputBytes
	}
	if p.Client.MaxOutputBytes != nil && !changed("max-output-bytes") {
		o.outputBytes = *p.Client.MaxOutputBytes
	}
	if p.Client.MaxResponseBytes != nil && !changed("max-response-bytes") {
		o.responseBytes = *p.Client.MaxResponseBytes
	}
	if o.responseBytes < 1 {
		return api.Fail(2, "response byte limit must be positive")
	}
	return nil
}

func requestBody(cmd *cobra.Command, o *options, in io.Reader, name string) (map[string]json.RawMessage, error) {
	if o.stdin == (o.file != "") {
		return nil, api.Fail(2, "choose exactly one of --stdin or --file")
	}
	if o.inputBytes < 1 || o.outputBytes < 1 || o.inputBytes == int(^uint(0)>>1) {
		return nil, api.Fail(2, "client byte limits must be positive and representable")
	}
	reader := in
	if o.file != "" {
		f, err := os.Open(o.file)
		if err != nil {
			return nil, api.Fail(2, "cannot open input file")
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil || !stat.Mode().IsRegular() {
			return nil, api.Fail(2, "input file must be a regular file")
		}
		reader = f
	}
	b, err := io.ReadAll(io.LimitReader(reader, int64(o.inputBytes)+1))
	if err != nil {
		return nil, api.Fail(2, "cannot read input")
	}
	if len(b) > o.inputBytes {
		return nil, api.Fail(6, "input exceeds byte limit")
	}
	if !utf8.Valid(b) {
		return nil, api.Fail(2, "input must be UTF-8")
	}
	body := make(map[string]json.RawMessage)
	for key, value := range o.request {
		body[key] = value
	}
	put := func(key string, value any) { body[key], _ = json.Marshal(value) }
	if name == "responses" {
		var raw map[string]json.RawMessage
		if json.Unmarshal(b, &raw) != nil || raw == nil {
			return nil, api.Fail(2, "responses input must be a JSON object")
		}
		for key, value := range raw {
			body[key] = value
		}
	} else {
		put("input", string(b))
		if _, exists := body["instructions"]; !exists {
			if name == "summarize" {
				put("instructions", "Summarize the supplied input.")
			}
			if name == "review" {
				put("instructions", "Review the supplied code or diff.")
			}
		}
	}
	for _, field := range o.set {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" || !json.Valid([]byte(value)) {
			return nil, api.Fail(2, "--set requires name=JSON")
		}
		body[key] = json.RawMessage(strings.TrimSpace(value))
	}
	if cmd.Flags().Changed("model") {
		put("model", o.model)
	}
	if cmd.Flags().Changed("instructions") {
		put("instructions", o.instructions)
	}
	if cmd.Flags().Changed("store") {
		put("store", o.store)
	}
	if cmd.Flags().Changed("max-output-tokens") {
		put("max_output_tokens", o.tokens)
	}
	return body, nil
}
func report(w io.Writer, machine bool, err *api.Error) {
	if machine {
		_ = json.NewEncoder(w).Encode(struct {
			Error *api.Error `json:"error"`
		}{err})
	} else {
		_, _ = fmt.Fprintln(w, "offload:", err.Message)
	}
}

func outcome(err error) string {
	if err == nil {
		return "success"
	}
	var known *api.Error
	if !errors.As(err, &known) {
		return "invalid_input"
	}
	switch known.Code {
	case 3:
		return "transport_or_output_error"
	case 4:
		return "server_error"
	case 5:
		return "response_error"
	case 6:
		return "bounds_error"
	case 124:
		return "timeout"
	case 130:
		return "canceled"
	default:
		return "invalid_input"
	}
}

func printStats(w io.Writer, s metrics.Summary) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Recorded invocations: %d\n", s.Events)
	for _, status := range []string{"completed", "failed", "incomplete", "in_progress", "queued", "cancelled"} {
		if n := s.ResponseStatuses[status]; n > 0 {
			fmt.Fprintf(&b, "API response status %s: %d\n", status, n)
		}
	}
	for _, group := range []struct {
		label  string
		counts metrics.InferenceCount
	}{{"Loopback inference", s.Loopback}, {"Nonlocal-allowed inference", s.Nonlocal}} {
		c := group.counts
		fmt.Fprintf(&b, "%s: %d invocations, %d attempted (%d succeeded, %d failed), %d not attempted\n", group.label, c.Invocations, c.Attempted, c.Succeeded, c.Failed, c.NotAttempted)
		for _, token := range []struct {
			label string
			count metrics.TokenCount
		}{{"input", c.Tokens.Input}, {"output", c.Tokens.Output}, {"total", c.Tokens.Total}} {
			sum := "unknown"
			if token.count.KnownSum != nil {
				sum = fmt.Sprint(*token.count.KnownSum)
			}
			fmt.Fprintf(&b, "  %s tokens: %s (known: %d calls; unknown: %d calls)\n", token.label, sum, token.count.KnownCalls, token.count.UnknownCalls)
		}
	}
	fmt.Fprintf(&b, "Service checks: %d invocations, %d attempted (%d succeeded, %d failed), %d not attempted\n", s.Checks.Invocations, s.Checks.Attempted, s.Checks.Succeeded, s.Checks.Failed, s.Checks.NotAttempted)
	if s.PartialTail {
		fmt.Fprintln(&b, "Warning: incomplete final record ignored; repair the log before further writes.")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
