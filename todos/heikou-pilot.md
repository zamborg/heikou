# Heikou pilot

Status: slices 1 and 2 shipped. The pilot is an installation-level manager built
on today's CLI and authority model, driven by an ordinary agent running in
`~/.heikou`. It is not the workstream manager described in
[the manager mode map](../docs/manager-mode-map.html). The remaining slices —
a Heikou-owned headless loop and any UI — are still open.

## The idea

A **pilot** is an agent Heikou owns and drives, whose job is to operate
Heikou itself through the `h` CLI. It reads `h list --json`, organizes
workstreams and sessions, maintains persistent notes, and starts or steers work
on the user's behalf. It is rooted in the Heikou data directory.

It exists because the dashboard's control surface is dense. `Ctrl-G`, `Ctrl-X`
twice, and the mark-then-move halves of `Ctrl-T` are learnable but not
guessable. The pilot replaces "which key organizes this?" with
"put the three OAuth sessions in a workstream called Auth and title them."

The pilot is a translation layer over the existing command plane. It is not a
new execution path, not a daemon, and not a grant holder.

## Why this before workstream manager mode

The two managers differ in what they need to be honest.

| | Pilot (installation) | Workstream manager |
| --- | --- | --- |
| Domain | organization: workstreams, membership, titles, roots, notes | coordination: sequencing and dispatching agent work |
| Needs semantic agent status | no | **yes** |
| Needs grants and authority levels | no | **yes** |
| Needs durable command queue, events, idempotent IDs | no | **yes** |
| Needs SQLite | no | probably |
| Authority model | runs as the local human | new `session` actor policy |
| Blocked on | nothing | authoritative Claude/Codex turn signals |

The pilot's domain is exactly the domain Heikou already has truth about.
Workstreams, memberships, titles, and roots are durable facts the controller
owns and validates. Nothing about organizing them requires knowing whether an
agent is currently thinking — which is the unsolved prerequisite blocking
bounded autonomy in [session-status-titles.md](session-status-titles.md).

## The gap this closed

The read surface is already sufficient. `h list --json` returns workstream `id`,
`name`, `description`, `artifact_dir`, `roots`, and `revision`, plus per-session
`state`, `title`, `display_title`, `initial_prompt`, `latest_via_heikou`,
`workstream_id`, `root`, `available`, `alive`, `orphaned`, `exit_code`,
`runtime_seconds`, and `last_activity_at`. An agent can orient from that alone.

The write surface had a hole. `Controller` implemented every organization
action with full validation through the typed command plane, but eleven of them
were reachable **only as keystrokes in the organizer view** that has since been
folded into the dashboard. Slice 1 gave each one
a verb:

| Controller action | CLI verb |
| --- | --- |
| `Start` | `h spawn` |
| `Send` | `h send` |
| `Stop` | `h stop` |
| `AttachCommand` | `h attach` |
| `CreateWorkstream` | `h ws create` |
| `RenameWorkstream` | `h ws rename` |
| `ReorderWorkstream` | `h ws reorder` |
| `ArchiveWorkstream` | `h ws archive --yes` |
| `MoveSession` | `h move` |
| `AdoptSession` | `h adopt` |
| `SetSessionTitle` | `h title` |
| `AddRoot` | `h ws root add` |
| `ReplaceRoot` | `h ws root set` |
| `RemoveRoot` | `h ws root rm` |
| `DeleteSession` | `h delete --yes` |

Before that, a pilot could start and message sessions but could not organize
anything, which is the entire job. The surface is independently valuable: it
makes Heikou scriptable by humans, not just by agents.

## The pilot is not a tmux session

The obvious design is "the pilot is an ordinary Heikou session." That is wrong,
and the reason is measurable rather than aesthetic.

### Measured: `capture-pane` cannot show a conversation

Reading a native agent's output without attaching means `Capture`, which runs
`capture-pane -p -J -S -120`. Against a live `claude` pane, that call returned
**85 lines, of which 60 were shell output produced before `claude` ever
started.** The `claude` portion was exactly the visible 24-row frame.

The alternate screen is why. A full-screen TUI switches to the alt grid, which
has no scrollback: content that scrolls off is unrecoverable. `capture-pane -S`
reads back into the *normal* screen's retained history, so what comes back is a
fossil of whatever preceded the runner, plus one current frame. A control
line written to the alt screen and scrolled past returned zero matches; a line
still inside the visible frame returned one.

Heikou `exec`s directly into the runner rather than going through a shell, so
its own sessions carry no pre-launch fossil. For them the capture is simply the
bare current frame. Either way there is no conversation to render, at any width.

This also means asking for 120 lines and rendering the result is quietly
misleading today. It is not a pilot-specific problem, and it deserves its own
entry in [the audit](code-quality-audit.md).

