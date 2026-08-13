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
- **Brief** — the one-line summary in the middle of a session row. Its lead is
  the session's title, initial task, or runner; the text after `↳` is the latest
  message sent through Heikou. A leading `~` marks text Heikou derived rather
  than observed.
- **Ungrouped** — durable sessions with no active workstream membership.
- **Orphaned** — tmux panes carrying a Heikou ID unknown to durable state; they
  are never silently adopted.

## Install

Heikou currently targets macOS. Requirements: Go 1.25+, tmux 3.3+, and at
least one of `codex` or `claude`. Runner commands can be configured when they
are not on `PATH`; Heikou also discovers Codex inside the macOS ChatGPT app
bundle. tmux 3.5+ additionally lets Codex read modified keys such as
`Shift-Enter`; see below.

```sh
go install github.com/zamborg/heikou/cmd/h@latest
h doctor
```

`@latest` resolves to the newest release tag, so this command never goes stale.
Substitute an explicit tag when you need a particular release.

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
so it can walk you through sending a follow-up, reattaching, workstreams,
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
| Type a message, then `Enter` | Send it to the pinned session. The selection is held there while you draft, so the pane below keeps showing the conversation you are answering. Once it lands, the composer returns to composing a new session — press `Space` again to follow up |
| `Esc` while replying | Return to composing a new session, discarding the draft with it so the next `Enter` cannot spawn a session from a follow-up |
| `Shift-Enter` | Insert a newline; `Ctrl-J` is the fallback for terminals that cannot distinguish shifted Enter |
| `Option-Left` / `Option-Right` | Move by word; `Option-Delete` deletes the previous word |
| `Command-Left` / `Command-Right` | Move to the start or end of the logical line |
| `Command-Up` / `Command-Down` | Move to the start or end of the whole draft |
| `Tab` | Cycle Codex → Claude → `no-agent`, with or without composer text |
| `Shift-Tab` | Cycle the selected workstream's explicit roots, with or without composer text |
| `F1`, or `?` with an empty composer | Open scrollable help, including the noun glossary and current composer keys |
| `Ctrl-S` or `F2` | Open settings; `e` edits JSON, `r` reloads, `Esc` returns |
| `F3` | Re-read sessions, the terminal preview, and the selected workstream's notes and files |
| `Ctrl-N` | Create a workstream, named through the composer |
| `Ctrl-R` | Rename the selected workstream, or edit/clear the selected session's durable title |
| `Ctrl-T` | Mark the selected session for a move; on a workstream, move the marked session there or adopt an orphan |
| `Shift-Up` / `Shift-Down` | Reorder a named workstream, or move a session to the adjacent workstream |
| `Up` / `Down` | Select a workstream or session, or move between multiline composer rows |
| `Ctrl-G` | Enter resize mode; `Up` grows the lower pane, `Down` shows more sessions, `r` resets, and `Esc` exits |
| `Enter` on a workstream | Collapse or expand its sessions |
| `Enter` on a session | Attach its native terminal; inactive while replying, so it cannot attach to a row other than the pinned target |
| `Ctrl-\` or `Ctrl-b d` while attached | Detach back to Heikou |
| `Ctrl-X` twice | Stop/remove a present runtime; once no pane remains, press twice again to delete its durable record |
| `Esc` | Leave a reply and discard its draft, then clear the composer, then release a move mark, then select Ungrouped |
| `Ctrl-C` | Quit the dashboard; `Esc` never quits |

The terminal application decides whether macOS modifier chords reach a TUI.
Heikou accepts enhanced Option/Command events plus common Alt, Home/End, and
Ctrl-key fallbacks. If a terminal reports `Shift-Enter` as ordinary Enter, use
`Ctrl-J` for a newline.

Inside an attached runner the same chords are tmux's business rather than
Heikou's, and Heikou settles them for you: the private server encodes modified
keys for every pane instead of letting each runner negotiate. Claude Code asks
for a scheme tmux implements and Codex asks for one it does not, and a pane that
falls back to the legacy encoding receives `Shift-Enter` as a plain Enter — so
the key meant to open a line sends the message instead. A pane reads this when
it starts, so a session launched before 0.7.0 keeps the old behaviour until you
restart it. The option that names the encoding Codex can read arrived in tmux
3.5; on 3.3 and 3.4 the key stays distinguishable from Enter, but only Claude
Code can read it.

Every full-screen surface carries an unmistakable mode badge: **Dashboard**,
**Settings**, or **Help**. Organizing happens on the dashboard rather than in a
view of its own.

Each session row carries a **brief**: the durable user title when one is set,
otherwise a one-line initial task, followed after `↳` by the most recent message
successfully sent through Heikou, for as long as the tmux runtime is retained.
The two halves get separate width budgets, so a long title cannot crowd the
message out and a narrow row drops the message rather than showing a fragment of
it. Rows omit the **latest via Heikou** label that names the field — twenty
columns the message itself can use — and the details pane below still spells it
out. Text entered directly in an attached native terminal is not observable by
this shim.

Which sources fill a brief is a single ordered layout you can change in
settings, so a row can show only your title, or carry a runner's own status
line. Anything a source cannot prove from durable state or a tmux observation
renders with a leading `~`, the same way an exit code tmux cannot prove is
reported as unknown rather than as zero.

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

Every organizing action is also a command, so the whole durable model can be
driven without the TUI. Root and archive management live here only:

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
h history a1b2c3 --last 10
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
  adopted into a workstream; `Ctrl-T` below makes adoption explicit.

Organizing is done in place. Each chord carries a verb and reads the selected
row for its noun, so one key covers both nouns it could apply to: `Ctrl-R`
renames a workstream or retitles a session, and `Shift-Up`/`Shift-Down`
reorders a named workstream or walks a session to the adjacent one. `Ctrl-N`
creates a workstream. `Ctrl-T` marks a session with `◆` and moves it into the
next workstream you select, adopting an orphan explicitly when that is what it
is. The synthetic Ungrouped and Orphaned sections remain fixed after named
workstreams.

`Ctrl-O` edits the selected workstream's roots. Roots get one chord rather than
three because they are one list the dashboard already has a cursor for:
`Shift-Tab` picks which root a new session launches into, and that is the root
`Ctrl-O` opens. Press it again to walk to the next root, and once more to reach
an empty slot that adds one — so adding is editing the slot past the end rather
than a separate mode. `Enter` saves the path shown; an empty draft removes that
root and asks once more before doing it. A workstream always keeps its last
root. The composer prefix names the slot the whole time, which is what lets one
chord carry three outcomes honestly.

Because every printable key belongs to the composer, these are chords rather
than bare letters. That is the whole reason a separate organizer view existed;
folding the verbs into chords removed the view and the second set of keys with
it. Archiving stayed in the CLI rather than claiming a chord, since it is setup
rather than operation.

The read-only lower pane follows the selection: a workstream shows a bounded
`notes.md` preview and a shallow tree of its artifact directory, and a session
shows its terminal preview instead. A session resolves to its parent
workstream, so moving between a group and its members costs no extra read.
Press `Ctrl-G` to enter resize mode, then `Up` to grow the lower pane, `Down`
to expose more sessions, or `r` to restore automatic sizing.
Rendering context does not change domain state, inspect registered repository
roots, or modify files. The preview is cached against the selected workstream,
so it reads when the selection lands somewhere new and costs nothing while the
cursor sits still. Press `F3` after an agent or editor rewrites notes under a
stationary cursor; moving off the row and back does the same thing.

Roots are `Ctrl-O` on the dashboard and `h ws root add|set|rm` on the CLI;
archiving is `h ws archive`. Every workstream keeps at least one root, root
edits never rewrite historical session records or touch the filesystem, and
archiving keeps all durable sessions and moves their memberships to Ungrouped.

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

### Choosing what a brief shows

The `brief` block is two ordered lists of source names. Each slot takes the
first source with something to say. Omitting a slot keeps its default; an
explicitly empty `detail` is how you ask for a row that shows only its lead:

```json
{
  "brief": {
    "lead": ["title", "prompt", "runner"],
    "detail": []
  }
}
```

The built-in sources are `title`, `prompt`, `latest`, and `runner`. Anything
else must be defined under `brief.sources` as a command Heikou runs:

```json
{
  "brief": {
    "lead": ["title", "prompt", "runner"],
    "detail": ["status", "latest"],
    "sources": {
      "status": {
        "command": ["agent-status", "--porcelain"],
        "interval_seconds": 5,
        "timeout_seconds": 2
      }
    }
  }
}
```

The command is argv, not a shell string. It runs once per session, is told
which session through `HEIKOU_SESSION_ID`, `HEIKOU_SESSION_RUNNER`,
`HEIKOU_SESSION_STATE`, `HEIKOU_SESSION_ROOT`, and `HEIKOU_SESSION_TITLE`, and
prints one line to stdout. It is never given the session's prompt or messages.

A session is only re-run after `interval_seconds` **and** only if it has shown
terminal activity since the last look, so an idle dashboard costs nothing. Runs
are capped at four at a time and thirty-two per pass; a capped pass says how
many it deferred. Output is stripped of ANSI and control characters, reduced to
one line, and bounded. A source that fails or times out drops its text rather
than leaving a stale line that looks current.

Command output always renders with a leading `~`. The command may well be
reporting the truth, but Heikou cannot check that, and the mark is the
difference between what it observed and what it was told. Unknown source names,
duplicate entries, an empty `lead`, a source nothing refers to, and a timeout
longer than its interval are all load errors rather than surprises at runtime.
Brief changes apply as soon as settings are reloaded with `r`.
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

[`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) is the full version. The short one:

