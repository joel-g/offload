# Design

`main` creates an interrupt-aware context. The Cobra CLI validates flags and reads
one explicit source with a byte ceiling. The API client sends a non-streaming
Responses request containing `model`, `instructions`, `input`, `store:false`,
`stream:false`, and `max_output_tokens`. It returns only assistant `output_text`
from a completed response. Reasoning and tool-call output are never executed or
returned as assistant text. Refusals, incomplete responses, and server errors fail
without exposing partial content.

`ask` treats the input as the user's prompt. `summarize` and `review` use fixed
instructions that frame supplied input as untrusted data. Those instructions are
guidance to a model, not a security boundary. All commands have identical network,
storage-request, and size policies.

Optional local usage metrics use a fixed event schema and an explicit file flag.
The client extracts only bounded numeric usage fields; the CLI records normalized
outcomes and the validated requested model. Stats aggregates local records without
inference. A bounded sibling-directory lock serializes local readers and writers.
See [usage metrics](usage-metrics.md) for limitations and recovery.

The client clones Go's standard HTTP transport, disables proxies, and enforces
destination policy both at URL validation and at dialing. Default mode never
resolves arbitrary hostnames. Redirect opt-in does not imply nonlocal opt-in.
Requests use a context and an HTTP timeout. TLS uses the standard trust store.
Response bodies are read through a limit before decoding; output text is checked
before writing. Remote errors are reduced to status codes or static messages.

Normal tests use `httptest` and exercise protocol fields, parsing, cancellation,
timeouts, byte limits, redirects, endpoint policy, error redaction, and CLI output.
No model download, inference, or server configuration is part of the tests.

V1 intentionally omits streaming, tool use, repository discovery, credential
persistence, automatic model selection, automatic edits, and backend fallback.
The protocol assumes a Responses-compatible server with a completed status and
assistant message content; incompatible servers fail explicitly.