### The runtime type does not fit either

`heikou.Session` carries `PaneID`, `AttachedClients`, `PaneInMode`,
`InputDisabled`, `ExitCode`, and `AttachCommand`. For a pilot every one of those
is meaningless or actively harmful. `Tmux.Send` refuses delivery when
`PaneInMode` is set, so a user who scrolls inside the pilot's pane would break
their own pilot. A "one live pilot per installation" rule would be a constraint
bolted on to make a plural abstraction hold a singular thing.

### The pilot is headless, and this does not weaken the thesis

`DESIGN.md` rejects zwarm's headless model for a specific, correct reason: it
makes raw native attachment and steering a running turn impossible. That reason
does not apply to the pilot. Nobody wants to attach to their file manager.

Heikou's honesty rule is *do not claim semantic state you cannot prove*. Driving
the pilot loop directly satisfies that rule harder than tmux does: Heikou owns
the process, so turn boundaries are authoritative rather than inferred.

The invariant that keeps this from becoming two ways to run an agent:

> A headless runner is permitted only for an agent Heikou itself owns, and such
> an agent never appears in `Snapshot.Sessions`.

That is one assertion in a test. The moment a headless thing becomes a row a
user can attach to, the carve-out has rotted and this document was wrong.

Verified as available for the first implementation: `claude -p` supports
`--session-id <uuid>`, `--resume`, `--continue`, and
`--output-format stream-json`. Heikou already allocates a UUID before launch and
already supplies it to Claude as its session ID, so pilot conversation
continuity across dashboard restarts uses identity Heikou owns. Codex headless
support needs the same verification before it is offered.

## Blast radius, stated plainly

The pilot is rooted in the Heikou data directory, so it cannot edit project code
directly. But `h spawn` starts native agents in project roots registered on
workstreams. **A pilot that can spawn causes edits in repositories it cannot
itself read.** That is the real consequence surface.

A conversation view makes that the frictionless path, which is a genuine
regression risk: "I've started three sessions in ~/work/api" as a sentence is
weaker than the keystroke maze it replaces. The eventual fix is that a spawn
surfaces as a Heikou-rendered confirmation the human presses, not a sentence the
agent writes — which is only possible because Heikou owns the loop.

Be honest about the limit: owning the loop is necessary, not sufficient. A
headless agent with shell access can still call `h spawn` directly. Real gating
would mean Heikou owning the pilot's whole tool surface, which is a larger
design than v1. Until then, scoping is by which verbs exist and what the skill
teaches, and that is a guardrail rather than enforcement.

## Design

### Slice 1 · complete the CLI verb surface

Wire the existing controller methods to verbs. No new controller code, no new
authority, no new state. Each verb constructs the same `local_human` command the
TUI does, and reuses the existing `resolveWorkstream` name/ID-prefix resolution.

```sh
h ws list [--json]
h ws create NAME -C ROOT [-d DESCRIPTION] [--json]
h ws rename ID NAME
h ws reorder ID --up|--down
h ws archive ID
h ws root add ID PATH
h ws root set ID OLD NEW
h ws root rm ID PATH

h title ID [TITLE]              # empty value clears the title
h move ID --workstream W|--ungrouped
h adopt ID [--workstream W]
h delete ID
```

This slice is the contract. It stands alone, ships alone, and must merge before
any pilot work. Building a conversation view for an agent with no verbs to call
is building a window onto nothing.

### Slice 2 · the `manage-heikou` skill

A second embedded skill beside `learn-heikou`, following the same pattern:
`skills/manage-heikou/SKILL.md` plus an `embed.go` exposing `Instructions`.

`learn-heikou` teaches a human to press keys. `manage-heikou` teaches an agent
to drive the CLI. It must establish:

- the noun model, and that a workstream organizes but does not coordinate;
- `h list --json` as the only orientation step, run before every action;
- the CLI as the **only** mutation path;
- that `state.json` is never edited by hand, because direct writes bypass the
  advisory lock, schema validation, and revision;
- that workstream `notes.md` under `artifact_dir` is the durable place to record
  decisions;
- that Heikou reports process truth only, so the pilot must never describe a
  session as working, ready, or blocked; and
- that the user confirms before anything that starts or stops a process.

### Slice 3 · the pilot loop

A new seam, deliberately not an extension of `Supervisor`. `Supervisor` is the
tmux/PTY boundary, and `Send(ctx, Session, string) error` is an honest signature
for pasting into a pane and a wrong one for a turn that has a response.

The pilot interface owns one operation — ask, and get an answer — plus the
durable conversation identity needed to resume. Its turns are Heikou's own
records, not `SessionRecord`s, and they do not enter `Snapshot.Sessions`.

