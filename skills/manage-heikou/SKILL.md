---
name: manage-heikou
description: Operate Heikou itself through the h CLI — create and rename workstreams, register roots, move sessions between workstreams, title sessions, start and steer agent sessions, maintain workstream notes, and report honest session state. Use when running inside ~/.heikou, when asked to organize Heikou workstreams or sessions, or when asked what Heikou is currently running.
---

# Managing Heikou state

Complete command reference for the Heikou pilot. The operating rules live in
`AGENTS.md` next to this file; this document is the surface.

Every command accepts `--json` for a machine-readable result and `--socket` to
target a non-default tmux socket. Workstreams and sessions may be named by full
id, id prefix, or — for workstreams — name or name prefix. An ambiguous prefix
is an error rather than a guess.

Flags may appear before or after positional arguments, so you can write a
command in whatever order reads naturally. The one exception is text that
itself begins with a dash — a title or a message — which must follow an
explicit `--`:

```sh
h title SESSION -- "-w is not a flag here"
h send SESSION -- "-y"
```

Without the `--` the command fails rather than guessing, which is the intended
behaviour: quietly sending the wrong message or starting the wrong runner is
worse than an error you can see.

## Orient

```sh
h list --json          # every workstream and session, with ids and paths
h list                 # the same, human-readable
h ws list --json       # workstreams only, with roots and session counts
```

`h list --json` returns per workstream: `id`, `name`, `description`,
`artifact_dir`, `roots`, `revision`. Per session: `id`, `runner`, `state`,
`title`, `display_title`, `initial_prompt`, `latest_via_heikou`,
`workstream_id`, `workstream`, `root`, `available`, `alive`, `orphaned`,
`exit_code`, `runtime_seconds`, `last_activity_at`.

`state` is one of `live`, `exited`, `stopped`, `start_failed`, `unavailable`.
`exit_code` is `null` when tmux cannot prove the outcome — that is an unknown
result, not a success.

## Workstreams

A new installation is seeded once with `heikou-managers`, rooted only at the
Heikou home directory. It is where pilots run. If the user removes it, it stays
removed; `h init` is the only way it comes back.

```sh
h ws create NAME -C DIR [-d DESCRIPTION]   # DIR becomes the first root
h ws rename WORKSTREAM NEW-NAME
h ws reorder WORKSTREAM --up | --down      # display order in the dashboard
h ws archive WORKSTREAM --yes              # ask the user first
```

Archiving keeps every session record and moves its members to Ungrouped. It is
not a delete, but it is not reversible through the CLI either.

## Roots

A root is a directory a workstream may launch agents into. Registering one never
touches the filesystem, and editing roots never rewrites the root recorded on
sessions that already launched.

```sh
h ws root add WORKSTREAM DIR
h ws root set WORKSTREAM CURRENT-DIR NEW-DIR
h ws root rm  WORKSTREAM DIR
```

A workstream always keeps at least one root; removing the last one is refused.

## Sessions

```sh
h spawn -r claude -C DIR -w WORKSTREAM "task"    # confirm with the user first
h spawn -r codex  -C DIR "task"
h spawn -r no-agent -C DIR "scratch shell"
h send SESSION "follow-up message"
h title SESSION "A durable name"
h title SESSION --clear
h move SESSION --workstream WORKSTREAM
h move SESSION --ungrouped
h adopt SESSION [-w WORKSTREAM]                  # claim an orphaned tmux pane
h stop SESSION                                   # confirm with the user first
h delete SESSION --yes                           # confirm with the user first
h peek SESSION [--lines N]                       # current frame, not history
h attach SESSION                                 # hands over the terminal
```

`-C` must be a root registered on the workstream when `-w` is given; otherwise
the launch is refused. That is deliberate — membership never implicitly adds a
root. If the directory should be usable, add it with `h ws root add` first and
say so.

`h send` delivers literal UTF-8 into the pane. It is refused for a dead pane, a
pane in tmux copy mode, and a pane with input disabled.

Do not run `h attach` yourself. It takes over the terminal and is a thing the
human does.

## Notes and artifacts

Each workstream owns a directory, reported as `artifact_dir`. Write `notes.md`
there with ordinary file edits. The dashboard previews it, and the user refreshes
that preview with `R` in the organizer.

Notes are not state mutations. Do not route them through `h`.

## Things that are refused, and why

| Attempt | Result |
| --- | --- |
| launching into a directory not registered on the target workstream | refused; add the root first |
| removing a workstream's only root | refused; edit it instead |
| deleting a session whose tmux pane still exists | refused; stop it first, and ask the user before doing so |
| deleting a record bound to a different tmux socket | refused; it names the socket to retry with |
| sending to a dead pane, or one in copy mode | refused with the reason |
| creating a second active workstream with an existing name | refused |

These messages are specific. Show them to the user rather than paraphrasing.

## Never

- edit `state.json`, its lock files, or anything under `workstreams/` other than
  ordinary notes and artifacts
- pass `--yes` without being asked to
- run `h spawn` or `h stop` without confirming the specifics first
- describe a session as working, ready, blocked, or needing input
- present `h peek` output as a record of what a session did
