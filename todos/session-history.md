# Session history

Status: shipped for Claude as `h history`; Codex remains unsupported. Raised by
a pilot during testing, which correctly reported that `h peek` shows only the
current terminal frame and not what a session did.

## What shipped

`internal/transcript` locates and parses the JSONL file Claude Code writes per
session, and `h history SESSION [--last N] [--json]` projects it into turns.
Every answer names its runner and reports `available`, `missing`, or
`unsupported`, so a caller can tell an authoritative record from an absent one.
A missing transcript exits zero.

Parsing decisions worth knowing, because each one is a way the naive version is
wrong:

- A tool's return value is stored under the *user* role. Trusting the role
  reports every tool result as something the user said.
- An assistant reply is stored as many appended records. A user message is the
  turn boundary; anything else reports one reply as thirty turns.
- The summary written when a session runs out of context is stored as a user
  message and is often the longest record in the file. It restates turns already
  present, so it is excluded.
- Local slash commands arrive inside a machine-written envelope, so `/compact`
  reads as a message saying `<command-name>/compact</command-name>` unless the
  envelope is recognized.
- Thinking blocks are dropped. They are most of the bytes and none of the answer.
- Subagent (sidechain) turns are excluded: they are not this session's
  conversation, and interleaving them reports an order that never happened.

The same file now also answers a second question. `Reader.ReadActivity` reads
the tail rather than the whole transcript and reports the one thing the session
was last recorded doing, which fills the brief's detail slot; see
[runner-activity.md](runner-activity.md).

Both readers ask for the file by the **conversation** id, which is the durable
session id only until a session is resumed. A resumed session is launched
`--resume <conversation id>` and gets a durable id of its own, so Claude goes on
appending to the original file and writes nothing under the new id. Asking by
the durable id there reports a session with a full history as having none — and
pays the fallback directory scan to find that out, every time. Callers get the
right id from `control.Session.ConversationID`, which prefers the registered
conversation and falls back to the durable id. Provenance does not gate it: an
`observed` id is precisely the one matched against a file on disk.

## Still open

Codex history. **The blocker named here has since been removed** — see
[session-resume.md](session-resume.md). This document said Codex history needed
"Codex to accept an externally supplied session id, or to report the id it
chose". Codex reports the id it chose, in the `session_meta` record of its
rollout, and Heikou now identifies and registers that id per session on a unique
launch-directory / time-window / verbatim-prompt match.

So the identification problem is solved and only the parsing is left. Making
`h history` work for Codex now means reading the rollout's `response_item`
records — a different record vocabulary from Claude's, with its own ways to be
wrong — and locating the file from the registered conversation id instead of
scanning. Until someone does that, `h history` still says `unsupported` for
Codex, and the reason it gives is now out of date rather than wrong in kind.

## The problem

`h peek` is honest but thin. A pilot asked "what happened in the OAuth session?"
can answer with durable facts and one screenshot of the pane, and nothing else.
That is the single most common question about a session, and Heikou currently
cannot answer it.

The reason is measured and structural, not a missing feature flag. A
full-screen runner draws on the terminal's alternate screen, which keeps no
scrollback. Against a live `claude` pane, `capture-pane -p -J -S -120` returned
85 lines, 60 of which were shell output produced *before* the runner started;
the runner contributed only its visible 24-row frame. Anything that scrolled
past is gone. No capture flag recovers it, and no pane width helps.

So history cannot come from tmux. It has to come from the runner.

## The source that already exists

Heikou launches Claude as `claude --session-id <heikou-uuid>`, and Claude Code
writes a JSONL transcript per session under `~/.claude/projects/`, in a
directory named for the session's root with `/` and `.` replaced by `-`.

Both halves of that are already true, unplanned: Heikou owns the id, and
records `InitialRoot`. So a Claude session's transcript is locatable today from
data already in `state.json`.

Codex needs its own investigation. Do not assume symmetry.

## Shape

A read-only observer, not a new runtime concern.

- `h history SESSION [--json] [--last N]` returns turns, not raw terminal text.
- It is **optional and fails soft**. A missing transcript is a normal answer —
  "no transcript for this session" — not an error, because the file layout
  belongs to Claude and may change.
- It never merges with `h peek`. Peek is the current frame; history is what
  happened. Conflating them is exactly the mistake the pilot instructions warn
  against.
- The projection must say which runner supplied it, so a caller can tell an
  authoritative transcript from an absent one.

## Why this is worth more than it looks

This is the first authoritative, structured runner signal Heikou would have.
[session-status-titles.md](session-status-titles.md) blocks semantic agent
status on exactly that: step 3 is "probe authoritative Claude and Codex
signals." A transcript reader is that probe, arrived at from a different
direction.

It does not by itself deliver turn state — a transcript says what happened, not
whether the agent is waiting right now. But it is the first real evidence that a
runner-specific observer can be built without terminal scraping, and it makes
the honest-status work concrete rather than theoretical.

## Deliberately not

- Do not scrape the terminal to reconstruct history. That is the heuristic this
  codebase has consistently refused.
- Do not persist a copy of the transcript into Heikou state. It is the runner's
  data; read it where it lives.
- Do not present a transcript as proof a session is healthy, finished, or idle.
  It is a record of the past, and the runtime state enum remains the only claim
  Heikou makes about now.

## First step

Confirm the Claude transcript format is stable enough to parse: turn boundaries,
message roles, and whether tool calls are distinguishable. Then decide whether
Codex has an equivalent before designing a shared projection, rather than
generalizing from one runner.
