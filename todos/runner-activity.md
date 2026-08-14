# What a runner exposes about what it is doing

Status: the transcript reader shipped as the built-in `activity` brief source.
The stronger signal — Claude Code's own per-process status file — is
investigated, documented below, and deliberately not built, because it belongs
to the status column rather than to the brief.

## Where this starts

[brief-sources.md](brief-sources.md) shipped the *mechanism*: two ordered slots,
a cache-backed observer, a generic argv command source, a provenance mark. What
it did not ship was a source that observes the agent. Title, initial task,
latest-via-Heikou and runner name are four ways of restating what the user
already told Heikou. A row could say what you asked for and never what happened
next.

So the question this document answers is narrow and factual: **what does a live
Claude Code or Codex session actually expose on this machine, without a network
call, that could drive a status line?** Everything below was measured against
real files under `~/.claude` and `~/.codex` in August 2026, against Claude Code
2.1.229 and Codex 0.145.0.

## Claude Code

### 1. The transcript — `~/.claude/projects/<slug>/<session id>.jsonl`

This is what shipped. `internal/transcript` already located and parsed it for
`h history`, and Heikou owns the session id because it launches
`claude --session-id <id>`, so the file is identified exactly rather than
guessed at.

The records are richer than `h history` needed. Across 40 real transcripts the
types present are `assistant`, `user`, `attachment`, `system`, `mode`,
`ai-title`, `custom-title`, `last-prompt`, `queue-operation`, `pr-link`,
`agent-name`, `relocated`, `worktree-state`, `file-history-snapshot`,
`permission-mode`, `bridge-session`, `frame-link`. What matters for a status
line:

- An `assistant` record carries `message.stop_reason`. Observed values are
  `tool_use` (6013), `end_turn` (227), `stop_sequence` (24). **`tool_use` is the
  only value that promises more records**, so anything else is a finished reply.
  That is the turn boundary, stated by the runner rather than inferred.
- A `tool_use` content block carries the tool `name` and its full `input`. `Bash`
  inputs carry both `command` and a model-written one-line `description`.
  `Edit`/`Read`/`Write` carry `file_path`, `Grep`/`Glob` carry `pattern`,
  `Task` carries `description`. This is what makes `editing observer.go` and
  `running make check` possible at all.
- A tool's return value is stored under the **user** role as a `tool_result`
  block — the trap `h history` already documents.
- `isSidechain` marks a subagent's records, which are not this session's work.
- Every record carries a `timestamp`, and `system` records include
  `subtype: "turn_duration"` with `durationMs` and `messageCount` on some
  versions. It appeared in 17 records across 40 files, so it is real but not
  dependable enough to be a primary signal.
- `ai-title` and `custom-title` records carry Claude Code's *own* one-line name
  for the session — "Verify builds after merging PRs to main". Available and
  cheap; deliberately unused, because Heikou already has a title slot the user
  owns and a second automatic title in the same cell would be two answers to one
  question.

What the transcript cannot say: whether a tool call is executing or sitting
behind an approval prompt. Both are the same record — a `tool_use` with no
result yet. That is why the shipped source reports `running` and never `waiting
on you`.

### 2. The per-process session file — `~/.claude/sessions/<pid>.json`

**This is the strongest signal on the machine, and it is not the one that
shipped.** Claude Code writes one JSON file per running process:

```json
{
  "pid": 30824,
  "sessionId": "bb305bb2-5fd7-4f7e-9a44-f48512b7db40",
  "cwd": "/",
  "kind": "interactive",
  "entrypoint": "cli",
  "version": "2.1.228",
  "tmux": "h-bb305bb2-5fd7-4f7e-9a44-f48512b7db40:@16.%16",
  "name": "Say hi!",
  "status": "idle",
  "statusUpdatedAt": 1786722574907
}
```

Three things make it remarkable:

- It is keyed by **`sessionId`** — the id Heikou minted. No correlation problem.
- `status` is one of `busy`, `shell`, `idle`, `waiting`, and it is written on
  every transition rather than on a timer. A sibling field `waitingFor` carries
  a short phrase for why: `input needed`, `sandbox request`, `dialog open`,
  `worker request`.
- It already knows the tmux pane. The `tmux` field above is a *Heikou* session
  name, unprompted.

`claude agents --json` is the same data through a supported interface: one
process, 270 ms, every interactive and background session as a JSON array with
`sessionId`, `pid`, `cwd`, `kind`, `startedAt`, `name`, and `status`. That is one
subprocess for the whole dashboard rather than one per row.

`waiting` + `waitingFor` is exactly the `needs_input` signal that
[session-status-titles.md](session-status-titles.md) has deferred since V0.3.4,
and it is what its step 3 — "probe authoritative Claude and Codex signals" —
was asking for.

### 3. Claude Code's own status line

Claude Code has a user-configurable status line: `settings.json` takes
`"statusLine": {"type": "command", "command": "..."}`, and Claude invokes that
command with a JSON payload describing the session, then draws the output
**inside its own alternate-screen UI**.

Heikou cannot cooperate with it, in either direction:

- It cannot *read* it. The rendered line lives in the pane's alternate screen,
  which keeps no scrollback — the same measured limitation that made
  `h history` necessary. Capturing the pane returns the current frame, and the
  status line is one row of it, in whatever layout the user's program chose.
- It should not *write* it. Heikou would have to edit the user's
  `~/.claude/settings.json` to install a shim, which is another program's
  configuration file. Heikou does not write outside `~/.heikou`.

