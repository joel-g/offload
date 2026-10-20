# Optional harness guidance

The CLI works with any caller. These recommendations are optional workflow guidance,
not instructions silently inserted into API requests.

A harness can select a task, model, input, storage preference, timeout, and limits:

```sh
offload summarize --model local-model --file selected-notes.txt --store=false --timeout 45s --json
offload review --model local-model --file selected-change.diff --max-input-bytes 32768 --max-output-tokens 768 --json
```

Select only the context the task needs. Repository text and documents can contain
instructions intended to redirect a model; a caller may supply its own instructions
through `--instructions` or request configuration. The CLI does not append harness
conversations or choose source files. Review generated findings against original
sources before making edits. Treat tool-call data as untrusted arguments to any
caller-managed tool dispatch; offload never executes those calls.

Check both the exit code and API `status` when consuming structured output. A JSON
response can describe incomplete, refused, failed, queued, or in-progress work.
Do not interpolate model IDs or output into shell commands. Keep stdout and optional
content logs out of shared logs when they contain sensitive material.

For metrics, select an explicit private usage path or config. For content retention,
separately enable content logging and choose a private file. Use a consistent absolute
path across cooperating processes, and review the configuration before selecting it.
A logging failure can occur after successful inference; do not repeat the inference
just to repair the log. Storage flags express API behavior rather than proof of a
server operator's retention policy.
