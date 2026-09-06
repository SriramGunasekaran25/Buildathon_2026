# entire-agent-cursor

External agent binary that teaches the [Entire CLI](https://github.com/entireio/cli)
how to work with the **Cursor Agent CLI** (`cursor-agent`).

See [AGENT.md](AGENT.md) for the full protocol mapping, hook lifecycle, and the
sidecar transcript design.

## Capabilities

| Capability | Status |
|------------|--------|
| hooks | Yes — command hooks in `.cursor/hooks.json` for Cursor lifecycle events |
| transcript_analyzer | Yes — via a repo-scoped sidecar JSONL transcript |
| compact_transcript | Yes — emits Entire v1 compact transcript JSONL |
| uses_terminal | Yes — print and interactive `cursor-agent` sessions |
| transcript_preparer | No |
| token_calculator | No |

## How it works

- **Hooks** — `install-hooks` writes `.cursor/hooks.json` wiring Cursor's native
  `beforeSubmitPrompt`, `afterFileEdit`, `stop`, and `sessionEnd` events to
  `entire hooks cursor <verb>`. Existing (foreign) hook entries and unknown
  top-level keys are preserved across install/uninstall.
- **Sessions** — the `conversation_id` in each hook payload is the session id.
  Cursor has no single portable transcript file, so the hook commands append a
  normalized **sidecar** JSONL at
  `${TMPDIR}/entire-cursor/<repo-hash>/<conversation-id>.jsonl`, which Entire
  reads for prompts, modified files, and assistant responses.

## Build

```bash
cd agents/entire-agent-cursor
mise run build       # go build -o entire-agent-cursor ./cmd/entire-agent-cursor
mise run test        # go test ./...
```

## Install

Put `entire-agent-cursor` on your `PATH`, then opt in to external agents in the
repo's untracked `.entire/settings.local.json`:

```json
{
  "external_agents": true
}
```

Then enable it in a repository:

```bash
entire enable --agent cursor --telemetry=false
```

## Usage

```bash
cursor-agent -p "Create hello.txt with hello world"
git add hello.txt
git commit -m "add hello.txt"
entire checkpoint list
```

Sessions can be resumed with `cursor-agent --resume <conversation-id>` (an empty
id maps to `cursor-agent --continue`).

## Optional Databricks checkpoint export

Leave Databricks unset if you do not want to wire credentials yet. When all of
the variables below are set, this adapter mirrors normalized Entire session
context into Databricks on turn/session boundaries:

- `ENTIRE_DATABRICKS_HOST`
- `ENTIRE_DATABRICKS_TOKEN`
- `ENTIRE_DATABRICKS_WAREHOUSE_ID`
- `ENTIRE_DATABRICKS_CATALOG`
- `ENTIRE_DATABRICKS_SCHEMA`

Each captured lifecycle event then writes a row into the Delta table
`<catalog>.<schema>.entire_agent_checkpoints`, including the Entire repo path,
session id, prompt/summary, transcript location, branch/commit (when git is
available), and modified file list. Export is best-effort: failures are recorded
in the event metadata (`databricks_export`, `databricks_error`) and never block
a Cursor session.

## Development

```bash
mise run build
mise run test

cd ../../
E2E_AGENT=cursor mise run test:e2e:lifecycle   # requires the real cursor-agent CLI
```
