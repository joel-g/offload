# Configuration and content logging

Select configuration with `--config config.json`. No config is automatically read
from a repository, home directory, or environment variable. The JSON file is limited
to 64 KiB, must be a regular UTF-8 file, and rejects unknown policy fields, duplicate
object keys, and ambiguous null policy values. The `request` object accepts arbitrary
API fields, including null values. A selected missing or malformed file is an error;
there is no silent fallback that might enable unexpected behavior.

```json
{
  "client": {
    "endpoint": "http://127.0.0.1:1234",
    "timeout": "2m",
    "allow_nonlocal": false,
    "allow_redirects": false,
    "max_input_bytes": 65536,
    "max_output_bytes": 65536,
    "max_response_bytes": 8388608,
    "api_key_env": ""
  },
  "request": {"model":"local-model","store":false},
  "usage_file": "usage.jsonl",
  "content_logging": {"enabled":false,"file":"content.jsonl"}
}
```

The example explicitly requests `store:false`; an omitted field leaves the server's
API default intact. Client limits and network rules are local operational settings,
not API fields. Credentials are never read from JSON: `api_key_env` names an
environment variable, and only its value becomes the HTTP bearer credential.
Missing/empty selected variables produce a redacted error. No credential variable
is implicitly selected. Use HTTPS when sending credentials to a remote server.

## Precedence

Client/logging settings: built-in operational defaults < explicit config < explicitly
provided flags. False booleans and empty strings on flags are real overrides.
`--log-content=false` disables configured content logging, `--usage-file=` disables
metrics, and `--api-key-env=` disables configured authentication.

API fields: `request` defaults < selected raw request or helper input < repeated
`--set name=JSON` fields (last wins) < named API flags (`--model`, `--instructions`,
`--store`, `--max-output-tokens`). Nested objects are replaced as a whole. Text
helpers set `input` from the selected file/stdin; only summarize/review add task
instructions, and only when instructions were not configured. Named API flags are
omitted unless explicitly supplied. Use `responses` for a request without helper
input/instruction synthesis. Server-side validation decides supported API values.

Relative log paths in config resolve beside that config file. Relative flag paths
resolve from the process working directory. Config never changes the working
directory. Logs must be distinct from each other and from the config and selected
input file; existing hard-link and symlink aliases are checked. Use trusted parent
directories and do not externally replace files while commands run.

## Separate logging channels

Both channels are off by default. Setting `usage_file` opts into metadata metrics;
it does not enable content logging. Setting a content file alone does not enable
content logging; `enabled:true` or `--log-content` is also required. These choices
apply only to invocations that explicitly select the config/flags. `stats` reads
only the usage file and records neither channel.

Content logs contain a version, timestamp, command, diagnostic model label, normalized
client outcome, selected API input, API output array, and truncation flags. Input
strings are logged as strings; structured input/output is JSON-encoded text. Missing
output is null. Only attempted inference calls are eligible. Instructions, unrelated
request fields, HTTP headers, endpoint/config/input paths, raw error bodies, and
credential environment values are not captured. Model labels that are oversized,
credential-like, or outside the diagnostic alphabet are omitted without changing
the API model field.

The entire input or output field is withheld when common credential indicators are
detected, including authorization, password/API-key/token assignments, bearer/basic
values, private-key markers, and URL userinfo. Any occurrence of the selected bearer
credential is also withheld, even without a label. Redaction runs before truncation and
cannot be disabled. It is deliberately conservative and can over-redact normal text.
It cannot guarantee detection of unlabelled, encoded, or otherwise disguised secrets.
Content logging is therefore an explicit decision to retain potentially sensitive
material, not a safe way to archive arbitrary secrets. Redaction never changes the
request sent to the server or the response returned to stdout.

Each logged text field is capped at 64 KiB with a truncation indicator. JSON content
records are capped at 1 MiB and each log file at 32 MiB. These are local logging
limits; inference I/O and API token settings are independent. Usage records remain
content-free even when content logging is enabled. Retention has no automatic expiry:
stop writers before rotating, archiving, or deleting logs.

Both log channels share the local exclusive sibling-directory lock and permission
rules described in [usage metrics](usage-metrics.md). New files request mode 0600;
Windows files inherit directory ACLs, so select an already-private directory. Stale
locks and partial final lines require manual recovery after all writers stop. Neither
logger automatically repairs content. Logging failure is visible and exits 7 after
otherwise successful work; normal output may already exist. Original command failures
retain their exit code and receive a separate logging error. Do not rerun inference
automatically to repair a log. Hard kills or write/sync failures can leave missing or
partial records.
