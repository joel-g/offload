// Package api implements the bounded, non-streaming Responses protocol.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/joel-g/offload/internal/metrics"
)

const MaxResponseBytes = 8 << 20

// Error contains only a locally generated message, never remote response content.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string            { return e.Message }
func Fail(code int, message string) error { return &Error{code, message} }

type Config struct {
	Endpoint                      string
	AllowNonlocal, AllowRedirects bool
	Timeout                       time.Duration
}
type Client struct {
	base *url.URL
	http *http.Client
}

func validate(u *url.URL, nonlocal bool) error {
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return Fail(2, "endpoint must be an HTTP(S) URL without credentials, query, or fragment")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if !nonlocal && !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return Fail(2, "non-loopback destination requires --allow-nonlocal")
	}
	return nil
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, Fail(2, "invalid endpoint URL")
	}
	if err = validate(u, cfg.AllowNonlocal); err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		return nil, Fail(2, "timeout must be positive")
	}
	// Disable all ambient proxies. Resolve localhost locally rather than via DNS.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: cfg.Timeout}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(host, "localhost") {
			host = "127.0.0.1"
		}
		if !cfg.AllowNonlocal {
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsLoopback() {
				return nil, errors.New("destination denied")
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
	}
	client := &http.Client{Timeout: cfg.Timeout, Transport: transport}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !cfg.AllowRedirects {
			return errors.New("redirect denied")
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return validate(req.URL, cfg.AllowNonlocal)
	}
	return &Client{u, client}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) request(ctx context.Context, method, path string, body any, target any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return Fail(5, "cannot encode request")
		}
		reader = bytes.NewReader(b)
	}
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return Fail(2, "cannot construct request")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return transportError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Fail(4, fmt.Sprintf("server returned HTTP %d", resp.StatusCode))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return transportError(ctx, err)
	}
	if len(b) > MaxResponseBytes {
		return Fail(6, "response exceeds byte limit")
	}
	if json.Unmarshal(b, target) != nil {
		return Fail(5, "invalid JSON response")
	}
	return nil
}

func transportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return Fail(130, "request canceled")
	}
	var ne net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return Fail(124, "request timed out")
	}
	return Fail(3, "HTTP request failed (connection, TLS, or redirect policy)")
}

type ResponseRequest struct {
	Model           string `json:"model"`
	Instructions    string `json:"instructions"`
	Input           string `json:"input"`
	Store           bool   `json:"store"`
	Stream          bool   `json:"stream"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

type response struct {
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
	Usage  json.RawMessage `json:"usage"`
	Output []struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

func (c *Client) Generate(ctx context.Context, model, instruction, input string, tokens, maxOutput int) (string, metrics.Usage, error) {
	var result response
	var usage metrics.Usage
	if err := c.request(ctx, http.MethodPost, "/v1/responses", ResponseRequest{model, instruction, input, false, false, tokens}, &result); err != nil {
		return "", usage, err
	}
	usage = metrics.ParseUsage(result.Usage)
	if (len(result.Error) > 0 && string(result.Error) != "null") || result.Status == "failed" {
		return "", usage, Fail(4, "inference failed")
	}
	if result.Status != "completed" {
		return "", usage, Fail(5, "inference did not complete; no partial output returned")
	}
	var parts []string
	for _, item := range result.Output {
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "refusal" {
				return "", usage, Fail(5, "model refused the request")
			}
			if part.Type == "output_text" {
				parts = append(parts, part.Text)
			}
		}
	}
	text := strings.Join(parts, "\n")
	if strings.TrimSpace(text) == "" {
		return "", usage, Fail(5, "response contains no assistant text")
	}
	if len(text) > maxOutput {
		return "", usage, Fail(6, "output exceeds byte limit")
	}
	return text, usage, nil
}

func (c *Client) Models(ctx context.Context) ([]string, error) {
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "/v1/models", nil, &result); err != nil {
		return nil, err
	}
	if result.Data == nil {
		return nil, Fail(5, "response contains no model list")
	}
	models := make([]string, 0, len(result.Data))
	for _, model := range result.Data {
		if !metrics.ValidModel(model.ID) {
			return nil, Fail(5, "response contains invalid model ID")
		}
		models = append(models, model.ID)
	}
	return models, nil
}
