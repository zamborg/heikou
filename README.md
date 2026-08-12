# heikou

Heikou is a fast terminal dashboard for parallel native coding agents. It starts
real Codex and Claude Code sessions inside a private tmux server, organizes them
into durable workstreams, lets you send follow-ups, and hands your terminal
directly to the native agent UI when you attach.

Tmux remains the runtime supervisor and the coding-agent CLIs remain the native
runners. Heikou adds a small durable organization layer without introducing a
daemon, manager agent, task graph, or replacement execution engine.

## Nouns

- **Workstream** — a durable named grouping for roots, sessions, notes, and
  artifacts; it provides organization, not autonomy.
- **Session** — a durable launch identity with an optional user title, initial
  task, root, runner, and outcome; it persists beyond its runtime.
- **Runtime** — the tmux pane associated with a session and the source of its
  current process observations.
- **Root** — an explicit launch working directory registered on a workstream.
- **Runner** — the `codex`, `claude`, or `no-agent` command integration used to
  launch a native agent or shell.
- **Composer** — the dashboard input bar used to start sessions and send
  follow-up messages. Its prefix names the destination `Enter` will commit to.
- **Ungrouped** — durable sessions with no active workstream membership.
- **Orphaned** — tmux panes carrying a Heikou ID unknown to durable state; they
  are never silently adopted.

## Install

Heikou currently targets macOS. Requirements: Go 1.25+, tmux 3.3+, and at
least one of `codex` or `claude`. Runner commands can be configured when they
are not on `PATH`; Heikou also discovers Codex inside the macOS ChatGPT app
bundle.

```sh
go install github.com/zamborg/heikou/cmd/h@v0.3.6
h doctor
```

Go does not run package-defined post-install hooks. After successful checks,
`h doctor` prints the next step: `h quickstart`.

Ensure `$(go env GOPATH)/bin` is on your `PATH`. To install the `heikou`, `h`,
and `H` aliases together, build from source instead:

```sh
git clone https://github.com/zamborg/heikou.git
cd heikou
make install
```

`make install` writes `heikou` to `~/.local/bin` and adds `h` / `H` symlinks.
Override the destination with `make install PREFIX=/somewhere`.

For an agent-guided first run, run:

```sh
h quickstart
```

This embeds the [`learn-heikou` skill](skills/learn-heikou/SKILL.md) in the
installed binary, prefers Claude when its configured executable is available,
falls back to Codex, starts a real durable Heikou session, and attaches to it
immediately. Use
`h quickstart -r codex` or `h quickstart -r claude` to choose explicitly.

The guide's first lesson is how to detach. Press `Ctrl-b`, release both keys,
then press `d`; `h quickstart` will open the dashboard with the guide selected
so it can walk you through sending a follow-up, reattaching, `F3`, workstreams,
and persistent notes.

## Use it

Run `h` (or `H`) from the directory that new agents should use as their root:

```sh
cd ~/code/my-project
h
```

The composer is always ready, and it picks its destination *before* you type
rather than when you commit. An empty composer starts a new session; `Space`
aims it at the selected live session and pins that target. Either way the
prefix names where the text is going and `Enter` sends it there, so the commit
key never depends on remembering which one you meant:

