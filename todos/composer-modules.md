# Composable composer modules

Status: proposed; do not implement as part of V0 settings.

## The idea

The bottom input in Heikou is the **composer**. Today it has one commit key,
`Enter`, which delivers to whichever destination the composer is aimed at:
a new session by default, or a session pinned by pressing `Space` on an empty
composer. It could become a small host for built-in modules that recognize an
initial trigger, render contextual suggestions, and turn the composed value
into a typed action.

The reply key is already an instance of this shape: a trigger that only
activates at the start of a fresh composer, changes the prefix, and redirects
where `Commit` sends. A module system should subsume it rather than sit beside
it — `@a1b2 look again` below is the same action reached by typing instead of
by a mode.

Possible modules:

| Trigger | Example | Potential action |
| --- | --- | --- |
| plain text | `fix the flaky test` | Start an agent task, as today |
| `/` | `/work/project` | Search directories and set the launch root |
| `$` | `$review-pr` | Search named actions or reusable task recipes |
| `@` | `@a1b2 look again` | Target a session explicitly |

These triggers are sketches, not reserved syntax yet. In particular, `$` may
be natural task text or shell input, so a module must activate only at the
beginning of a fresh composer and remain easy to escape back to literal text.

## Small architecture

Keep modules compiled into Heikou and registered in order. This is an internal
UI extension point, not a runtime plugin system.

```go
type ComposerModule interface {
    Match(input string) bool
    Suggest(context.Context, ComposerContext, string) ([]Suggestion, error)
    Preview(ComposerContext, string) RenderFragment
    Commit(context.Context, ComposerContext, string) (Action, error)
}
```

`ComposerContext` should be a read-only snapshot: launch root, chosen runner,
selected session, workstream metadata when that exists, and terminal size.
`Action` should be data such as `StartTask`, `SendMessage`, `ChangeRoot`, or
`RunRecipe`; modules should not mutate tmux or global state directly.

The composer owns the state machine:

1. Insert text normally.
2. Select the first matching module.
3. Ask it for optional suggestions and a render fragment.
4. Let the user accept, dismiss, or keep typing.
5. Commit a typed action through the same controller used by keyboard commands.

This preserves one execution path for mouse/keyboard commands, module actions,
and future MCP or queue requests.

## Rendering contract

A module may influence only three bounded regions:

- the prompt/prefix at the start of the composer;
- an inline completion after the cursor; and
- a suggestion panel immediately above the composer.

It must not own the session list or terminal preview. Rendering must remain
width-bounded, ANSI-sanitized, and usable without color. Suggestions should be
cancellable with `Esc`; dismissing a module must preserve the typed text.

## Safety and semantics

- A trigger never implies shell evaluation.
- Recipe/action arguments remain structured argv or typed Heikou actions.
- Directory results must resolve to real directories before changing root.
- Modules cannot read terminal transcripts unless their interface explicitly
  receives a user-approved excerpt.
- Async suggestions carry a composer revision so stale results cannot replace
  newer input.
- Prefixes need a literal escape convention before any are stabilized.

## First experiment

Start with `/` as a directory switcher because it has a clear, reversible
effect and exercises matching, suggestions, preview, and commit without adding
another execution backend. Keep plain text as the fallback module. Do not add a
general shell/action module until named actions and their safety model are
defined.

Acceptance criteria for that experiment:

- typing `/` never changes the current launch root by itself;
- results appear quickly from recent roots plus bounded filesystem search;
- accepting a result updates the header and only future sessions;
- `Esc` returns the exact original composer text; and
- `Enter` still commits to the destination named in the prefix for all
  non-matching text, whether that is a new session or a pinned reply target.
