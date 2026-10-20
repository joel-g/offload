# offload

A small Go CLI for compatible Responses APIs, usable from terminals, scripts, and
coding harnesses. It sends explicitly selected input, returns API data or text,
and never executes model tools or edits source files.

## Build

Requires Go 1.23 or newer.

```sh
go build -o offload .
go test ./...
go vet ./...
```

Use `offload.exe` on Windows. Dependencies are pinned in `go.mod` and `go.sum`.
Normal tests use mock servers; they do not download models or run real inference.

## Commands

```sh
offload health
offload models
offload ask --model local-model --file question.txt
offload summarize --model local-model --file notes.txt
offload review --model local-model --file change.diff --json
offload responses --file request.json
offload ask --config config.json --stdin
offload stats --config config.json --json
```

`ask`, `summarize`, and `review` accept UTF-8 text. `responses` accepts a JSON
request object and writes the complete JSON response. Select exactly one explicit
source, `--file` or `--stdin`; files must be regular files. There is no repository
crawling or automatic context collection.

`ask` supplies no instructions. `summarize` and `review` are convenience commands
with short task instructions; override them with `--instructions`, `--set`, or
configuration. They impose no tool-use or workflow policy on the model.

### Model selection

`--model` sets the request's `model` field exactly; it does not call a load/unload
endpoint or inspect which model is currently loaded. A model can also come from
configuration or a raw request. If absent everywhere, the field is omitted: the
server chooses its behavior and may require an explicit model. There is no silent
client fallback to another model.

LM Studio can load the requested model on demand when JIT loading is enabled.
Auto-Evict may unload a previous JIT-loaded model. `models` lists models visible to
the server, which may include downloaded models that are not currently loaded.
See LM Studio's [loading documentation](https://lmstudio.ai/docs/developer/core/ttl-and-auto-evict)
and [model-list documentation](https://lmstudio.ai/docs/developer/openai-compat/models).
These server behaviors are separate from offload's request construction.

### API options

Unspecified optional API fields are omitted. In particular, offload does **not**
force `store:false`, `stream:false`, a token limit, temperature, or reasoning effort.
Server defaults apply. Request storage behavior explicitly if needed:

```sh
offload ask --model local-model --file question.txt --store=false --max-output-tokens 1024
offload ask --file question.txt --set 'temperature=0.2' --set 'reasoning={"effort":"low"}'
```

`--set name=JSON` forwards any top-level request field. Nested objects are replaced,
not merged. Shell quoting varies; use a JSON request file for complex values:

```json
{"model":"local-model","input":"Explain this algorithm.","store":false,"max_output_tokens":1024}
```

Tools, structured inputs, continuation IDs, reasoning settings, and provider
extensions can be supplied through raw requests, `--set`, or `request` in config.
The server validates and implements those fields. Tool-call responses are returned
as data; offload has no local tool execution loop. Requested server-hosted tools
may run on the server according to its own API behavior.

Supported endpoints and limitations are defined in [API compatibility](docs/api-compatibility.md).
This is a non-streaming Responses client, not complete OpenAI or LM Studio API parity.

## Configuration and logging

Use `--config config.json` to select a file explicitly. There is no automatic
repository/home-directory config search or environment-based config discovery.
[config.example.json](config.example.json) keeps both logging channels off.

```json
{
  "client": {"timeout":"2m"},
  "request": {"model":"local-model","store":false},
  "usage_file":"usage.jsonl",
  "content_logging":{"enabled":false,"file":"content.jsonl"}
}
```

Usage metrics record counts, timings, outcomes, response status, and reported token
usage. Content logging is separate and requires explicit enablement plus its own
file. Set `content_logging.enabled:true` or use `--log-content --content-file ...`.
It records redacted input/output content, never HTTP traffic or configuration.
Credential redaction is conservative but cannot identify every secret; opt-in
content logs can retain sensitive material. Keep logs outside source control.

Flags override configuration. For API fields the order is configuration defaults,
command input/raw request, `--set`, then dedicated API flags. Config-relative file
paths resolve beside the config; CLI paths resolve from the working directory.
`--log-content=false` disables configured content logging and `--usage-file=`
disables configured metrics. [Configuration details](docs/configuration.md).

## Client operational defaults

| Setting | Default | Override |
| --- | --- | --- |
| Endpoint | `http://127.0.0.1:1234` | `--endpoint` / `client.endpoint` |
| HTTP deadline | 2m | `--timeout` / `client.timeout` |
| Selected input limit | 65536 bytes | `--max-input-bytes` / `client.max_input_bytes` |
| Rendered inference output limit | 65536 bytes | `--max-output-bytes` / `client.max_output_bytes` |
| HTTP response limit | 8 MiB | `--max-response-bytes` / `client.max_response_bytes` |
| Nonlocal destinations | denied | `--allow-nonlocal` / `client.allow_nonlocal` |
| Redirects | denied | `--allow-redirects` / `client.allow_redirects` |
| Authentication | none | `--api-key-env` / `client.api_key_env` |
| Usage/content logs | disabled | explicit logging policy or flags |

Byte limits must be positive and representable; the old fixed input/output ceilings
and generation-token ceiling are removed. Byte limits bound client I/O, not model
behavior. Raising them permits larger allocations. The output limit includes JSON
encoding but excludes the final newline. The HTTP deadline covers connection/body
reading, not waiting for stdin. Interrupting cancels an active request.

Loopback defaults, redirect checks, and disabled ambient proxies are client
network controls, not API requirements. Remote use is explicitly configurable;
prefer HTTPS. Credentials are read only from the explicitly named environment
variable and never persisted. Authorization is removed on cross-origin redirects.
URL-embedded credentials are rejected. No TLS verification bypass is provided.

## Results and errors

Plain text helpers return assistant text. `--json` returns a command envelope with
the full API `response`; `responses` returns the full API object directly, including
tool calls, reasoning, refusals, incomplete output, and API status. Structured-mode
exit 0 means an HTTP/JSON response was received, not that inference completed;
inspect `status`. Text mode reports non-text or incomplete responses as errors.
`models` prints IDs and `health` checks `/v1/models` without inference.

Errors on stderr are static messages/status codes, never raw server error bodies,
URLs, input paths, credential values, or headers. JSON errors are emitted when
`--json` was parsed. Help and early argument-parsing errors may be plain text.

| Exit | Meaning |
| --- | --- |
| 0 | Client operation succeeded; inspect API status in structured output |
| 2 | Invalid command, config, input, or unsupported request mode |
| 3 | Transport/policy or output write failure |
| 4 | HTTP failure, or failed inference in text mode |
| 5 | Malformed response, or unavailable completed text in text mode |
| 6 | Client byte limit exceeded |
| 7 | Logging or stats read failure (stdout may already contain a result) |
| 124 | HTTP timeout |
| 130 | Request canceled |

[Design](docs/design.md), [usage metrics](docs/usage-metrics.md), and
[optional harness guidance](docs/harnesses.md).