```sh
make check
```

That runs the same gates as CI, in the same order, so a green `make check`
should mean a pull request has nothing left to discover.

**There is nothing to build before pushing.** No binaries, generated code, or
vendored dependencies are committed; users compile from source at `go install`,
and the agent instruction files reach the binary through `//go:embed` at compile
time. Editing `SKILL.md` or a help string is the whole change.

**Releasing is bumping `version` in [`cmd/h/main.go`](cmd/h/main.go).** `@latest`
resolves to the newest tag rather than to `main`, so a change merged without a
bump reaches nobody while nothing looks wrong. When CI passes on a `main` commit
whose `version` has no tag, the `Tag` workflow creates and pushes it; when the
version is unchanged it reports how far `main` has drifted ahead of the last
release. Nobody tags, builds, or uploads by hand.

The integration suite uses a randomly named private tmux server and a fake PTY
agent. It verifies caller-owned identity, lifecycle preservation, nonzero exits,
Unicode and paths with spaces, and literal delivery of shell-looking
prompt/message content. Controller tests cover durable-before-launch ordering,
failed launch retention, conservative reconciliation, orphan detection, and
explicit stop outcomes.

The end-to-end suite builds `h` and drives it as a subprocess against a
throwaway `HEIKOU_HOME` and a private tmux socket, so argument parsing, refusal
text, `--json` shape, and exit codes are tested as they ship. Alongside it, the
in-process suite drives the same verbs directly against a stub controller, which
is where the twenty-odd refusals and every `--json` key are checked without a
tmux server anywhere in sight.

The tmux-dependent suites skip themselves without tmux. Set
`HEIKOU_TEST_REQUIRE_TMUX=1` — as CI and `make race` do — to turn that skip into
a failure, so a run cannot report green over a suite that never executed.

`internal/architecture` holds the module's shape: which package may import
which, and which concerns are allowed exactly one home. Adding a package means
placing it in that map, and an import that contradicts the layering fails there
rather than being discovered during a later refactor.