| Action | Result |
| --- | --- |
| Type a task or label, then `Enter` | Start the chosen Codex, Claude, or `no-agent` session |
| `Space` with an empty composer | Aim the composer at the selected live session; the prefix becomes `↳ reply …` |
| Type a message, then `Enter` | Send it to the pinned session; moving the selection first does not redirect it |
| `Esc` while replying | Clear the draft, then return to composing a new session |
| `Shift-Enter` | Insert a newline; `Ctrl-J` is the fallback for terminals that cannot distinguish shifted Enter |
| `Option-Left` / `Option-Right` | Move by word; `Option-Delete` deletes the previous word |
| `Command-Left` / `Command-Right` | Move to the start or end of the logical line |
| `Command-Up` / `Command-Down` | Move to the start or end of the whole draft |
| `Tab` | Cycle Codex → Claude → `no-agent`, with or without composer text |
| `Shift-Tab` | Cycle the selected workstream's explicit roots, with or without composer text |
| `F1`, or `?` with an empty composer | Open scrollable help, including the noun glossary and current composer keys |
| `Ctrl-S` or `F2` | Open settings; `e` edits JSON, `r` reloads, `Esc` returns |
| `F3` | Open the expandable workstream/session organizer |
| `r` in F3 | Rename a workstream, or edit/clear the selected durable session title |
| `R` in F3 | Refresh the selected workstream's notes and artifact preview after external changes |
| `Up` / `Down` | Select a workstream or session, or move between multiline composer rows |
| `Ctrl-G` | Enter resize mode; `Up` grows the snapshot, `Down` shows more sessions, `r` resets, and `Esc` exits |
| `Enter` on a workstream | Collapse or expand its sessions |
| `Enter` on a session | Attach its native terminal; inactive while replying, so it cannot attach to a row other than the pinned target |
| `Ctrl-\` or `Ctrl-b d` while attached | Detach back to Heikou |
| `Ctrl-X` twice | Stop/remove a present runtime; once no pane remains, press twice again to delete its durable record |
| `Esc` | Clear the composer, then leave a reply, then leave the dashboard |

The terminal application decides whether macOS modifier chords reach a TUI.
Heikou accepts enhanced Option/Command events plus common Alt, Home/End, and
Ctrl-key fallbacks. If a terminal reports `Shift-Enter` as ordinary Enter, use
`Ctrl-J` for a newline.

Every full-screen surface carries an unmistakable mode badge: **Dashboard**,
**Workstream Organizer**, **Settings**, or **Help**.

Session rows lead with the durable user title when one is set, otherwise a
one-line initial task. The most recent message successfully sent through
Heikou appears as secondary **latest via Heikou** detail for as long as the
tmux runtime is retained. Text entered directly in an attached native terminal
is not observable by this shim.

Leaving the dashboard never stops an agent. Exited and failed panes remain
inspectable while tmux retains them. Stopping removes the runtime but preserves
the durable session record and its workstream history; deletion is offered only
after no tmux pane remains. Deletion fails closed: it checks stable tmux identity
without relying on rich pane metadata, refuses records bound to another socket,
and retains an interrupted pending launch when its original socket is unknown.

The same primitives are available without the TUI:

```sh
h spawn -r claude -C ~/code/project "Investigate the flaky test"
h spawn -r codex -C ~/code/project -w "Core" "Implement the fix"
h spawn -r no-agent -C ~/code/project "scratch shell"
h list
h send a1b2c3 "Also check whether the retry hides the root cause"
h list --json
h spawn --json -r codex -C ~/code/project "Machine-readable launch"
h send --json a1b2c3 "Machine-readable delivery result"
h attach a1b2c3
h stop a1b2c3
```

Every organizing action the `F3` organizer performs is also a command, so the
whole durable model can be driven without the TUI:

```sh
h ws create "API work" -C ~/code/api -d "the public API"
h ws list --json
h ws root add "API work" ~/code/api-client
h ws rename "API work" "Public API"
h ws reorder "Public API" --up
h title a1b2c3 "OAuth retry investigation"
h move a1b2c3 --workstream "Public API"
h move a1b2c3 --ungrouped
h adopt a1b2c3 -w "Public API"
h peek a1b2c3
h ws archive "Public API" --yes
h delete a1b2c3 --yes
```

Workstreams and sessions accept a full id, an id prefix, or a workstream name;
an ambiguous prefix is an error rather than a guess. Flags may appear before or
after positional arguments. `h ws archive` and `h delete` require an explicit
`--yes`.

`h list --json` returns a machine-readable projection of workstreams and
sessions, including durable/display titles, latest-via-Heikou text, runtime
availability, a stable process-state enum, and an `exit_code` that is `null`
when tmux cannot prove the outcome. Every command above accepts `--json` and
returns a machine-readable result. These are local human CLI surfaces; they do
not enable manager authority.

## The pilot

Because that command surface is complete, an ordinary agent can maintain
Heikou's state. Heikou writes the instructions for one into `~/.heikou`:

```text
~/.heikou/
  AGENTS.md                       operating contract, read by Codex and Claude
  CLAUDE.md                       pointer to AGENTS.md
  skills/manage-heikou/SKILL.md   the full command reference
