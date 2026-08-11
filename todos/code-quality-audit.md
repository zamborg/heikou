# Code-quality and reliability audit

This backlog captures the August 2026 audit of tmux/process execution,
configuration and filesystem safety, diagnostics, CLI packaging, and release
hygiene. The full test suite, vet, race detector, and real tmux integration
tests passed during the audit.

## Priorities

### P0 · release gate

- [ ] Automate the release check that source version, README install command, and
  tag agree. Until then, merge and pass CI before creating the advertised tag.
- [x] Add CI for gofmt, vet, normal tests, race tests, a build, and real tmux
  integration.
- [x] Make durable deletion fail closed when a matching tmux pane has malformed
  metadata, refuse deletion from a socket other than its durable binding, and
  retain an outcome-less pending launch whose socket was never recorded.
- [x] Fix organizer-context invalidation and make snapshot/preview polling
  generation-tagged and single-flight.

### P1 · operational reliability

- [ ] Add a redacted, rotating diagnostic log under the XDG state directory.
  Record operation, version, socket, session ID, duration, exit status, and
  cancellation cause; never record prompts, messages, argv payloads, or
  environment values.
- [ ] Preserve typed tmux command failures and surface every marker-bearing pane
  whose required metadata cannot be parsed instead of silently dropping it from
  the normal dashboard projection. Lifecycle deletion already uses a separate,
  metadata-independent existence check.
- [x] Treat a retained dead pane with missing `pane_dead_status` as “outcome
  unknown,” especially on tmux 3.3/3.4. Never default an empty status to zero or
  persist `OutcomeExited` until an exit code is actually observed.
- [x] Replace parallel screen booleans and string edit/action modes with typed
  primary-screen, overlay, and organizer-edit state. Dashboard and organizer now
  share one indexed overview relationship model. Independent lifecycle
  confirmation fields remain intentionally narrow and explicit.
- [x] Add an explicit ordered v1-to-v2 state migration and versioned fixture
  tests before persisting durable session titles. Schema-only migration does not
  advance the domain revision.
- [x] Route current human mutations through a closed typed actor/scope command
  plane. The active authorizer remains local-human-only; session actors stay
  denied until manager grants are designed.
- [x] Resolve native runner argv through a trusted config-backed controller
  resolver instead of accepting executable argv in command actions.
- [x] Add machine-readable `h list --json`, `h spawn --json`, and
  `h send --json` local CLI surfaces.
- [ ] Consolidate the remaining lifecycle confirmation fields into typed state
  only if that makes their transitions materially clearer.

### P2 · hardening

- [ ] Extend `h doctor` with bounded, non-destructive state validation,
  lifecycle-lock, tmux socket, permissions, and diagnostic-log checks.
- [ ] Bound prompt and follow-up payload sizes before durable storage or tmux
  transport; use a non-argv transport if large prompts become a requirement.
- [ ] Remove a newly created empty artifact directory when workstream state
  creation fails.
- [x] Route and test `h --version` before dashboard flag parsing.
- [x] Make selected organizer rows valid UTF-8 and width-safe down to one column.
- [x] Add deterministic tests for abandoned artifact reads, coalesced polling,
  stale snapshot/preview completion, and unavailable-session previews.

## Strengths to preserve

- Runner launch and follow-up delivery use argv, `exec`, and tmux buffers rather
  than shell interpolation; integration tests exercise shell-looking input.
- Session identity is durable before launch, reconciliation is conservative,
  and deletion refuses any live or retained tmux pane.
- State writes are validated, locked, private, atomic, file-synced, and
  directory-synced. Ordered schema migrations preserve domain revisions and
  reject invalid, future, or version-inaccurate JSON.
- Settings use a strict JSON schema and argv arrays with private atomic creation.
- Human mutations share a closed typed command boundary, and configured runner
  argv is supplied only by the trusted resolver.
- Artifact context reads are byte/entry/depth bounded, symlink-aware, and
  isolated from registered repository roots.
- Typed UI screen/edit state and one shared overview projection keep dashboard
  and organizer navigation consistent.
- The long-lived tmux server refreshes removed credentials, with a regression
  test proving stale values do not leak into later sessions.

## Ordered cleanup

1. Add durable redacted diagnostics and typed tmux errors.
2. Make malformed Heikou pane metadata visible in the normal projection.
3. Replace the remaining independent lifecycle confirmation fields only if a
   typed state makes those transitions materially clearer.
4. Split the large UI file further by screen without building a generic
   framework.
5. Expand `h doctor` around the real state, lock, tmux, and log boundaries.
6. Add explicit payload limits or a large-prompt transport.
7. Reconcile failed workstream artifact-directory creation.