There is a third path that costs Heikou nothing: a user who already has a status
line command can have it *also* append a line to a file, and point a
`brief.sources` command at that file. That works today with no code. It is worth
mentioning in documentation if anyone asks; it is not worth a feature.

## Codex

Codex writes `~/.codex/sessions/<yyyy>/<mm>/<dd>/rollout-<timestamp>-<uuid>.jsonl`,
and the content is **at least as good as Claude's**:

- `session_meta` with `session_id`, `cwd`, `originator`, `cli_version`.
- `event_msg` records with typed payloads: `task_started` and `task_complete`
  (explicit turn boundaries, with `turn_id`, `started_at`, `duration_ms`,
  `time_to_first_token_ms`, and `last_agent_message`), `user_message`,
  `agent_message`, `token_count`, `patch_apply_begin`/`patch_apply_end`.
- `response_item` records with `custom_tool_call`, whose `input` embeds the real
  command: `tools.exec_command({"cmd":"h list --json", ...})`.

A Codex status line would be *better* than the Claude one — `task_complete`
carries the finished reply directly, and there is no ambiguity about turn
boundaries at all.

**It is blocked on identity, exactly as `h history` reported.** `codex --help`
on 0.145.0 has no `--session-id`; the TUI mints its own id and never tells the
launcher. Nothing correlates it back:

- `~/.codex/session_index.jsonl` holds `{id, thread_name, updated_at}` and no
  `cwd` or `pid`.
- `~/.codex/process_manager/chat_processes.json` is the desktop app's exec
  registry, not the CLI's.
- There is no `~/.claude/sessions` equivalent.

Matching by `session_meta.cwd` plus start time is a guess, and Heikou routinely
launches several sessions in the same root, so the collision is the normal case
rather than the edge case. A guess that attributes one session's activity to
another is worse than no line.

One correlation route is *not* a guess and is worth recording: Heikou owns the
tmux pane and therefore knows `pane_pid`, and the codex process holds its rollout
file open, so `lsof -p <pid>` would name the file exactly. **This is untested** —
there was no live Codex session on the machine during this investigation — and it
costs a subprocess per session on a platform-specific tool. It is the cheapest
path to Codex parity if anyone wants to fund it.

## What shipped, and why that one

The **transcript tail**, as the built-in `activity` source.

- It reuses `internal/transcript`, which already owns locating and parsing this
  file. No second undocumented layout entered the codebase, and there stays one
  answer to where a runner's records live.
- It produces text worth a whole cell. `running make check` and
  `editing observer.go` are a status line; `busy` is a state word, and the row
  already has a column for those.
- It is filesystem-only, which keeps the install story intact: one `go install`,
  no network, no API key, nothing to configure before first run.
- It fits the existing machinery exactly — observer cadence, activity gate,
  concurrency cap, cache-reading source, `~` mark — rather than needing a new
  one.

Cost, stated plainly: a `pread` of the last 128 KiB of one file, at most once
every five seconds per session, and only for a live Claude session that has
shown terminal activity since the last look. It is in the default `brief.detail`
layout, so it is on by default; removing `activity` from that list turns it off
completely.

## Rejected, and what would unblock each

**The per-process session file / `claude agents --json`.** Rejected *for the
brief*, not on the merits. Its output is a state word, and the brief is the cell
for content; putting `busy` there would spend the row's one variable cell on
something the status column should own. What would make it viable is the work it
actually belongs to: [session-status-titles.md](session-status-titles.md) steps
4 and 5, where `busy`/`idle`/`waiting` becomes the agent turn state and
`waitingFor` becomes `needs_input`. That work needs freshness rules
(`statusUpdatedAt` is event-driven, so a crashed process leaves a stale file next
to a live pid), a decision about `claude agents --json` versus reading the
directory, and a plan for the file layout changing under a Claude Code upgrade.
It is the single highest-value follow-up from this investigation.

**Claude Code's own status line.** Rejected because it is unreadable from
outside the alternate screen and installing a shim means writing another
program's settings file. What would unblock it is Claude Code exporting the
rendered line somewhere — which, given the per-process file above already exists,
would be a strictly worse version of a signal Heikou can already read.

**`ai-title`.** Rejected because Heikou already has a title slot the user owns.
What would make it interesting is a distinct question to answer with it — a
"what is this session about" that is explicitly not the user's title — and no
such slot exists.

**Reading the pane.** Rejected again, for the third time in this repo. A
full-screen runner draws on the alternate screen; `capture-pane` returns the
current frame and nothing that scrolled past. It is also the heuristic this
codebase has consistently refused: a spinner glyph is not evidence. One
exception worth knowing about: `codex --no-alt-screen` runs the TUI inline and
preserves scrollback, and Heikou owns the runner argv, so a user could set
`commands.codex` to include it. That makes the pane readable — but it makes it
readable as *terminal text to be scraped*, which is the thing to avoid, not the
thing to enable.

**A model-written summary.** Already answered by
[brief-sources.md](brief-sources.md): the argv command source is the extension
point, and the first network call and API key in the binary is a price nothing
here justifies.

## What is deliberately not claimed

- **`waiting on you`.** The transcript cannot distinguish a command that is
  executing from one blocked on an approval prompt. The `waiting` status in the
  per-process file can, and that is where the claim belongs.
- **Anything in the status column.** The activity source fills a brief slot. The
  runtime lifecycle enum remains the only claim Heikou makes about now.
- **Any line for a session that is not alive.** The observation is dropped when
  a session stops being alive, because `running make check` beside `exited` is
  the same looks-current failure as freezing a failed source's text.
