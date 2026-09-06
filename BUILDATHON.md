# Entire Cursor Adapter + Databricks Checkpoint Lake

> BTW Buildathon 2026 submission — Track 3: Bring Entire to a New Agent or Workflow.

## One-sentence summary

We bring Entire to the **Cursor Agent CLI** with a full external-agent adapter, and make agent
session context durable and queryable by mirroring every captured Entire checkpoint event into a
**Databricks Delta table** that a team can query long after the local session is gone.

## Problem, intended user and why it matters

**User:** a developer (or a team lead reviewing that developer's work) who drives changes through a
terminal coding agent.

**Problem:** agent-assisted development produces the most valuable context — the prompt that caused
a change, what the agent tried, what failed, which files it touched — in places that do not survive:
a Cursor conversation buffer, a local temp file, a machine that goes to sleep. Git keeps the *what*;
the *why* evaporates. Two concrete gaps:

1. **Cursor is not covered.** Entire supports several external agents, but a developer working in
   `cursor-agent` gets no checkpoints at all, so their reasoning is never captured in the first place.
2. **Captured context stays local.** Even when an agent *is* supported, session context lives on one
   machine. A tech lead cannot ask "which agent sessions touched the payments module last week, and
   what were they trying to do?" without shoulder-surfing each developer.

**Why it matters:** the answer to "why is this code like this?" is currently lost within hours. We
make it survive the session (Entire checkpoints) *and* survive the laptop (Databricks), without
adding a step the developer has to remember.

## Selected Entire track and why Entire is essential

**Track 3 — Bring Entire to a New Agent or Workflow.**

Entire is not a logger bolted onto the side here; it is the mechanism:

- The product is an implementation of the **Entire external-agent protocol**. Every capability
  (session identity, transcript reading, prompt/modified-file extraction, compact transcript,
  resume-command formatting, hook install/uninstall) is an Entire contract our binary satisfies.
  Remove Entire and there is no product — only a JSON file that nothing consumes.
- Entire's **hook lifecycle** is what turns raw Cursor events into normalized
  SessionStart / TurnStart / TurnEnd / SessionEnd events. That normalized event is exactly what
  becomes a checkpoint *and* exactly what we mirror to Databricks — one capture path, two durable
  destinations.
- Entire's **checkpoints** supply the semantic payload (intent, prompt, summary, modified files)
  that makes the Databricks table worth querying. A row keyed only on a git SHA would be a worse
  `git log`; a row carrying the prompt and the agent's own summary is a different artifact.

The submitted implementation lives entirely in the designated Entire fork; we do not merely call an
Entire command from another interface.

## Architecture and main workflow

```
cursor-agent
   │  native lifecycle events (project-scoped .cursor/hooks.json)
   ▼
entire hooks cursor <verb>
   │
   ▼
entire-agent-cursor  (this repo, Go, Entire external-agent protocol)
   ├── append normalized sidecar JSONL   → ${TMPDIR}/entire-cursor/<sha256(repo)[:16]>/<conv-id>.jsonl
   ├── emit protocol EventJSON           → Entire  → Checkpoint (local, durable, resumable)
   └── best-effort export                → Databricks SQL Statement Execution API
                                              → workspace.entire_hackathon.entire_agent_checkpoints
```

### Components

| Path | Role |
|---|---|
| [`agents/entire-agent-cursor/cmd/entire-agent-cursor/main.go`](agents/entire-agent-cursor/cmd/entire-agent-cursor/main.go) | Subcommand dispatch for the 20 protocol verbs |
| [`agents/entire-agent-cursor/internal/cursor/hooks.go`](agents/entire-agent-cursor/internal/cursor/hooks.go) | `.cursor/hooks.json` install/uninstall (preserves foreign entries) and hook→event mapping |
| [`agents/entire-agent-cursor/internal/cursor/transcript.go`](agents/entire-agent-cursor/internal/cursor/transcript.go) | Sidecar JSONL reader: prompts, modified files, summary, positions |
| [`agents/entire-agent-cursor/internal/cursor/compact.go`](agents/entire-agent-cursor/internal/cursor/compact.go) | Entire v1 compact transcript emission |
| [`agents/entire-agent-cursor/internal/cursor/databricks.go`](agents/entire-agent-cursor/internal/cursor/databricks.go) | Checkpoint → Delta table exporter (opt-in, best-effort) |
| [`agents/entire-agent-goose/internal/goose/databricks.go`](agents/entire-agent-goose/internal/goose/databricks.go) | The same exporter wired into the existing Goose adapter |
| [`agents/entire-agent-cursor/AGENT.md`](agents/entire-agent-cursor/AGENT.md) | Protocol mapping one-pager, including explicitly marked `(unverified)` items |
| `demo-taskstore` (separate local repo, not part of this fork) | Small Go repo used as the demo target so checkpoints are produced by real edits |

### Key design decisions

- **Sidecar transcript, not a scraped Cursor file.** Cursor exposes no single portable transcript
  file, so the hook commands *write* the transcript themselves as append-only JSONL. Positions are
  record indexes rather than byte offsets, because every hook appends whole records.
- **Repo-scoped sidecar directory** (`sha256(repo-root)[:16]` under the OS temp dir). Rejected the
  flat `${TMPDIR}/entire-cursor/` layout: Entire attributes sessions by first match, so a shared
  directory risks another agent claiming a Cursor session.
- **First `beforeSubmitPrompt` per `conversation_id` = SessionStart**, later prompts = TurnStart,
  tracked by a `<session-id>.started` marker that `sessionEnd` clears. Cursor fires no dedicated
  session-start hook, so this is inferred rather than observed — and is documented as such.
- **Export is opt-in and never blocks.** The exporter stays disabled until all five
  `ENTIRE_DATABRICKS_*` variables are set. A config, build or network failure is written into the
  event metadata (`databricks_export`, `databricks_error`, truncated to 240 chars) and the hook still
  returns success. A telemetry sink must never be able to break someone's coding session.
- **Credentials only from the environment.** No token is read from, or written to, the repository.

## Entire Graph findings and verification

Graph activation used in the build:

```bash
entire plugin install graph
entire graph version
entire graph init-agents --repo .
```

**Finding 1 — definition lookup: what is the real protocol surface?**
Searched the graph for the external-agent protocol handlers and the existing `entire-agent-goose`
implementation to enumerate the verbs a new adapter must satisfy.
*Verified against source:* the dispatch table in
[`cmd/entire-agent-cursor/main.go`](agents/entire-agent-cursor/cmd/entire-agent-cursor/main.go)
implements every verb the goose module implements, and the shared protocol-compliance workflow
(`.github/workflows/protocol-compliance.yml`) is the executable check of that claim.

**Finding 2 — impact analysis before a high-risk change.**
The riskiest edit was touching `entire-agent-goose`'s hook path to add the export call: goose is an
already-working adapter and a regression there breaks existing users. Relationship analysis on
`ParseHook` showed the blast radius was confined to the hook-event construction path — the exporter
is called only after the `EventJSON` is fully built, so it can add metadata but cannot alter event
type, session id or transcript handling.
*Verified against tests:* the [`internal/goose/hooks.go`](agents/entire-agent-goose/internal/goose/hooks.go)
change is +16/−8 lines confined to that path (plus +2/−1 in `agent.go` for the injectable exporter
field), and the pre-existing goose hook and transcript tests still cover the untouched behavior.

**Finding 3 — semantic diff of the submitted implementation.**
The submitted change is additive: one new module (`entire-agent-cursor`) plus a new, independently
constructed exporter file in the goose module. No shared upstream package signature was modified, so
no other agent module's behavior changes.

**Honesty note (please read before judging this section):** graph output is evidence, not an oracle.
The findings above were each re-checked by reading source and tests, and the claims we could not
verify live in this environment — chiefly the exact set and payload shape of Cursor's hook events —
are marked `(unverified)` in [`AGENT.md`](agents/entire-agent-cursor/AGENT.md) rather than presented
as fact. Raw `entire graph` transcripts for the three lookups above should be attached to the
submission alongside this file.

## Noon Curveball: what changed and how we adapted

**The constraint received.** The Track 3 card (`track-3-agent-session`) delivered a JSONL sample of a
*foreign* agent's session — a fictional `AcmeCode 1.4.2` working on `github.com/example/checkout-service`.
Its event vocabulary is not ours: `session_started`, `user_prompt`, `agent_response`, `tool_call`,
`tool_result`, `file_read`, `file_changed`, `usage`, `checkpoint_created`, `session_ended`, with
`message_id`/`parent_message_id` threading, per-file `lines_added`/`lines_removed`, and a
`checkpoint_created` record carrying `intent` and `open_questions`. The required procedure was also
explicit: commit the last stable version, confirm the checkpoint with `entire status`, end the agent
session, read the card, start a **fresh** session, reconstruct intent from the pre-noon checkpoint,
run Entire Graph impact analysis **before** editing, implement and test, then checkpoint the revised
design.

**What it really asks.** Our pre-noon design assumed the adapter both *writes* and *reads* its own
sidecar format — a closed loop we control. The card breaks that assumption: the adapter must be able
to ingest a session transcript produced by an agent we did not write, in a format we do not control.
That is an architectural change, not a feature: transcript analysis has to move from "parse our
records" to "normalize an external vocabulary into Entire events."

**Status — implemented: NO.** As of the final commit, no code in this repository parses the
`track-3-agent-session` vocabulary (no reference to `session_started`, `file_changed` or
`checkpoint_created` exists in the tree). We are recording this plainly rather than implying a
response we did not ship.

**The adapted design we did produce (planned, not merged).** The pre-noon architecture already
anticipates the seam, which is why the response is small:

1. Introduce a `sidecarRecord` *source* interface in `internal/cursor/transcript.go`, so the four
   analyzers (`extract-prompts`, `extract-modified-files`, `extract-summary`,
   `get-transcript-position`) read from a normalized record stream instead of directly from our own
   JSONL shape.
2. Add a foreign-format decoder that maps the card's vocabulary onto that stream:
   `user_prompt.text` → prompt; `file_changed.path` → modified file (de-duplicated, `change`
   preserved as metadata); `agent_response.text` → summary candidate; `session_started`/
   `session_ended` → SessionStart/SessionEnd; `usage` → token totals, which would let us finally flip
   `token_calculator` to `true`; `checkpoint_created.intent` and `.open_questions` → checkpoint
   context, the single richest field in the sample.
3. Detect the format by sniffing the first record's `event` key, so the existing sidecar path is
   untouched and cannot regress.
4. Test with the supplied `track-3-agent-session.jsonl` as a golden fixture: assert the extracted
   prompt, the two modified files, the 8-passed final `tool_result`, and that the `open_questions`
   survive into checkpoint context.

**Known risk in that plan:** `file_read` must *not* become a modified file, and the failed-then-fixed
`tool_result` pair means "latest wins" ordering has to be deliberate — the sample was authored so
that a naive parser reports a failing test run as the session outcome.

## Checkpoint links and what each checkpoint proves

Retrieve the shareable links with `entire checkpoint list` in the Entire clone and paste them here
before submitting.

| Milestone | What it proves | Link |
|---|---|---|
| Initial understanding and intended architecture | The Cursor hook lifecycle mapping and the sidecar-transcript decision, including the rejected flat-temp-dir layout | _paste_ |
| Last stable state before the Noon Curveball | The adapter builds, unit tests pass, Databricks export is opt-in and green | _paste_ |
| Response to the Noon Curveball | The reconstruction of intent in a fresh session and the foreign-format ingestion design above | _paste_ |
| Final implementation and verification | The submitted commit, test evidence, and the open risks named honestly | _paste_ |

- **Entire mirror:** `entire://aws-ap-south-1.entire.io/gh/sriramgunasekaran25/buildathon_2026`
- **GitHub fork (submission):** https://github.com/SriramGunasekaran25/Buildathon_2026
- **Upstream:** https://github.com/entireio/external-agents
- **Final commit SHA:** tip of `main` on the submission remote — the value entered in the submission
  form is the output of `git rev-parse HEAD` for the pushed commit that contains this file.

## Setup, run and test instructions

### Build and unit-test the adapters

```bash
cd agents/entire-agent-cursor
mise run build          # go build -o entire-agent-cursor ./cmd/entire-agent-cursor
mise run test           # 26 unit tests across agent/hooks/transcript/databricks

cd ../entire-agent-goose
mise run test           # includes 5 tests covering the Databricks exporter
```

### Enable the Cursor adapter in a repository

Put `entire-agent-cursor` on your `PATH`, opt in to external agents in the repo's untracked
`.entire/settings.local.json`:

```json
{ "external_agents": true }
```

Then:

```bash
entire enable --agent cursor --telemetry=false
entire status
```

### Demonstrate the critical path

Our demo target is `demo-taskstore`, a throwaway Go repo kept outside this fork; any small
Entire-enabled repository works the same way.

```bash
cd <any-entire-clone>            # e.g. demo-taskstore
cursor-agent -p "Add a Complete method to TaskStore and cover it with a test"
go test ./...
git add -A && git commit -m "feat: complete tasks"
entire checkpoint list          # the checkpoint carries the prompt, not just the diff
```

### Enable the Databricks export (optional)

```bash
export ENTIRE_DATABRICKS_HOST=https://<workspace-host>
export ENTIRE_DATABRICKS_TOKEN=<pat>            # never commit this
export ENTIRE_DATABRICKS_WAREHOUSE_ID=<warehouse-id>
export ENTIRE_DATABRICKS_CATALOG=workspace
export ENTIRE_DATABRICKS_SCHEMA=entire_hackathon
```

All five must be set; setting only some is treated as a configuration error and reported in event
metadata rather than silently ignored. Verify with:

```sql
SELECT captured_at, event_name, branch, head_commit, prompt, modified_files_json
FROM workspace.entire_hackathon.entire_agent_checkpoints
ORDER BY captured_at DESC
LIMIT 20;
```

### End-to-end (requires the real `cursor-agent` CLI)

```bash
E2E_AGENT=cursor mise run test:e2e:lifecycle
```

## Databricks use, data sources and limitations

**Opted in to Best Use of Databricks: yes.**

**Capabilities used and why they are essential.** The adapter writes through the **SQL Statement
Execution API** (`POST /api/2.0/sql/statements`) against a **serverless SQL warehouse**, into a
**Unity Catalog Delta table** `workspace.entire_hackathon.entire_agent_checkpoints`. Databricks is
what makes the product's second claim true: without it, checkpoint context is per-machine and
per-session, and "which sessions touched this module, and what were they trying to do?" is not a
question anyone can ask. With it, agent context becomes a queryable table with the same governance as
the rest of the lakehouse. The table is created on demand (`CREATE TABLE IF NOT EXISTS`) so a fresh
workspace needs no manual setup.

**Schema** (see [`databricks.go`](agents/entire-agent-cursor/internal/cursor/databricks.go)):
`session_id`, `repo_path`, `repository_system`, `agent_name`, `event_name`, `event_type`, `prompt`,
`summary`, `session_ref`, `model`, `branch`, `head_commit`, `transcript_available`,
`modified_files_json`, `metadata_json`, `captured_at`.

**Data sources and provenance.** Every row is derived from one Entire lifecycle event on the
developer's own machine: the Cursor hook payload (`conversation_id`, `prompt`, `model`), the sidecar
transcript we wrote, and `git rev-parse` for branch and HEAD. `branch`/`head_commit` are empty
strings when git is unavailable rather than guessed. The demo rows come from `demo-taskstore`, a
throwaway repo we wrote for this event — no customer, employer or third-party code is involved.

**Responsible use and safe handling.**
- Credentials come only from environment variables; no token is committed, logged, or written into
  checkpoint context or this file.
- Catalog and schema names are validated as identifiers before interpolation, and string values are
  escaped as SQL literals — the prompt text is attacker-influenced input and is treated as such.
- Prompts and assistant summaries are stored verbatim. **Teams should treat this table as sensitive**
  and govern it accordingly: a prompt can contain whatever the developer typed.

**Free Edition limitations we designed around.** One serverless 2X-Small warehouse and a
quota-limited account: the exporter uses a single short statement per event with a 15-second client
timeout, does no polling loop, and degrades to a metadata note when the warehouse is asleep or quota
is exhausted, so a cold warehouse cannot stall a coding session. If the live warehouse is unavailable
during judging, the prepared table screenshot is the fallback evidence.

## Known limitations and next steps

1. **The Curveball response is designed but not implemented** — see that section. This is the single
   biggest gap in the submission and we are not dressing it up.
2. **Cursor hook payloads were not captured live.** Event names and field shapes follow Cursor's
   documented hooks interface; unverified items are marked `(unverified)` in `AGENT.md` and are held
   in place by unit tests and the protocol-compliance suite, not by observation. First next step:
   run the real CLI and replace every `(unverified)` marker with a captured payload.
3. **Modified files come only from `afterFileEdit`.** Files changed by a raw shell command inside the
   agent are invisible to us. A `git status` diff at TurnEnd would close this.
4. **No token accounting** (`token_calculator: false`) — Cursor hook payloads carry no reliable
   totals. The Curveball format's `usage` record is the natural source once ingestion lands.
5. **Export is fire-and-forget per event.** There is no local queue, so events produced while offline
   are lost from Databricks (never from Entire). Next step: spool failed exports to the sidecar
   directory and flush on the next successful call.
6. **`repo_path` is a local filesystem path**, which is useful for a single developer and wrong for a
   fleet. It should be normalized to the remote URL before this is used across a team.
7. **The sidecar is never garbage-collected.** Long-lived machines accumulate JSONL under the temp
   directory; it needs a retention policy.

### Credibility of the changed behavior

What we can defend: the adapter's protocol surface, the hook install/uninstall round-trip that
preserves foreign entries, the sidecar analyzers, and the exporter's failure handling are covered by
31 unit tests and are deterministic. What we cannot yet defend: that Cursor emits exactly the events
we mapped, and any claim about the Curveball format at runtime. Those two are the top of the backlog,
in that order.
