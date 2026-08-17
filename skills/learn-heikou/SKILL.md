---
name: learn-heikou
description: Guide a new Heikou user through h quickstart, installation checks, the dashboard, workstreams, persistent notes, native agent sessions, attach and detach, follow-ups, and safe shutdown. Use when someone is setting up Heikou, asks how Heikou works, wants a first-session walkthrough, or is unfamiliar with the dashboard's organize chords or tmux controls.
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
3. Explain that workstreams persist across dashboard restarts and collect
   related sessions and context, and that everything is organized on the
   dashboard itself — there is no separate view to open. If the list or the
   lower pane needs more room, press `Ctrl-G`, resize with `Up` or `Down`, then
   press `Esc`.
4. Press `Ctrl-N`, type a short workstream name, and press `Enter`. The current
   project directory becomes its first root, and the new workstream becomes the
   selection, so the composer is already aimed at it.
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
9. Select the named workstream. Its `notes.md` and artifact tree fill the pane
   below the list. Record decisions, useful commands, and next steps in that
   file — it persists independently of any one agent session. Most users let an
   agent write it; the path is shown under `FILES`. Press `F3` to re-read it
   after an agent or editor changes it while the cursor sat still.
10. Select a durable session and press `Ctrl-R` to give it a concise title.
    Explain that saving an empty title clears it, and that titles never rename
    the native provider conversation or tmux runtime.

If this guide itself is running inside a Heikou session, start at step 7. Ask
the user to detach, send `I made it back` with `Space` then `Enter`, and
reattach with `Esc` followed by `Enter`.
Then help them organize the guided session: detach again, select it under
Ungrouped, and press `Ctrl-T` to mark it — a `◆` appears on its row and stays
there while the cursor moves. Press `Ctrl-N` to create a named workstream; it
becomes the selection once it lands. Press `Ctrl-T` again to move the marked
session into it.

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

Clarify two things that surprise people. Organize keys are chords rather than
bare letters because every printable key belongs to the composer, so `Ctrl-R`
renames and a typed `r` is just text. And each chord reads the selected row for
its noun: `Ctrl-R` on a workstream renames it, `Ctrl-R` on a session titles it.

Also mention that `Esc` never quits — it steps back through reply, composer
text, and move mark, then parks on Ungrouped. Quitting is `Ctrl-C`.

## Keep this rescue card available

| Goal | Action |
| --- | --- |
| Open help and the noun glossary | `F1`, or `?` with an empty composer |
| Open settings | `Ctrl-S` or `F2` |
| See a workstream's notes and files | Select it; they fill the pane below the list |
| Cross a long list | `Option-Up` / `Option-Down` jump to the previous or next workstream, passing over the sessions between |
| Navigate with the mouse | Off by default; set `"mouse": true` in settings, then a click or the wheel moves the selection. Costs plain-drag text selection on the dashboard — `Shift`-drag, or `Option` in iTerm2, restores it |
| Re-read everything from disk | `F3` |
| Resize the lower pane | `Ctrl-G`, then `Up` / `Down`; `r` resets and `Esc` exits |
| Create a workstream | `Ctrl-N`, type a name, then `Enter` |
| Rename a workstream | Select it, press `Ctrl-R`, then `Enter` |
| Title or clear a durable session | Select it, press `Ctrl-R`, then save a title or an empty value |
| Move a session into a workstream | `Ctrl-T` on the session, select the workstream, `Ctrl-T` again |
| Reorder a named workstream | Select it, then `Shift-Up` / `Shift-Down` |
| Archive a workstream | Select it and press `Ctrl-V` twice; the first press says what happens, its sessions move to Ungrouped and keep running, and any other key cancels |
| Add, edit, or remove a root | Select the workstream, press `Ctrl-O` to open its roots; `Ctrl-O` again walks to the next one and then to an empty slot that adds. `Enter` saves; an empty draft removes after one more `Enter` |
| Start a session | Type a task, then `Enter` |
| Send to the selected live session | `Space` on an empty composer, type a message, then `Enter` |
| Leave a reply and compose a new session | `Esc`; the draft goes with it |
| Attach to a selected session | `Enter` with an empty composer, when not replying |
| Detach back to Heikou | `Ctrl-b`, release, then `d`; or `Ctrl-\` |
| Copy text out of an attached session | Drag to use tmux's selection, which reaches the system clipboard but stops at the pane; hold `Shift` (`Option` in iTerm2) for the terminal's own selection |
| Leave the dashboard without stopping agents | `Ctrl-C`, or `Esc` with an empty composer |
| Stop a runtime but keep its record | Select it and press `Ctrl-X` twice |

Mention the CLI equivalents when useful: `h quickstart`, `h list`,
`h spawn -r RUNNER -C DIR -w WORKSTREAM LABEL`, `h send ID MESSAGE`,
`h attach ID`, `h stop ID`, and `h help`. Add `--json` to `h list`, `h spawn`,
or `h send` when a machine-readable result is useful.

Do not stop, delete, archive, or move the user's sessions without explicit
confirmation. Point to `h help` for the complete current key map if behavior
differs from this guide because composer bindings can be configured.
