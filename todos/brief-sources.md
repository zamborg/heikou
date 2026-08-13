# Configurable brief sources

Status: shipped. Configuration and command sources are implemented; a
model-written summary is deliberately out of scope because the command source
already makes it a user's own program rather than Heikou's problem.

## Where this starts

A session row's **brief** is the one-line cell between its state and its
runtime. It has a lead and a detail, each filled by the first `BriefSource` in
an ordered layout with something to say. The interface, the built-in sources
(title, initial task, latest via Heikou, runner), the separate truncation
budgets, and the provenance mark are all implemented; see the brief section of
`docs/DESIGN.md`.

The layout is now `config.BriefConfig` and command sources run behind an
observer. Nothing here is open.

## The idea

Let a user say what their brief should contain, the way Claude Code lets a user
own their status line. Three requests motivate this:

- **Just my title.** Drop the detail slot entirely; the row shows only the name
  the user gave the session.
- **The runner's own status line.** Whatever Codex or Claude reports about
  itself, rendered in the detail slot instead of the last message.
- **A written summary.** A small model reads the session's recent output and
  keeps a one-line description current.

These are the same feature: a source that produces text Heikou did not already
have. They differ only in where the text comes from and what it costs.

## What shipped

The first two requests are done. `brief.lead` and `brief.detail` are ordered
source-name lists, `brief.sources` defines argv commands, and an empty `detail`
asks for a title-only row. The README documents the block; the constraints below
are why it looks the way it does.

## Constraints that are not negotiable

**Sources are argv, never shell strings.** Claude Code's status line is a shell
command; Heikou has spent its whole design avoiding shell interpolation — argv
arrays in `commands`, `exec` into the runner, tmux buffers for follow-up text,
integration tests that push shell-looking input through and assert it stays
literal. A command source takes `["my-status", "--session"]`, not a string.

**Rendering never blocks.** `Source.Fragment` runs for every visible row on
every frame at a one-second refresh, so a subprocess source reads a cache there
and does its work in `brief.Observer`, which owns its own cadence. A source with
nothing cached falls through to the next one in the slot rather than rendering
blank, and a failing source drops what it last said rather than freezing it.

**Cost is per session per interval, not per session.** Twelve sessions on screen
is twelve subprocesses every time the cache refreshes. The implemented control
is two conditions rather than one: the interval must have elapsed *and* the
session must have shown terminal activity since the last observation. Content
hashing was the obvious idea and turned out to be unnecessary — the runtime
already reports an activity timestamp, and a session that has not moved cannot
have a different status line. Runs are also capped at four concurrent and
thirty-two per pass, and a capped pass reports its deferral.

**Untrusted output.** A command's stdout and a model's completion are both
untrusted text landing in a TUI. `brief.OneLine` strips ANSI, drops control
characters, collapses whitespace, and bounds the result; a bounded writer keeps
a command that never stops printing from growing memory. Note that a control
character which separates words has to become a space rather than vanish —
deleting a tab outright welds the words on either side of it together.

**Provenance is already enforced; keep it that way.** A model-written summary
sets `Proven: false` and renders with a leading `~`. Do not add a config flag
that turns the mark off. The whole argument for the mark is that the row is
otherwise indistinguishable from text the user typed.

## The third request is already answered

A model-written summary needs no work here, and that is the point of the command
source rather than a gap in it. A user who wants one writes a program that reads
`HEIKOU_SESSION_*`, calls whatever model they like, and prints a line. Heikou
runs it on a cadence, sanitizes the output, bounds it, and marks it as something
it was told rather than saw.

Building it in would have bought nothing and cost a great deal: the first
network call and the first API-key handling anywhere in the binary, a provider
choice baked into a terminal dashboard, and a second way to do what the generic
source already does. Today Heikou talks to tmux and the filesystem and nothing
else, which is why the install story is one `go install` with nothing to
configure before first run.

Heikou is the contract layer. It defines what a source is asked, what it may
return, how often it runs, and how its answer is marked. What a source does to
produce that line is the user's business.

## Suggested order

1. [x] Make the layout configurable with the built-in sources only.
2. [x] Add the cache-backed observer, proving fallback and that nothing blocks
   the render path.
3. [x] Add the argv command source behind that observer, with change detection,
   a cap, and a timeout.
4. [x] Resolved by not building it: the command source is the extension point,
   and a user's own program can call a model behind it.
