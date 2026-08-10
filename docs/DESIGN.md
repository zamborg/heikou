# Heikou design

## Product thesis

The fastest useful parallel-agent system is not a new agent runtime. It is a
high-quality control surface over the native runners people already trust.

Heikou V0 therefore optimizes one loop:

1. See every coding-agent session in one scan-friendly view.
2. Dispatch a new task without leaving the composer.
3. Preview and steer an existing session without opening it.
4. Attach to the exact native Codex or Claude terminal when full control is
   useful.
5. Detach back to the global view without stopping anything.

Durable workstreams now organize that loop without changing its execution
substrate. Structured signals and richer orchestration remain future observers
and callers of the same controller actions—not prerequisites for using the
dashboard.

## Research synthesis

### Claude Code Agent view

The closest production reference is Claude Code's [`claude agents` Agent
view](https://code.claude.com/docs/en/agent-view), not the in-session `/agents`
panel or experimental agent teams. Its strongest choices are:

- a sparse, full-terminal global list across directories;
- an always-available dispatch input at the bottom;
- action-oriented status ordering, with “needs input” prominent;
- a lightweight peek/reply layer before full attachment;
- direct attachment to the native conversation; and
- background work that survives leaving the dashboard.

Anthropic's implementation has a per-user supervisor and structured semantic
state, so Heikou cannot honestly reproduce all its statuses from tmux alone. The
V0 copies the interaction loop rather than pretending to copy the substrate.
Anthropic's [launch post](https://claude.com/blog/agent-view-in-claude-code) is
also useful visual evidence: compact rows, restrained status color, and almost
no card chrome.

### `zwarm`

The intended package appears to be [`zwarm`](https://pypi.org/project/zwarm/),
invoked as `uvx zwarm interactive`; the literal PyPI package named `swarm` is an
unrelated game. `zwarm` contributes useful vocabulary—spawn, list, peek, show,
watch, continue, kill—and a clean runner-adapter idea.

Its execution substrate is intentionally not reused. It launches headless
one-shot processes, redirects JSONL, and reconstructs follow-up context. That
makes raw native attachment and steering a running turn impossible. Heikou
inverts the design: the PTY-backed native session is truth, and structured logs
can become an optional observer later.

### TUI stack

Go plus [Bubble Tea v2](https://pkg.go.dev/charm.land/bubbletea/v2) is the right
V0 tradeoff:

- a single, quickly starting binary;
- an Elm-style state/update/view split;
- declarative alternate-screen and keyboard behavior; and
- `ExecProcess`, which suspends the dashboard, lends the terminal to tmux, and
  restores the dashboard afterward.

Rust with Ratatui/Crossterm remains a credible eventual foundation if profiling
or deeper terminal control justifies the extra implementation surface. The
runtime boundary below is intentionally independent of Bubble Tea.

## Architecture

```mermaid
flowchart LR
    UI["Bubble Tea dashboard"] --> C["workstream controller"]
    CLI["h spawn / send / attach"] --> C
    CFG["one JSON settings module"] --> UI
    CFG --> CLI
    C --> DS["versioned atomic state"]
    C --> S["Supervisor interface"]
    S --> T["private tmux server (-L heikou)"]
    T --> P1["one session + pane"]
    T --> P2["one session + pane"]
    P1 --> C["native codex"]
    P2 --> A["native claude"]
    T --> SH["native no-agent shell"]
    C -. "future typed caller" .-> M["manager agent"]
    S -. "future replacement" .-> D["daemon / MCP queue"]
    C -. "future observer" .-> O["structured runner events"]
    A -. "future observer" .-> O
```

The package boundaries are:

- `internal/heikou`: runner-neutral session types and the `Supervisor`
  contract;
- `internal/config`: the single JSON settings model and strict loader;
- `internal/workstream`: durable workstream/session/membership types and the
  versioned atomic store;
- `internal/control`: the sole join between durable organization and current
  runtime observation;
- `internal/runner`: tiny Codex and Claude argv adapters plus the exec wrapper;
- `internal/supervisor`: the tmux implementation;
- `internal/ui`: the dashboard reducer and renderer; and
- `cmd/h`: human-facing CLI commands and dependency diagnostics.

There is no Heikou daemon in V0. The private tmux server already provides the
needed process lifetime and PTY ownership. It is isolated from the user's normal
tmux server and configured with zero-session persistence.

## Session lifecycle

Before the first child starts, Heikou bootstraps the private server with:

- `exit-empty off`, so the supervisor can exist with zero sessions;
- global `remain-on-exit on`, so even an immediately failing runner leaves an
  inspectable pane and exit code;
- extended-key and passthrough support for modern agent TUIs;
- a large history buffer and `window-size latest`; and
- `Ctrl-\` as a root-table detach binding, in addition to normal `Ctrl-b d`.

The bootstrap marker is versioned so a later Heikou release can safely migrate
an already-running server's configuration. Environment variable *names* are
refreshed into tmux on each invocation; values are never embedded in a shell
command. Provider executables are resolved to absolute paths before spawn.
Codex resolution also checks known macOS application-bundle locations when the
bare `codex` name is absent from the login-shell `PATH`.

Each task becomes one tmux session and one canonical pane. The controller
allocates the UUID before launch; `Supervisor.Start` accepts that caller-owned
identity and never generates one. Metadata is encoded in tmux user options:

| Field | Purpose |
| --- | --- |
| stable UUID | Heikou identity; also supplied to Claude as its session ID |
| pane ID | immutable target for capture and input |
| runner | `codex`, `claude`, or `no-agent` |
| root | launch directory |
| initial prompt | stable list/detail fallback |
| started timestamp | elapsed runtime |

The ID is present in the tmux session name, environment, session metadata, and a
pane-scoped canonical marker in the same tmux command queue that creates the
session. Reconciliation therefore never depends on a successful later metadata
write. The tmux child directly invokes a small hidden Heikou exec mode with
argv—not a shell string. That process decodes metadata and replaces itself with
the native runner using `exec`. Prompts beginning with `-`, quotes, backticks,
and shell syntax remain literal.

`no-agent` deliberately takes a smaller path: Heikou omits the child command
from `tmux new-session`, so tmux starts its default interactive shell. The
composer text is retained only as the session label and is never injected into
that shell. Follow-up transport remains available, making this a cheap way to
test session creation, input, capture, attachment, and detachment without an
agent request.

### Settings

V0 has one settings file, normally `~/.config/heikou/config.json`. It contains a
default runner, argv arrays for Codex and Claude, and four composer bindings:
`new_session`, `send_message`, `cycle_runner`, and `cycle_root`. Arrays preserve
the exact executable/flag boundary and avoid shell parsing. Composer bindings
are context-aware: the first pair applies with text and the second while empty.
Environment compatibility variables override JSON values; explicit CLI flags
select the runner and root. Command changes apply to new sessions only.

`Ctrl-S` opens a read-only settings view; `e` creates/opens the JSON file in the
user's editor and `r` reloads it. The view displays the active composer bindings
alongside launch commands. There are intentionally no forms, accounts, or
daemon-owned settings in this iteration.

### Durable workstreams

Workstream state is application data, not configuration. It normally lives at
`~/.local/state/heikou/state.json`, independently of `internal/config`, with
ordinary artifacts at `~/.local/share/heikou/workstreams/<id>/`. The JSON
sidecar is versioned, mode `0600`, written by temp-file/fsync/rename, and guarded
by an advisory lock. Storage remains behind a repository interface so a later
SQLite implementation does not change the domain contract.

The deliberately small durable model is:

- `Workstream`: name, description, artifact directory, explicit roots,
  revision, timestamps, and optional archive time;
- `SessionRecord`: caller-owned launch ID, backend, initial prompt/root,
  creation time, launch intent/binding, and durable terminal outcome; and
- `Membership`: one optional active-workstream membership per durable session.

Starting a session is ordered as follows:

1. The controller allocates `SessionRecord.id`.
2. One atomic state write creates the record, optional membership, and pending
   launch intent.
3. The controller calls `Supervisor.Start` with that ID.
4. Success records a stable tmux binding; failure records `start_failed`.

An external launch failure never rolls back the durable record or membership.
The binding stores a stable driver/socket/session locator; `PaneID`, current
command, attachment count, and process state remain runtime observations only.

### Status and reconciliation

The runtime layer derives only states tmux can prove:

| State | Evidence |
| --- | --- |
| `live` | canonical pane process is alive |
| `attached` | live session has one or more tmux clients |
| `exited` | pane is dead with status 0 |
| `failed` | pane is dead with nonzero status |

`pane_dead_time` freezes runtime for exited sessions. `window_activity` provides
a coarse terminal-activity timestamp on tmux versions where no reliable
per-pane output time exists.

The controller conservatively joins those observations to durable records:

| Durable/runtime evidence | Projected result |
| --- | --- |
| durable ID plus matching live pane | `live` |
| durable ID plus matching dead retained pane | record `exited` and exit code |
| explicit stop whose tmux kill succeeds | record `stopped` |
| durable ID, no pane, no terminal outcome | `unavailable` |
| pane carrying an unknown durable ID | `orphaned` and excluded from membership |

Absence never implies exit, and Heikou never automatically restarts an
unavailable session. A positive matching pane can repair an ambiguous launch
result after a timeout; this is reconciliation, not automatic restart.
Legacy panes can enter the durable model only through an explicit organizer
action that atomically creates their `SessionRecord` and chosen membership;
ordinary reconciliation never adopts them.

Lifecycle cleanup is deliberately staged. Two presses of `Ctrl-X` stop and
remove a present runtime while retaining its durable record. Only after no pane
remains can another confirmed `Ctrl-X` delete that record and membership; the
controller refuses deletion whenever it still observes a live or dead retained
pane. Deletion never doubles as an implicit stop. A separate advisory lifecycle
lock spans each launch and deletion, preventing concurrent dashboard or CLI
processes from deleting a pending identity while its tmux runtime is created.

### Follow-up messages

Messages never enter a shell command constructed by Heikou. It:

1. writes the exact UTF-8 bytes to a uniquely named tmux buffer on stdin;
2. uses bracketed `paste-buffer -p -r` into the canonical pane;
3. deletes the buffer; and
4. sends the `Enter` key separately.

Delivery is refused for a dead pane, a pane in copy/scroll mode, or a pane whose
input is disabled. The selected terminal preview remains visible because an
alive process might currently be showing an approval dialog rather than its
normal composer. In a `no-agent` pane the destination is intentionally an
interactive shell, so that shell interprets follow-up text after delivery.

### Attachment

Bubble Tea suspends its renderer and restores the host terminal before running
`tmux -L heikou attach-session`. `TMUX` and `TMUX_PANE` are removed from the
child environment, so this works when Heikou itself is launched inside another
tmux server. Detaching returns control to the same dashboard process and forces
a fresh session/preview read.

## Interaction model

The primary dashboard is a grouped projection with selectable, collapsible
workstream rows, member session rows, a synthetic Ungrouped inbox, and a
separate Orphaned tmux section. Dashboard, Workstream Organizer, Settings, and
Help each render an unmistakable mode badge. The composer deliberately avoids
modal focus:

- non-empty `Enter` always creates a new session;
- non-empty `Tab` sends to the selected session;
- empty `Enter` attaches; and
- empty `Tab` cycles Codex, Claude, and `no-agent`.

When a workstream header is selected, empty `Enter` collapses/expands it instead
of attaching. `Shift-Tab` cycles that workstream's explicit roots. The composer
always renders the workstream and exact root that a typed `Enter` will use.
`F3` opens a two-pane organizer. Its upper pane is an expandable tree containing
named workstreams, Ungrouped, Orphaned, and their session rows. `Enter` on a
workstream expands/collapses it unless a move source is active, when it instead
moves a durable session or explicitly adopts an orphan there. `Enter` on a
session marks it as the move source; `m` also marks or completes a move. `u` or
`Space` returns to the dashboard with the highlighted workstream as launch
target or the highlighted session selected.

The lower pane is read-only context for the selected workstream; a selected
session resolves to its parent workstream. It renders a bounded `notes.md`
preview and shallow artifact-directory tree. This UI-owned read never mutates
domain state, modifies files, or inspects any registered repository root. The
organizer also supports create, rename, add/edit/remove-root, notes/files,
archive, and the same safe stop/delete lifecycle as the dashboard. Root edits
affect future launch choices only; they never rewrite historical session roots
or touch the filesystem.

This makes the two most common actions one keystroke after typing while keeping
their consequences distinct. A selected session's preview is always open, so
the dashboard does not require a separate peek mode. Multiline clipboard
content is collapsed to a single line; follow-up transport itself remains
capable of arbitrary UTF-8.

Rows stay intentionally sparse: process mark, runner, short ID, truthful state,
most recent Heikou-routed user message (falling back to the initial task),
optional root basename, and runtime. The recent-message preview is bounded tmux
metadata for the lifetime of the retained runtime; Heikou does not claim to see
text entered directly in an attached native TUI. Detailed path, activity, and
the exact terminal tail sit below the list.

`F1`, or `?` when the composer is empty, opens a scrollable, viewport-safe help
panel. It describes Heikou, reports the active composer bindings, and defines
the core nouns: workstream, session, runtime, root, runner, composer,
Ungrouped, and Orphaned.

Future composer-prefix ideas are kept outside the committed architecture in
[`todos/composer-modules.md`](../todos/composer-modules.md).

## Explicit extension seams

The `Supervisor` contract is the main seam. A future daemon, remote host, MCP
messaging service, or Codex app-server runtime can implement the same operations
without coupling the UI to its protocol.

Runner-specific observers should enrich sessions rather than take over PTY
ownership. Likely sources include Claude hooks or `claude agents --json`, Codex
notifications/app-server events, and optional append-only event logs. Those can
add semantic state, turn boundaries, approval questions, final responses,
tokens, cost, model, and notifications.

Workstreams remain above `Supervisor`; tmux never learns their meaning. A future
manager will issue the same typed controller actions rather than gaining a
second execution path. Manager roles, grants, leases, approvals, event queues,
execution-attempt tables, and daemons are intentionally absent today.

Editing isolation deserves its own explicit policy. A later spawn hook can
choose current checkout, new git worktree, existing worktree, or arbitrary
directory per task. It should never be silently bundled into the dashboard.

## Next iterations

1. Add structured runner observers and a real `needs input` priority state.
2. Add notifications for blocked, exited, failed, and unavailable transitions.
3. Add safe worktree spawn policies and conflict visibility.
4. Add pin, filter, tags, and richer workstream notes as projections.
5. Add model/token/cost telemetry only from authoritative runner events.
6. Move from one-second polling to tmux control mode or a daemon only when scale
   or latency makes that measurable.

## Testing contract

The automated suite covers:

- fixed-width views at 40×15, 80×24, and 120×40;
- CJK, emoji, and combining graphemes;
- multiline paste normalization;
- paths containing spaces and Unicode;
- literal prompts/messages containing shell-looking syntax;
- strict JSON settings, context-aware composer bindings, and exact configured
  argv transport;
- scrollable help/glossary and expandable organizer navigation;
- raw `no-agent` shells whose labels are never executed;
- durable-before-launch ordering and failed-launch retention;
- conservative reconciliation, staged stop/delete cleanup, and orphan
  separation;
- atomic workstream root replacement/removal without rewriting session history;
- bracketed tmux paste delivery;
- immediate/nonzero process exits and frozen runtimes; and
- cleanup against random private tmux sockets.

Real Codex and Claude smoke tests remain opt-in because they require local
authentication and can consume paid model usage.
