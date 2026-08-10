# Session status, titles, and recency

Status: proposed; save for a later implementation pass.

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

**Runtime lifecycle** remains process truth owned by `Supervisor`:

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

## Durable presentation metadata

Add an optional user-owned `Title` to `SessionRecord`. It is durable display
identity, not process state, and must not rename the stable tmux session.

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

## Observer seam

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
presentation metadata. `Supervisor` remains unaware of semantic agent state.
`CompletedTurnID` is the stable token used to determine whether a return is
unread.

Potential sources include Claude hooks or structured agent output and Codex
notifications or app-server events. Choose the first source only after proving
that its turn-start, turn-complete, and input-request signals are reliable.

## Later implementation order

1. Add title and bounded `SessionActivity` storage with migration tests.
2. Add controller operations for rename, clear-title, and successful-send
   preview updates.
3. Update dashboard rows, details, and contextual F3 rename behavior.
4. Improve honest runtime/activity labels using current tmux observations.
5. Add the observer interface alongside the first reliable runner integration.
6. Add durable returned-result acknowledgement once completed-turn IDs exist.

The first four steps provide useful organization and recency immediately. The
`returned` badge remains deferred until Heikou has a source it can trust.