```

A new installation is also seeded with one workstream named `heikou-managers`,
rooted only at `~/.heikou`, so there is somewhere to launch pilots from the
dashboard without building it by hand.

It is seeded only on an installation that has never written durable state, and
the state file is what marks that: reads never create it and no-op mutations
never write it. So deleting or archiving the workstream keeps it deleted, and an
installation you have already organized is never seeded behind your back. `h init`
is the explicit way to create it, or to get it back.

Start a pilot by running an agent in that directory:

```sh
cd ~/.heikou && claude
```

Codex works the same way. The instructions are deliberately vendor-neutral: the
contract lives in `AGENTS.md`, and `CLAUDE.md` only points at it, so there is
one source of truth rather than two that can drift.

Then ask for what you want in words — "make a workstream for the API work and
move those three sessions into it", "register ~/code/api-client as a root",
"what's running right now?" — instead of remembering which key does it.

Those files are installed on first run and are **never overwritten**, so house
rules you add to `AGENTS.md` survive upgrades. `h init --force` refreshes them
from a newer binary.

The pilot is an ordinary agent with a shell, not a privileged one. It acts as
you, through the same CLI and the same command plane, and holds no grant of any
kind. Scope comes from what the instructions teach and which verbs require
`--yes`, which is a guardrail against mistakes rather than a security boundary.

## Workstreams

The main page is a workstream projection. Named workstreams appear first,
followed by two honest system groups:

- **Ungrouped** contains durable sessions with no membership and preserves the
  original raw-session workflow.
- **Orphaned tmux** contains panes carrying a Heikou ID that is unknown to the
  durable store. They remain attachable and steerable but are never silently
  adopted into a workstream; the organizer workflow below makes adoption
  explicit.

Press `F3` for an upper tree of named workstreams, Ungrouped, Orphaned, and
their sessions. On a workstream row, `Enter` expands/collapses it unless a move
source is active, in which case it moves or adopts that session there. On a
session row, `Enter` marks it as the move source; `m` also marks or completes a
move. Press `u` or `Space` to return to the dashboard with the highlighted
workstream or session selected. On a named workstream, `Shift-Up` and
`Shift-Down` move it in the durable display order; the synthetic Ungrouped and
Orphaned sections remain fixed after named workstreams.

The organizer's lower, read-only context pane follows the selected workstream;
selecting a session shows its parent workstream. It previews a bounded portion
of `notes.md` and a shallow tree of that workstream's artifact directory only.
It receives more space by default on taller terminals. Press `Ctrl-G` to enter
resize mode, then use `Up` to grow notes/files, `Down` to expose more sessions,
or `r` to restore automatic sizing. Dashboard snapshot sizing is adjusted the
same way and remembered independently for the current process.
Rendering context does not change domain state, inspect registered repository
roots, or modify files. Press `R` after an editor, agent, or other process
changes notes or artifacts to refresh that cached preview explicitly. The
organizer also creates or renames workstreams, edits or clears a durable session
title with contextual `r`, opens notes/artifacts, and archives. On a named
workstream, `p` adds a root, `Shift-P` edits the root selected with `Tab`, and
`d` twice removes that root without deleting files or changing historical
session records. Every workstream keeps at least one root.
Archiving keeps all durable sessions and moves their memberships to Ungrouped.

The composer always shows its exact workstream and launch root. A workstream may
contain sessions launched from several registered roots, but membership never
implicitly adds a root.

Workstream state is separate from settings. It remains a versioned, locked JSON
sidecar at `~/.heikou/state.json`; ordinary workstream files live in
`~/.heikou/workstreams/<id>/`. State updates are serialized with a
local advisory lock so CLI commands and the dashboard cannot overwrite one
another. V0.3.4 uses state schema v2 for durable titles. A valid v1 file is
validated, migrated, and atomically rewritten as v2 without manufacturing a
domain revision; future or invalid versions are rejected.

## The Heikou directory

Everything Heikou owns lives in one place, `~/.heikou`:

```text
~/.heikou/
  config.json          settings
  state.json           durable workstream/session state
  workstreams/<id>/    notes.md and artifacts