Settings stay one small strictly validated object. Add a `pilot` block that
selects a runner and nothing else:

```json
{
  "pilot": {
    "runner": "claude",
    "instructions": "/path/to/extra-instructions.md"
  }
}
```

`instructions` is optional and appended to the embedded skill, so a user can add
house rules without forking the binary. The block deliberately **cannot** supply
argv — executable resolution stays with the trusted `CommandResolver`, which is
the boundary that stops a command payload from choosing a binary.

Note for implementation: a native `claude` launch into an untrusted directory
opens a folder-trust prompt before anything else. The pilot's data directory
must be handled explicitly rather than discovered at first run.

### Slice 4 · selection-routed rendering

No vertical split and no new input widget. The details pane already dispatches
on what is selected — `renderDetails` branches to `renderWorkstreamDetails` for
workstream and orphan-header rows. The pilot adds one more branch.

- A pinned pilot row sits above the workstream projection, outside the normal
  sort.
- Selecting it renders the conversation in the details pane, replacing the
  session snapshot.
- The composer needs no new rule at all. It already chooses its destination
  before you type and names that destination in its prefix, and `Enter` is
  already the sole commit key. A pilot is one more destination: the prefix reads
  as the pilot, and the same `Enter` commits there.

That visible-destination model removed the question this section originally had
to answer. There is no "which key sends where" ambiguity left to resolve, so the
pilot inherits the answer rather than introducing a binding.

No chord is needed either, because selection is the toggle and the arrow keys
already move selection. `Ctrl-M` is in any case unavailable: it is the same byte
as `Enter`, and `Ctrl-J` is already the documented newline fallback. If a
surface-level toggle is ever wanted, `F4` is the right key — it joins the
existing `F1`/`F2`/`F3` family and is not in `reservedComposerKeys`.

## Scope the pilot by capability, not by policy

The pilot can shell out. It can call any `h` verb regardless of what actor label
Heikou attaches, so an authorizer rule would be theater: the agent is
indistinguishable from the human at the CLI boundary, and any environment marker
Heikou injects can be unset by the process it marked.

The honest lever is which verbs exist, not which callers are allowed.

| Pilot-appropriate | Human-only in v1 |
| --- | --- |
| `h ws create` / `rename` / `reorder` | `h ws archive` |
| `h ws root add` | `h ws root rm` / `set` |
| `h move`, `h adopt`, `h title` | `h delete` |
| `h send` | `h stop` |
| notes and artifact files | `state.json` |

`h spawn` is the hard case. It is the pilot's most useful verb and its largest
consequence, so v1 does not teach it in the skill; it waits for the
Heikou-rendered confirmation described above.

## What this deliberately does not claim

- **It is not a security boundary.** A pilot with shell access can run any
  command the user can. Scoping here is a guardrail against agent mistakes, not
  a sandbox. Real isolation needs OS-level sandboxing, which the manager map
  already lists as not guaranteed.
- **It is not manager authority.** The pilot acts as the local human because it
  invokes the same CLI. It holds no grant, and `localHumanAuthorizer` is
  unchanged. Adding a pilot does not enable session actors.
- **It does not know what agents are doing.** The pilot sees process truth only.
  It must never report a session as working, ready, or blocked.
- **It is not attachable and is not a session.** It has no pane, no exit code,
  and no membership, and it never appears in `Snapshot.Sessions`.

## Acceptance order

1. [x] Add the workstream and session CLI verbs in Slice 1 with tests covering
   name/ID-prefix resolution, `--json` results, and the existing validation
   errors surfacing as clean CLI failures. Flags are also accepted on either
   side of a positional argument, because the Go default silently folded them
   into workstream names.
2. [x] Add `skills/manage-heikou` and its embed, with a test asserting the
   instructions name the CLI-only mutation rule and the no-hand-editing rule.
   Installed into the Heikou home as `AGENTS.md`, a `CLAUDE.md` pointer, and
   `skills/manage-heikou/SKILL.md`, never overwriting an edited file.
3. [ ] Add the pilot seam and its `pilot` settings block, with strict decoding,
   a rejected-argv test, and the `Snapshot.Sessions` exclusion assertion.
4. [ ] Add `h pilot` for a headless conversation from the CLI, including
   durable conversation identity and resume.
5. [ ] Add the pinned row and the `renderDetails` branch, with fixed-width view
   tests at 40×15, 80×24, and 120×40 as the existing suite requires.
6. [ ] Only then consider a spawn confirmation surface, which is what would let
   the pilot be taught `h spawn` at all.

Slices 1 and 2 carry the value. If the pilot concept is dropped afterward, a
complete scriptable CLI and an accurate operating skill remain useful on their
own.
