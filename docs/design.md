# Design

The CLI separates client configuration from request fields. `client` controls
transport and local resource limits; `request` contains API defaults. Explicit
input, generic JSON field overrides, and named API flags build the request. The
client forwards optional API fields rather than manufacturing generation defaults.

`responses` sends a JSON object to `/v1/responses` and preserves the full JSON
response. `ask` sends text without instructions; `summarize` and `review` add minimal,
overridable task instructions. Plain helper output projects completed assistant
text; structured output retains all response data, including tool calls and partial
responses. No tool dispatch, shell execution, model-management call, or source edit
is performed by the CLI. Server-hosted tools are controlled by explicit API fields.

Transport uses Go's HTTP client with cancellation, a deadline, a response byte
limit, redirect validation, and no ambient proxies. Loopback is the default policy;
nonlocal use is configurable. Optional bearer authentication reads a named
environment variable. Credentials are stripped on cross-origin redirects and are
never included in logs. Error messages are generated locally without server bodies.

Configuration is explicitly selected, size-limited, and schema-checked. Separate
usage and content logs use a shared bounded append/lock implementation. Usage has a
metadata allowlist. Content requires explicit enablement and redacts credential-like
input/output fields before truncation. Neither logger accepts HTTP headers or raw
configuration. Logs must not alias each other, the config, or the selected input.

Tests use mock servers, temporary logs, and cooperating subprocesses. They cover
request omission/overrides, response preservation, transport controls, config
precedence, redaction, metrics aggregation, bounds, and concurrent appends. Real
server compatibility and inference must be tested separately.
