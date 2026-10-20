# offload

A small, harness-agnostic Go CLI for sending explicit, bounded work to local
inference. Works from a terminal or a coding harness such as Codex or Claude Code.
It does not crawl repositories, run shell commands, dispatch model tools, or edit
files. Model output is advice for the caller to review.

## Build

Requires Go 1.23 or newer.

```sh
go build -o offload .
go test ./...
go vet ./...
```

Use `offload.exe` on Windows. Dependencies are pinned in `go.mod` and `go.sum`.

## Usage

Start a compatible inference server separately. The default base URL is
`http://127.0.0.1:1234`. Generation uses `POST /v1/responses` with explicit
`store:false`; there is no fallback to an API without that field. LM Studio's
Responses API is the initial target (0.4.16 compatibility baseline). The normal
test suite uses mock inference only; it does not verify an installed server.

```sh
offload health
offload models
offload ask --model local-model --file question.txt
offload summarize --model local-model --file notes.txt --json
offload review --model local-model --file change.diff
printf 'Explain this small algorithm.\n' | offload ask --model local-model --stdin
```

Choose exactly one of `--file` and `--stdin`. Inputs must be nonempty UTF-8 text.
Files must be regular files. Model IDs are explicit; select an advertised ID from
`models`. `health` checks that `/v1/models` returns a model list; it does not load a
model or prove generation works. An empty advertised list is a healthy API.

| Flag | Default | Allowed range / meaning |
| --- | --- | --- |
| `--max-input-bytes` | 65536 | 1–16777216 UTF-8 bytes |
| `--max-output-bytes` | 65536 | 1–1048576 generated text bytes |
| `--max-output-tokens` | 1024 | 1–32768, sent to server |
| `--timeout` | 2m | Positive HTTP deadline, e.g. 30s |
| `--endpoint` | http://127.0.0.1:1234 | Base URL, without `/v1` |
| `--json` | false | Structured result on stdout; errors on stderr |
| `--allow-nonlocal` | false | Explicitly permit non-loopback destinations |
| `--allow-redirects` | false | Permit redirects, subject to destination policy |

Limits reject oversized data rather than silently truncating it. The HTTP response
body has a fixed 8 MiB ceiling. Generated-text limits exclude JSON encoding and the
plain-output trailing newline. The token limit relies on server enforcement; the
client separately checks response and text byte limits. Incomplete generation is
an error with no partial output. The deadline covers HTTP connection and body read,
not time spent waiting for stdin. Interrupting cancels an active HTTP request.

Plain output is generated text, one model ID per line, or `ok`. JSON examples:

```json
{"command":"ask","model":"local-model","text":"An explanation."}
```

```json
{"command":"models","models":["local-model"]}
```

Errors use `{"error":{"code":2,"message":"--model is required"}}` on stderr
when `--json` has been parsed. Invalid flags encountered before `--json` is parsed
can produce a plain error. Help is always plain text.

| Exit code | Meaning |
| --- | --- |
| 0 | Success |
| 2 | Invalid command, configuration, or input |
| 3 | Transport/policy failure or output write failure |
| 4 | HTTP failure or failed inference |
| 5 | Malformed, empty, refused, or incomplete response |
| 6 | Input, response, or output exceeds a byte limit |
| 124 | HTTP timeout |
| 130 | Request canceled |

## Privacy and trust

Only explicitly supplied text is sent. Prompts and responses are not logged or
persisted by this CLI. Requested output is written to stdout. Error messages omit
server bodies, URLs, input paths, and raw transport errors. No credentials are
stored; embedded URL credentials are rejected. There is no credential management
or config file in v1.

`store:false` requests no storage through the Responses protocol. It does **not**
prove that every server, proxy, or operator disables logging or retention. Verify
the server's behavior and policies before supplying sensitive material.

By default only literal loopback IPs and `localhost` are accepted. `localhost` is
dialed as `127.0.0.1`, without DNS. All ambient HTTP proxies are disabled. Redirects
are denied unless separately enabled; even then, every destination must satisfy
the loopback policy unless `--allow-nonlocal` is also supplied. Nonlocal opt-in
allows data egress, including plain HTTP if configured. Prefer HTTPS for remote
servers. No TLS verification bypass is provided.

See [design](docs/design.md) and [harness integration](docs/harnesses.md).
