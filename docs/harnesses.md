# Harness integration

A harness may invoke the same CLI used by a human. Give it a small task, an
explicit model, a selected input file or stdin, a timeout, and output limits.
Examples apply equally to coding harnesses and ordinary scripts:

```sh
offload summarize --model local-model --file selected-notes.txt --timeout 45s --json
offload review --model local-model --file selected-change.diff --max-input-bytes 32768 --max-output-tokens 768 --json
```

The caller prepares the selected file. `offload` never chooses repository files,
collects history, reads private configuration, or appends harness conversations.
Do not pass secrets or unnecessary context. Treat source comments, diffs, and
documents as untrusted input: they may contain instructions intended to redirect
the model. A summary/review instruction cannot guarantee injection resistance.

Check the exit status before consuming stdout. Parse JSON when integrating with a
program. Treat returned text and advertised model IDs as untrusted data; do not
interpolate them into shell commands. Review findings against the original source
before editing, executing anything, or approving a change. Keep stdout out of
shared logs if the requested output may contain sensitive material.

Keep loopback-only defaults unless the user explicitly chooses a remote inference
service and understands where the selected input will be sent. A harness should
not automatically retry policy failures with `--allow-nonlocal` or
`--allow-redirects`, or raise limits after a bounds failure.
