# Session status, titles, and recency

Status: next after the current layout nits. Deliver titles and semantic status
as separate slices.

Current stopgap: the tmux runtime retains a bounded preview of the latest user
message successfully sent through Heikou, and the dashboard uses it instead of
the initial prompt while that runtime is retained. This is intentionally not
durable chat history; direct native-TUI input remains unknowable. The durable
`SessionActivity` design below is still deferred.

## Product goal

Make the dashboard answer three different questions without conflating them:

1. Is the native process alive?
2. What is the agent doing in its current turn?
3. Is there a returned result the user has not opened yet?

At the same time, let users give sessions durable titles and show the most
recent message routed through Heikou instead of treating the immutable launch
prompt as the session's forever-label.

## Honest status model

Keep three independent axes.

**Runtime lifecycle observation** remains process truth owned by `Supervisor`;
the controller owns the joined lifecycle projection shown by the UI:

- `starting`
- `live`
- `exited`
- `stopped`
- `start_failed`
- `unavailable`

**Agent turn state** comes only from a fresh, backend-specific observation:

- `working`: a turn is active;
- `ready`: the CLI can accept another message;
- `needs_input`: an explicit approval, question, or input request; and
- `unknown`: no trustworthy fresh observation.

**User attention state** distinguishes a newly completed result:

- `returned`: a completed turn has not been opened; and
- `seen`: that completion has been acknowledged.

The UI precedence is `needs you`, `working`, `returned`, `ready`, then
`live · unknown`. Terminal runtime states always win. Stale observations fall
back to `unknown`, but do not erase a previously recorded unread completion.

Tmux silence is not semantic evidence. Until a runner observer exists, render
facts such as `active 12s ago`, `quiet 3m`, or `attached`; never translate those
signals into `working`, `ready`, `returned`, or `needs input`.

## Slice A · durable session titles

Add an optional user-owned `Title` to `SessionRecord`. It is durable display
identity, not process state, and must not rename the stable tmux session.

This is the immediate next implementation:

1. Add an explicit v1-to-v2 state migration with versioned JSON fixture tests.
2. Add optional `SessionRecord.Title` and a controller `SetSessionTitle` action;
   an empty value clears the title.
3. Make `r` contextual in F3: rename a workstream header or edit a session
   title.
4. Render title first, falling back to a one-line initial prompt. Keep the
   latest Heikou-routed message as secondary detail when space permits.
5. Never rename the tmux session, Claude/Codex native session, or teach
   `Supervisor` about presentation metadata.

## Deferred presentation activity

Keep recency and acknowledgement metadata separate from `SessionRecord`, keyed
by session ID:

```text
SessionActivity
  last_heikou_user_preview
  sent_at
  truncated
  acknowledged_turn_id
```

This is bounded presentation metadata, not chat history or an event log. The
preview should be normalized to one line and capped near 280 characters. Write
it only after `Supervisor.Send` succeeds. If the send succeeds but persistence
fails, report the metadata failure and never retry the message automatically.

Call the field **latest via Heikou** in the UI. Messages typed directly inside
an attached Claude or Codex TUI bypass Heikou and cannot be claimed as known.
The initial prompt remains the fallback when no later preview exists.

## Dashboard and organizer UX

A dashboard row should prioritize attention state and the user-owned title:

```text
● working    Fix flaky OAuth tests
              latest via Heikou · “also update the release notes”

◆ returned   Release Linux build
              returned 42s ago
```

Use the explicit title when present; otherwise derive a one-line display label
from the initial prompt without persisting an automatic title. At narrow widths,
keep status and title before runner details or the message preview.

The details pane should show title, agent state, runtime activity, latest via
Heikou, initial task, cwd, and the existing terminal preview.

In the F3 organizer, make `r` mean **rename the selected noun**:

- on a workstream header, rename the workstream;
- on a session row, edit its title; and
- saving an empty session title clears it and restores the prompt-derived label.

Selecting a row never acknowledges a returned result. Attaching/opening it with
Enter, or successfully sending the next message, does. A later explicit
“open result” action may use the same acknowledgement path.

Do not add this table in the title slice. The retained tmux preview already
provides the cheap latest-message shim; durable recency and acknowledgement can
wait until their lifecycle is needed.

## Slice B · real agent status

Before adding semantic state, fix the known retained-pane ambiguity tracked in
[`code-quality-audit.md`](code-quality-audit.md): an empty `pane_dead_status`
must project as dead with an unknown outcome, never exit code zero, and must not
produce a durable `OutcomeExited` record.

Then run a focused signal probe. Claude exposes session listings and hooks;
Codex app-server schemas expose turn and input state, but independently launched
native TUIs are not automatically attached to that server. Prove inheritance,
freshness, direct-TUI coverage, and stable completion identity before choosing
a source.

### Observer seam

Real turn status must come from reliable Claude- or Codex-specific signals, not
terminal scraping heuristics. Keep the observer above `Supervisor` and make it
batch-oriented:

```go
type AgentObservation struct {
	State           AgentState
	TurnID          string
	CompletedTurnID string
	StateSince      time.Time
	ObservedAt      time.Time
	FreshUntil      time.Time
	Source          string
}
```

A backend-selected `AgentObserver.Observe` returns observations for session
references. The controller projection combines those with runtime and durable
presentation metadata. Current observations live outside `SessionRecord` and
`Supervisor` remains unaware of semantic agent state.
`CompletedTurnID` is the stable token used to determine whether a return is
unread.

Potential sources include Claude hooks or structured agent output and Codex
notifications or app-server events. Choose the first source only after proving
that its turn-start, turn-complete, and input-request signals are reliable.

## Acceptance order

1. Ship Slice A by itself: migration, title set/clear, contextual F3 rename,
   rows/details, and downgrade/invalid-state tests.
2. Represent dead panes with unknown outcomes honestly and stop persisting
   guessed success.
3. Probe authoritative Claude and Codex signals in fixtures or opt-in smoke
   tests.
4. Add the observer interface with only `working`, `ready`, and `unknown` beside
   the first reliable runner integration. Terminal outcomes always win.
5. Add `needs_input` only after its signal is proven.
6. Add durable `returned`/`seen` acknowledgement only when a backend supplies
   stable completed-turn IDs.

The title slice provides useful organization immediately. Semantic labels and
the `returned` badge remain deferred until Heikou has sources it can trust.
