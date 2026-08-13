# Session ordering within a workstream

Status: proposed; deferred out of the dashboard consolidation PR.

## The idea

`Shift-↑` / `Shift-↓` reorders a named workstream today, and that ordering is
durable. The obvious symmetry is for the same chord on a session row to reorder
that session inside its workstream, so the rows you care about sit at the top of
their group.

## Why it is not in the consolidation PR

There is no durable session order to change. `Membership` is
`{WorkstreamID, SessionID, JoinedAt}` and `State.Sessions` is a flat list, so
the grouped projection in `overviewModel.sessionsByWorkstream` emits members in
whatever order `snapshot.Sessions` arrived in. Reordering has nothing to write.

The consolidation PR is a renderer and keymap change that deliberately leaves
the domain untouched. Adding a durable order field is a state-schema change with
a migration, so it does not belong in the same slice.

Instead, `Shift-↑` / `Shift-↓` on a session row moves it to the previous or next
workstream, which is plain `MoveSession` and needs nothing new.

## What it would take

1. An explicit order on membership — either a `Position int` on `Membership` or
   an ordered `SessionIDs []string` per workstream. Position on the membership
   is closer to the existing shape and keeps ungrouped sessions representable.
2. A state version bump and a migration that assigns initial positions from
   `JoinedAt`, so existing installations keep their current visible order.
3. A `ReorderSessionAction` on the command plane with the same validation and
   revision discipline as `ReorderWorkstreamAction`, plus an `h` verb for it so
   the CLI surface stays complete.
4. A tie-break rule for sessions that arrive while a reorder is in flight.

## Open question

Whether manual ordering is the right primitive at all. Sorting by recency or by
live-first may serve the underlying want — "the rows I care about sit at the
top" — without any durable state, and without a migration. Decide that before
building the schema change.
