# Cursor Agent — External Agent Contract

## Verdict: COMPATIBLE (preview)

The Cursor Agent CLI (`cursor-agent`) exposes a project-scoped **hooks** system
and resumable conversations. This adapter installs Entire hook commands into
`.cursor/hooks.json` and materializes a repo-scoped **sidecar transcript** so
Entire can create checkpoints, extract prompts and modified files, and surface
assistant responses.

> Payload field names below follow Cursor's documented hooks interface. Items
> that were not verified against a captured live payload in this environment are
> marked `(unverified)` and are validated structurally by the unit tests and the
> shared protocol-compliance suite.

## Binary

- Name: `cursor-agent`
- Print/non-interactive mode: `cursor-agent -p "<prompt>"` (unverified exact flag)
- Resume: `cursor-agent --resume <conversation-id>`, or `cursor-agent --continue`

## Hook Mechanism

- Config file: `<repo>/.cursor/hooks.json` (project scope).
- Format: JSON, `{ "version": 1, "hooks": { "<event>": [ { "command": "..." } ] } }`.
- Hook actions receive the event JSON on **stdin** and run to completion; Entire
  hook commands are wrapped as `entire hooks cursor <verb>` and never block the
  agent on failure.
- Native events used and their Entire mapping:

  | Native Cursor event   | Entire verb            | Protocol event        |
  |-----------------------|------------------------|-----------------------|
  | `beforeSubmitPrompt`  | `before-submit-prompt` | 1=SessionStart (first prompt of a conversation) / 2=TurnStart (subsequent) |
  | `afterFileEdit`       | `after-file-edit`      | none — records a modified file into the sidecar |
  | `stop`                | `stop`                 | 3=TurnEnd, carries the assistant response |
  | `sessionEnd`          | `session-end`          | 5=SessionEnd, clears the per-session marker |

- Cursor fires no dedicated session-start hook, so the **first**
  `beforeSubmitPrompt` for a `conversation_id` is mapped to SessionStart and
  later prompts to TurnStart. A per-session marker file
  (`<session-id>.started`) in the sidecar directory tracks this; `sessionEnd`
  clears it so a reused conversation id starts a fresh session. `(unverified:
  exact set of events Cursor emits may differ by version)`

- Hook input fields consumed: `conversation_id` (session id), `generation_id`,
  `hook_event_name`, `prompt`, `model`, `file_path`, `status`, `message`.

## Session Management

- Session ID source: `conversation_id` from every hook payload.
- Session directory: repo-scoped OS temp dir
  `${TMPDIR}/entire-cursor/<sha256(repo-root)[:16]>/`, keeping the sidecar out of
  the working tree and away from other agents' session dirs (first-match session
  attribution otherwise risks another agent claiming the session).
- Session file: `<session-dir>/<conversation-id>.jsonl` (sidecar).

## Transcript (sidecar)

Cursor has no single durable transcript file this adapter can portably rely on,
so the hook commands append normalized JSONL records:

```json
{"v":1,"agent":"cursor","event":"before-submit-prompt","session_id":"conv-123","ts":"...","prompt":"...","model":"..."}
{"v":1,"agent":"cursor","event":"after-file-edit","session_id":"conv-123","ts":"...","file_path":"/repo/x.go"}
{"v":1,"agent":"cursor","event":"stop","session_id":"conv-123","ts":"...","assistant_message":"..."}
```

- Positions are **record indexes**, not bytes (each hook appends whole records).
- `extract-modified-files`: de-duplicated `file_path` values from
  `after-file-edit` records after the offset.
- `extract-prompts`: `prompt` text from `before-submit-prompt` records.
- `extract-summary`: most recent non-empty `assistant_message`.
- `compact-transcript`: Entire v1 compact JSONL (base64) — prompts become user
  turns, file edits become `tool_use` blocks, stop responses become assistant
  text.

## Protocol Mapping

| Subcommand | Cursor mapping |
|---|---|
| `info` | Name `cursor`; protected dir `.cursor`; protected file `.cursor/hooks.json` |
| `detect` | `cursor-agent` lookup on `PATH` |
| `get-session-id` | Hook `conversation_id` |
| `get-session-dir` | Repo-scoped OS temp sidecar directory |
| `resolve-session-file` | Sanitized `<conversation-id>.jsonl` |
| `read-session` / `write-session` | Sidecar JSONL bytes (opaque round-trip safe) |
| `read-transcript` | Raw sidecar bytes |
| `chunk-transcript` / `reassemble-transcript` | Size-bounded raw byte chunks |
| `format-resume-command` | `cursor-agent --resume <quoted-id>` or `--continue` |
| `parse-hook` | Lifecycle table above |
| `install-hooks` | Read-modify-write `.cursor/hooks.json`, preserving foreign entries |
| `uninstall-hooks` | Remove only Entire hook entries |
| `are-hooks-installed` | All four Entire hook commands present |
| `get-transcript-position` | Sidecar record count |
| `extract-modified-files` / `extract-prompts` / `extract-summary` | Sidecar analysis |
| `compact-transcript` | Entire v1 compact JSONL |

## Declared Capabilities

| Capability | Value | Basis |
|---|---:|---|
| `hooks` | true | Project `.cursor/hooks.json` lifecycle events |
| `transcript_analyzer` | true | Sidecar contains prompts, edits, and assistant text |
| `compact_transcript` | true | Sidecar records map to Entire compact JSONL |
| `uses_terminal` | true | `cursor-agent` supports print and interactive modes |
| `transcript_preparer` | false | Sidecar is written by the hooks themselves |
| `token_calculator` | false | Cursor hook payloads carry no reliable token totals |

## Databricks Checkpoint Export (native)

On TurnEnd and SessionEnd the adapter can mirror normalized Entire session
context into Databricks. It is opt-in and stays disabled until all of
`ENTIRE_DATABRICKS_HOST`, `ENTIRE_DATABRICKS_TOKEN`,
`ENTIRE_DATABRICKS_WAREHOUSE_ID`, `ENTIRE_DATABRICKS_CATALOG`, and
`ENTIRE_DATABRICKS_SCHEMA` are set. See the README for details.

## Gaps & Limitations

- The exact set and payload shape of Cursor hook events is version-dependent and
  was not captured live here; mappings are structural and unit-tested.
- Modified files come only from `afterFileEdit` events; files changed by raw
  shell commands are not inferred.
- Token accounting is not implemented (`token_calculator: false`).
