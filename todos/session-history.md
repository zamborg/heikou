# Session history

Status: proposed. Raised by a pilot during testing, which correctly reported
that `h peek` shows only the current terminal frame and not what a session did.

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
