# Local usage metrics

Use `--usage-file usage.jsonl` or an explicitly selected config with `usage_file`
to opt in. No telemetry is sent anywhere. The file location is entirely
explicit; parent directories must already exist. Prefer a private directory
outside repositories, shared folders, and automatic uploads. Choose model IDs
that do not contain personal or sensitive names: the selected model ID is recorded.

```sh
offload review --model local-model --file selected-change.diff --usage-file usage.jsonl
offload stats --usage-file usage.jsonl
offload stats --usage-file usage.jsonl --json
```

Known API response statuses are recorded separately as `response_status` and
aggregated in `response_statuses`. Structured-mode success means the HTTP/JSON
operation succeeded; it does not mean the API status is completed. Content logging
is independent; see [configuration](configuration.md).

Every recorded event has a schema version, UTC start timestamp, elapsed
milliseconds, command, optional diagnostic model label (inference only), network
scope, attempted flag, normalized outcome, and token usage. Usage metrics contain
no prompt, response, input path, endpoint URL, header, raw error, server model name,
or personal identity. Diagnostic model labels have a 128-byte ceiling and a
restricted ASCII alphabet; absent, unsafe, or credential-like labels are omitted
without restricting API model selection.
Logging is a fixed field allowlist; arbitrary server metadata is never copied.

```json
{"version":1,"timestamp":"2026-01-01T00:00:00Z","duration_ms":250,"command":"ask","model":"local-model","scope":"loopback_only","attempted":true,"outcome":"success","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}
```

Outcomes are `success`, `invalid_input`, `transport_or_output_error`,
`server_error`, `response_error`, `bounds_error`, `timeout`, or `canceled`.
Elapsed time covers the command handler, including input reading and result
writing, but excludes flag parsing and metrics writing. Help, stats, invalid
commands, and flag/argument parsing failures are not recorded. Validation failures
inside a recognized command are recorded with `attempted:false`.

An attempt means the client called its HTTP operation, not proof that a server
received or completed it. Redirects are part of the same attempt. A canceled or
failed attempt may still have consumed server tokens. Successful server generation
followed by output failure counts as a failed invocation and can retain known
usage. Metrics recording failure does not change the recorded command outcome.

## Reading totals

`stats` separates loopback-only inference, inference with nonlocal access allowed,
and service checks (`health` and `models`). Enabling `--allow-nonlocal` always puts
an invocation in the nonlocal-allowed group, even when its initial URL is loopback.
This conservative distinction avoids attributing possible remote work to local
inference. A loopback service could itself forward requests; the client cannot
verify where the model actually runs.

For each inference group, the summary includes invocations, attempts, successes,
failed attempts, and invocations that never attempted HTTP. Per-field token totals
include `known_sum`, `known_calls`, and `unknown_calls` across attempted inference
calls, including failed calls. Non-attempts and service checks are excluded from
token accounting. The `commands` object provides per-command invocation counts.

Only server-reported integer `input_tokens`, `output_tokens`, and `total_tokens`
in the Responses API usage object are accepted. Each must be nonnegative and no
more than one billion. Missing, null, invalid, fractional, or excessive fields
remain null independently. No total is inferred from other fields. A reported zero
is known zero; no known counts yields a null sum, displayed as `unknown`. Partial
known totals are not a complete usage estimate. There are no hosted-token savings,
cost estimates, or tokenizer guesses. Server counts are unverified claims.

## Files, retention, and failures

New files request mode `0600` and lock directories `0700`. On Unix, existing files
with group/other permissions are refused. On Windows these modes do not establish
private ACLs: files inherit the parent directory's ACLs, so use a directory already
restricted to your account. Regular files only; log symlinks are refused. Use a
trusted parent directory and do not modify or alias files externally while active.
These rules are not a defense against another process with access to your account.

Cooperating readers and writers serialize through an exclusive sibling directory
named `<usage-file>.lock`. They wait at most two seconds, then report a sanitized
failure. Writes append one bounded JSON line and sync it before releasing the lock.
This protocol targets local filesystems, not shared/network filesystem locking.
A crash can leave a lock or partial line; locks are never automatically broken.
After confirming all users of the log have stopped, remove a stale lock manually.

The log has a 32 MiB ceiling and each record has a 4096-byte ceiling. There is no
automatic deletion, rotation, or retention expiry: records persist until you
delete them. Stop writers before archiving or replacing a full log. Stats rejects
malformed complete records without echoing their content. An unterminated final
fragment is ignored with `partial_tail_ignored:true` (or a plain warning). Appends
refuse such a tail; inspect or replace the file manually while writers are stopped.
Stats takes a locked snapshot and never repairs or rewrites content.

Logging failure is visible on stderr. If the command otherwise succeeded it exits
7; its normal stdout may already have been emitted. If the command also failed,
its original exit code is retained and a second sanitized error reports logging
failure. In JSON mode stderr can therefore contain two JSON error lines. A write,
sync, or close failure can leave an event present or partial. Do not automatically
repeat inference to repair metrics: that could duplicate work. Hard process kills
may omit an event entirely. These records are operational counters, not a billing
ledger or a complete audit trail.
