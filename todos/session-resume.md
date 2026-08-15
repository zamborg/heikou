# Session resume

Status: shipped. Sessions register their native runner conversation
automatically, and `h resume` continues it in a new session.

## The problem

A Heikou session outlives its tmux pane, but until now the *conversation* did
not. When the pane died, the Claude or Codex conversation was still on disk and
still resumable by its own CLI — and unreachable from Heikou, which had never
written the id down. The work could only be restarted cold.

## What the runners actually offer

Measured against the installed binaries, not assumed. Two separate capabilities
matter, and the runners differ on the first one:

| | choose the id at launch | resume by id | on disk |
| --- | --- | --- | --- |
| **Claude Code 2.1.229** | yes — `--session-id <uuid>` | yes — `-r/--resume <id>`, plus `--fork-session` | `~/.claude/projects/<slugged cwd>/<id>.jsonl` |
| **codex-cli 0.145.0-alpha.30** | **no** | yes — `codex resume <id> [prompt]` | `~/.codex/sessions/YYYY/MM/DD/rollout-<local ts>-<uuidv7>.jsonl` |

Verified argv, against the real binaries:

- `claude -p --resume <id> --name x -- "say ok"` reaches the conversation lookup
  and fails on a bogus id, so the flag combination parses. `--session-id` and
  `--resume` are mutually exclusive in meaning and are never both passed.
- `codex exec resume <id> -- "say ok"` parses the positional-then-`--` form and
  fails past argument parsing, on a runtime trust check.

For Codex, `--session-id` is rejected by the argument parser outright, there is
no `CODEX_SESSION_ID` environment variable, and session *names* exist but are
set from inside the TUI, not from argv. Codex genuinely cannot be told which id
to use.

## What shipped

State schema **v3** adds `SessionRecord.Conversation`: the id, a `source` of
`assigned` or `observed`, and when it was recorded. The v2-to-v3 migration is
schema-only and back-fills nothing, deliberately — see `docs/DESIGN.md`.

- **Claude** registers at launch, with no filesystem access at all: Heikou
  already passes `--session-id <durable id>`, so the conversation id *is* the
  session id. `assigned`.
- **Codex** registers on first use, by matching a rollout on launch directory,
  a start time inside the match window, and the **verbatim initial prompt**.
  Exactly one match or nothing. `observed`.
- `h conversation ID` reports the id and its source. `h resume ID MESSAGE`
  starts a new session continuing that conversation; the original record is
  untouched.

The registration is also what anything **reading** a runner's files has to ask
for. A resumed session's records are filed under the conversation it continued,
never under its own durable id, so `h history` and the brief's activity line
both locate a transcript through `control.Session.ConversationID`. Shipping the
registration and the activity line in the same release without that lookup is
how 0.7.2 left every resumed session permanently blank; see
[session-history.md](session-history.md).

The prompt is what makes the Codex match evidence rather than correlation.
Directory and time alone routinely describe several sessions, because running
many agents in one repository at once is the entire point of the tool.

## The honest failure mode

Codex registration can fail, and does so loudly rather than approximately:

- **no match** — the session never started, the rollout was deleted, or Codex
  has not written it yet;
- **ambiguous** — two launches agree on directory, window and prompt. Genuinely
  indistinguishable, so Heikou refuses.

Both leave the record unregistered and both exit zero from `h conversation`,
which is a state of the world rather than a fault. Nothing is ever recorded on a
partial match, and the nearest candidate is never chosen.

The window is generous on purpose. It is not what makes a match trustworthy —
directory and prompt are — and a cold start on a loaded machine is slow.

## Deliberately not

- **No back-fill on migration.** Possible for Claude, and wrong: a v2 record
  cannot tell a session that ran from one whose launch failed before Claude
  wrote anything, so it would register conversations that never existed.
- **No scan for Claude.** The resolver refuses runners that name their own
  conversation. Consulting the filesystem for something Heikou chose turns a
  certainty into an inference for no benefit.
- **No launch-time scan for Codex.** Codex has barely started when tmux returns,
  so a scan there would mostly find nothing and would either race or block the
  launch. Matching is anchored to the durable creation time, so asking later
  returns the same answer.
- **No reviving the old record.** Resume starts a new session. The old record is
  the durable account of what happened, including how it ended, and rewriting it
  to look alive would destroy the only copy of that.
- **No caller-supplied id.** The typed action carries neither an id nor a
  provenance, so no surface can assert an unverified conversation as fact.

## Next

- `h history` for Codex. The identification half is now done — see
  [session-history.md](session-history.md) — and only the rollout parser is
  left.
- A dashboard affordance for resume. The durable registration is the load-
  bearing part and shipped without it.
