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
- **Session** — a durable launch identity with its initial task, root, runner,
  and outcome; it persists beyond its runtime.
- **Runtime** — the tmux pane associated with a session and the source of its
  current process observations.
- **Root** — an explicit launch working directory registered on a workstream.
- **Runner** — the `codex`, `claude`, or `no-agent` command integration used to
  launch a native agent or shell.
- **Composer** — the dashboard input bar used to start sessions and send
  follow-up messages.
- **Ungrouped** — durable sessions with no active workstream membership.
- **Orphaned** — tmux panes carrying a Heikou ID unknown to durable state; they
  are never silently adopted.

## Install

Requirements: macOS, Linux, or WSL; Go 1.25+; tmux 3.3+; and at least one of
`codex` or `claude`. Runner commands can be configured when they are not on
`PATH`; Heikou also discovers Codex inside the macOS ChatGPT app bundle.

```sh
go install github.com/zamborg/heikou/cmd/h@v0.3.1
h doctor
```

Ensure `$(go env GOPATH)/bin` is on your `PATH`. To install the `heikou`, `h`,
and `H` aliases together, build from source instead:

```sh
git clone https://github.com/zamborg/heikou.git
cd heikou
make install
```

`make install` writes `heikou` to `~/.local/bin` and adds `h` / `H` symlinks.
Override the destination with `make install PREFIX=/somewhere`.

## Use it

Run `h` (or `H`) from the directory that new agents should use as their root:

```sh
cd ~/code/my-project
h
```

The composer is always ready:

| Action | Result |
| --- | --- |
| Type a task or label, then `Enter` | Start the chosen Codex, Claude, or `no-agent` session |
| Type a message, then `Tab` | Send it to the selected live session |
| `Tab` with an empty composer | Cycle Codex → Claude → `no-agent` |
| `Shift-Tab` with an empty composer | Cycle the selected workstream's explicit roots |
| `F1`, or `?` with an empty composer | Open scrollable help, including the noun glossary and current composer keys |
| `Ctrl-S` or `F2` | Open settings; `e` edits JSON, `r` reloads, `Esc` returns |
| `F3` | Open the expandable workstream/session organizer |
| `Up` / `Down` | Select a workstream or session |
| `Enter` on a workstream | Collapse or expand its sessions |
| `Enter` on a session | Attach its native terminal |
| `Ctrl-\` or `Ctrl-b d` while attached | Detach back to Heikou |
| `Ctrl-X` twice | Stop/remove a present runtime; once no pane remains, press twice again to delete its durable record |
| `Esc` | Clear the composer; press again to leave the dashboard |

Every full-screen surface carries an unmistakable mode badge: **Dashboard**,
**Workstream Organizer**, **Settings**, or **Help**.

Session rows show the most recent message successfully sent through Heikou for
as long as the tmux runtime is retained, then fall back to the initial task.
Text entered directly in an attached native terminal is not observable by this
shim.

Leaving the dashboard never stops an agent. Exited and failed panes remain
inspectable while tmux retains them. Stopping removes the runtime but preserves
the durable session record and its workstream history; deletion is offered only
after no tmux pane remains.

The same primitives are available without the TUI:

```sh
h spawn -r claude -C ~/code/project "Investigate the flaky test"
h spawn -r codex -C ~/code/project -w "Core" "Implement the fix"
h spawn -r no-agent -C ~/code/project "scratch shell"
h list
h send a1b2c3 "Also check whether the retry hides the root cause"
h attach a1b2c3
h stop a1b2c3
```

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
workstream or session selected.

The organizer's lower, read-only context pane follows the selected workstream;
selecting a session shows its parent workstream. It previews a bounded portion
of `notes.md` and a shallow tree of that workstream's artifact directory only.
Rendering context does not change domain state, inspect registered repository
roots, or modify files. The organizer also creates or renames workstreams,
opens notes/artifacts, and archives. On a named workstream, `p` adds a root,
`Shift-P` edits the root selected with `Tab`, and `d` twice removes that root
without deleting files or changing historical session records. Every
workstream keeps at least one root.
Archiving keeps all durable sessions and moves their memberships to Ungrouped.

The composer always shows its exact workstream and launch root. A workstream may
contain sessions launched from several registered roots, but membership never
implicitly adds a root.

Workstream state is separate from settings. It is stored in a versioned atomic
sidecar at `~/.local/state/heikou/state.json`; ordinary workstream files live in
`~/.local/share/heikou/workstreams/<id>/`. State updates are serialized with a
local advisory lock so CLI commands and the dashboard cannot overwrite one
another.

## Configuration

Press `Ctrl-S` (or `F2`) in the dashboard to open the settings pane. Press `e`
there to create/open `~/.config/heikou/config.json` in `$VISUAL`, `$EDITOR`, or
`vi`. Settings are deliberately one small JSON object:

```json
{
  "default_runner": "codex",
  "commands": {
    "codex": ["codex"],
    "claude": ["claude", "--dangerously-skip-permissions"]
  },
  "composer_keys": {
    "new_session": "enter",
    "send_message": "tab",
    "cycle_runner": "tab",
    "cycle_root": "shift+tab"
  }
}
```

Commands are argv arrays, not shell strings. Fixed flags are placed before the
task arguments Heikou adds. The four `composer_keys` fields may be omitted to
keep the defaults shown above; `new_session` and `send_message` apply when the
composer has text, while `cycle_runner` and `cycle_root` apply when it is empty.
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
| `HEIKOU_CONFIG` | `~/.config/heikou/config.json` | Settings file override |
| `HEIKOU_STATE` | `~/.local/state/heikou/state.json` | Durable application-state override |
| `HEIKOU_DATA` | `~/.local/share/heikou/workstreams` | Workstream artifact-directory base |
| `HEIKOU_CODEX_BIN` | `codex` | Codex executable name or path |
| `HEIKOU_CLAUDE_BIN` | `claude` | Claude executable name or path |

The dashboard also accepts `--runner`, `--root` / `-C`, and `--socket`.

## What Heikou deliberately does not claim

An interactive agent process stays alive while it is thinking, waiting for
input, or simply sitting at its prompt. Tmux cannot distinguish those semantic
states. Heikou therefore reports process truth only: `live`, `attached`,
`exited`, or `failed`, plus runtime, path, terminal activity, output preview,
and exit code. It does not invent “completed” or “needs input” states, nor does
it guess token usage.

Workstreams are organization, not autonomy. Heikou has no manager role,
coordination grants, approvals, parent-child sessions, task graph, automatic
restart, queue, daemon, or MCP message bus. Durable session records survive a
tmux-server loss, but a missing pane without an already recorded terminal
outcome is reported as `unavailable`—never guessed to have exited. Running
multiple editing agents in one checkout can still cause conflicts; the selected
working directory remains intentionally prominent.

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
