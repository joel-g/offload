# API compatibility

The supported wire contract is non-streaming JSON `POST /v1/responses` and
`GET /v1/models`, following the OpenAI-compatible shapes documented by
[LM Studio](https://lmstudio.ai/docs/developer/openai-compat/responses). LM Studio
0.4.16 is the initial intended integration baseline, not a claim that every field
or server version has been verified. The local automated suite uses mock servers.

Tested request behavior includes model/input/instructions, explicit true/false
storage policy, omitted defaults, token limits, temperature, reasoning objects,
tool definitions, structured input arrays, and continuation IDs. Raw requests and
`--set` forward additional fields unchanged; acceptance and semantics depend on the
server. Passing a field through is not evidence that a backend implements it.

Structured output preserves the response object rather than a hand-maintained
subset. This includes output item types unknown to the CLI, tool calls, reasoning,
refusals, partial output, and queued/in-progress/failed status. Text helper output
is intentionally narrower: completed assistant `output_text` only. Usage extraction
accepts only bounded numeric counts and known status labels for local counters.

There is no versioned full-API compatibility guarantee. Significant gaps:

- Streaming/SSE is not implemented; explicit `stream:true` is rejected before HTTP.
- Response retrieval, polling, cancellation endpoints, deletion, and input-item
  listing are not implemented. A queued/background response is returned as data.
- Chat Completions, embeddings, file uploads, and LM Studio native management APIs
  are not implemented. Structured input may contain API references, but the CLI
  does not upload or resolve local multimodal files.
- Authentication supports an explicitly selected bearer-token environment variable,
  not arbitrary header injection, credential storage, or provider-specific signing.
- HTTP error bodies are redacted, not returned as a complete API error object.
- `summarize` and `review` are convenience tasks, not standardized API endpoints.

A practical extension path is to add explicit endpoint commands and transport modes
with request/response fixtures for each capability. Keep generic JSON passthrough
for evolving request fields, add ergonomic flags only when useful, and test backend
behavior before advertising a field or version as supported. Tool execution belongs
in the caller or a separately selected server capability, not an implicit CLI loop.

## Changes from the initial implementation

Unspecified `store`, `stream`, and `max_output_tokens` are now omitted. To retain the
initial no-storage request, explicitly set `--store=false` or `request.store:false`.
This requests storage behavior; it does not establish an operator's retention policy.
`--model` is no longer mandatory and its string is forwarded without a client-specific
alphabet restriction. Missing model behavior belongs to the server. Diagnostic model
labels remain bounded and may be omitted without changing the request.

JSON helper output now includes the full `response` object instead of only extracted
text. Consumers should inspect API status; a structured client success can contain
an incomplete or failed model response. Usage stats separately count known API
response statuses so these are not confused with completed generation.
