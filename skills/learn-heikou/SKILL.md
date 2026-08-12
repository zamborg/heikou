---
name: learn-heikou
description: Guide a new Heikou user through h quickstart, installation checks, the dashboard, workstreams, persistent notes, native agent sessions, attach and detach, follow-ups, and safe shutdown. Use when someone is setting up Heikou, asks how Heikou works, wants a first-session walkthrough, or is unfamiliar with the F3 organizer or tmux controls.
---

# Learn Heikou

Act as a concise, interactive guide. Teach one action at a time, wait for the
user to try it, then explain what changed. Do not dump the entire manual at
once.

## Start with the mental model

Explain this loop in plain language:

`project root -> workstream -> session -> attach/detach -> follow up -> record notes`

- A **workstream** is a durable named group for roots, sessions, persistent
  notes, and artifacts. It organizes work; it is not an autonomous manager.
- A **session** is the durable record of one Codex, Claude, or shell launch.
- A **runtime** is the tmux pane currently backing a session.
- The **composer** is the input at the bottom of the dashboard.
- Leaving Heikou or detaching from a session does not stop its process.

## Guide the first run

1. Run or ask the user to run `h doctor`. Resolve missing required tools before
   opening the dashboard.
2. Have the user change to the project they want agents to edit and run `h`.
   The launch root shown in the composer is the directory a new session uses.
3. Press `F3` to open the **Workstream Organizer**. Explain that workstreams
   persist across dashboard restarts and collect related sessions and context.
   If the session tree or lower notes/files pane needs more room, press
   `Ctrl-G`, resize it with `Up` or `Down`, then press `Esc`.
4. Press `n`, enter a short workstream name, and press `Enter`. The current
   project directory becomes its first root. Press `u` or `Space` on that
   workstream to use it and return to the dashboard.
5. Type a small task in the composer and press `Enter` to start a session.
   With an empty composer, `Tab` cycles Codex, Claude, and `no-agent` before the
   launch.
6. Select the session and press `Enter` to attach to its native terminal.
7. Immediately practice detaching: press `Ctrl-b`, release both keys, then
   press `d`. This is a sequence, not one simultaneous chord. `Ctrl-\` is the
   one-chord alternative. The agent keeps running.
8. With the live session selected, press `Space` on the empty composer. The
   prefix changes to `↳ reply …`, naming the session the draft will reach.
   Type a follow-up and press `Enter` to send it. Point out that `Enter` sent
   to that session rather than starting a new one purely because the prefix
   said so, and that the target was fixed when `Space` was pressed, so moving
   the selection mid-draft cannot redirect it. Press `Esc` to leave reply mode,
   then `Enter` with an empty composer to attach again.
9. Open `F3`, select the named workstream, and press `e` to edit its `notes.md`.
   Record decisions, useful commands, and next steps there. These notes persist
   independently of any one agent session and appear in the organizer's lower
   context pane. Press `R` after an external editor or agent changes those
   files to refresh the preview.
10. Select a durable session in `F3` and press `r` to give it a concise title.
    Explain that saving an empty title clears it, and that titles never rename
    the native provider conversation or tmux runtime.

If this guide itself is running inside a Heikou session, start at step 7. Ask
the user to detach, send `I made it back` with `Space` then `Enter`, and
reattach with `Esc` followed by `Enter`.
Then help them organize the guided session: detach again and open `F3`. The
guided session is already marked as the move source. Only if that marker is
absent, select the session under Ungrouped and press `m` before continuing.
Press `n`, create a named workstream, and press `Enter` on the newly selected
workstream to move the session there. Press `e` on the workstream to record the
first persistent note.

## Reinforce the working pattern

Recommend this default rhythm:

1. Select or create a workstream for the outcome being pursued.
2. Confirm the composer shows the intended workstream, runner, and root.
3. Read the composer prefix before committing: it names the destination, and
   `Enter` always goes there. `Space` on an empty composer aims it at the
   selected session; `Esc` returns it to starting a new one.
4. Detach instead of terminating when switching between sessions.
5. Put durable context in workstream notes rather than relying on terminal
   scrollback or an agent's memory.
6. Stop a runtime only when finished; keep or delete its durable record
   intentionally.

Clarify the organizer's most surprising behavior: `Enter` on a session inside
`F3` marks it for moving; it does not attach. Press `u` or `Space` to return to
the dashboard with that session selected, then press `Enter` to attach.

## Keep this rescue card available

| Goal | Action |
| --- | --- |
| Open help and the noun glossary | `F1`, or `?` with an empty composer |
| Open settings | `Ctrl-S` or `F2` |
| Open workstreams and persistent notes | `F3` |
| Resize snapshot or notes/files | `Ctrl-G`, then `Up` / `Down`; `r` resets and `Esc` exits |
| Reorder a named workstream | In `F3`, press `Shift-Up` / `Shift-Down` |
| Title or clear a durable session | Select it in `F3`, press `r`, then save a title or an empty value |
| Refresh notes and artifacts | Select their workstream in `F3` and press `R` |
| Start a session | Type a task, then `Enter` |
| Send to the selected live session | `Space` on an empty composer, type a message, then `Enter` |
| Leave a reply and compose a new session | `Esc` (twice if the draft is non-empty) |
| Attach to a selected session | `Enter` with an empty composer, when not replying |
| Detach back to Heikou | `Ctrl-b`, release, then `d`; or `Ctrl-\` |
| Leave the dashboard without stopping agents | `Ctrl-C`, or `Esc` with an empty composer |
| Stop a runtime but keep its record | Select it and press `Ctrl-X` twice |

Mention the CLI equivalents when useful: `h quickstart`, `h list`,
`h spawn -r RUNNER -C DIR -w WORKSTREAM LABEL`, `h send ID MESSAGE`,
`h attach ID`, `h stop ID`, and `h help`. Add `--json` to `h list`, `h spawn`,
or `h send` when a machine-readable result is useful.

Do not stop, delete, archive, or move the user's sessions without explicit
confirmation. Point to `h help` for the complete current key map if behavior
differs from this guide because composer bindings can be configured.