```

Installations created before this layout used three separate XDG directories.
The first run of a newer binary moves them into `~/.heikou` exactly once, prints
what it moved, and repoints the absolute artifact directories recorded in state.
It never runs when `HEIKOU_HOME` is set, never runs when `~/.heikou` already
exists, and leaves any individually overridden path alone. If a move fails it
stops and reports what already succeeded rather than running against a split
installation.

## Configuration

Press `Ctrl-S` (or `F2`) in the dashboard to open the settings pane. Press `e`
there to create/open `~/.heikou/config.json` in `$VISUAL`, `$EDITOR`, or
`vi`. Settings are deliberately one small JSON object:

```json
{
  "default_runner": "codex",
  "commands": {
    "codex": ["codex"],
    "claude": ["claude", "--dangerously-skip-permissions"]
  },
  "composer_keys": {
    "reply": "space",
    "cycle_runner": "tab",
    "cycle_root": "shift+tab"
  }
}
```

Commands are argv arrays, not shell strings. Fixed flags are placed before the
task arguments Heikou adds. Callers select a runner, while the controller's
trusted config-backed resolver loads and resolves its argv immediately before
launch; a command action cannot supply arbitrary runner argv. The three
`composer_keys` fields may be omitted to keep the defaults shown above.
`reply` aims the empty composer at the selected session; `cycle_runner` and
`cycle_root` act whether or not the composer has text. All three are live at
once, so they may not share a key.
`Enter` is the single commit key and is not configurable — that is what keeps
the destination the one the composer displays. `Shift-Enter` inserts a newline
unless it is explicitly assigned to one of the configurable actions.
`Ctrl-G` is reserved for layout resize mode and cannot be assigned to a
composer action.
The removed `new_session` and `send_message` fields chose a commit key per
destination. A config still carrying either one fails to load with a message
naming `reply` as the replacement.
The settings pane displays the active bindings and reloads JSON changes with
`r`. Command changes affect new sessions; a changed `default_runner` applies
the next time the dashboard opens. `no-agent` is not configurable: it asks tmux
to start its default interactive shell without injecting the composer label.
Follow-up messages are then ordinary input to that shell, which makes it a
cheap transport test pane.

| Environment variable | Default | Purpose |
| --- | --- | --- |
| `HEIKOU_DEFAULT_RUNNER` | `codex` | Initial runner in the composer |
| `HEIKOU_TMUX_SOCKET` | `heikou` | Private tmux socket name |
| `HEIKOU_HOME` | `~/.heikou` | Directory holding every Heikou file |
| `HEIKOU_CONFIG` | `~/.heikou/config.json` | Settings file override |
| `HEIKOU_STATE` | `~/.heikou/state.json` | Durable application-state override |
| `HEIKOU_DATA` | `~/.heikou/workstreams` | Workstream artifact-directory base |
| `HEIKOU_CODEX_BIN` | `codex` | Codex executable name or path |
| `HEIKOU_CLAUDE_BIN` | `claude` | Claude executable name or path |

The dashboard also accepts `--runner`, `--root` / `-C`, and `--socket`.

## What Heikou deliberately does not claim

An interactive agent process stays alive while it is thinking, waiting for
input, or simply sitting at its prompt. Tmux cannot distinguish those semantic
states. Heikou therefore reports process truth only: `live`, `attached`,
`exited`, or `failed`, plus runtime, path, terminal activity, output preview,
and an exit code when tmux supplies one. Some retained dead panes—especially on
older tmux versions—omit `pane_dead_status`; Heikou reports their process as
exited with an unknown outcome and never guesses zero or persists a successful
exit. It does not invent “completed” or “needs input” states, nor does it guess
token usage.

Workstreams are organization, not autonomy. Heikou has no manager role,
coordination grants, approvals, parent-child sessions, task graph, automatic
restart, queue, daemon, or MCP message bus. Durable session records survive a
tmux-server loss, but a missing pane without an already recorded terminal
outcome is reported as `unavailable`—never guessed to have exited. Running
multiple editing agents in one checkout can still cause conflicts; the selected
working directory remains intentionally prominent.

All current human mutations pass through one closed typed command plane with
an explicit actor and installation/workstream scope. Its active policy admits
only the local human; session actors are rejected until real manager grants and
authorization semantics exist. This is a future extension seam, not manager
mode.

See [the design document](docs/DESIGN.md) for the architecture, research notes,
extension seams, and next steps. Future product ideas live separately in the
[`todos`](todos/README.md) folder; the first note describes a pluggable
[composer module system](todos/composer-modules.md).

## Development

```sh
make build
make check
go test -race ./...
```

The integration suite uses a randomly named private tmux server and a fake PTY
agent. It verifies caller-owned identity, lifecycle preservation, nonzero exits,
Unicode and paths with spaces, and literal delivery of shell-looking
prompt/message content. Controller tests cover durable-before-launch ordering,
failed launch retention, conservative reconciliation, orphan detection, and
explicit stop outcomes.
