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
- [ ] Treat a retained dead pane with missing `pane_dead_status` as “outcome
  unknown,” especially on tmux 3.3/3.4. Never default an empty status to zero or
  persist `OutcomeExited` until an exit code is actually observed.
- [ ] Replace parallel screen booleans, string edit/action modes, and independent
  confirmation fields with typed screen-specific UI state.
- [ ] Add an explicit state-migration function and versioned fixture tests before
  persisting session titles or any other new durable field.

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
  directory-synced.
- Settings use a strict JSON schema and argv arrays with private atomic creation.
- Artifact context reads are byte/entry/depth bounded, symlink-aware, and
  isolated from registered repository roots.
- The long-lived tmux server refreshes removed credentials, with a regression
  test proving stale values do not leak into later sessions.

## Ordered cleanup

1. Add durable redacted diagnostics and typed tmux errors.
2. Make malformed Heikou pane metadata visible in the normal projection.
3. Introduce typed screen, confirmation, edit, and action state; then split the
   large UI file by screen without building a generic framework.
4. Add state migration before the first new durable title/status field.
5. Expand `h doctor` around the real state, lock, tmux, and log boundaries.
6. Add explicit payload limits or a large-prompt transport.
7. Reconcile failed workstream artifact-directory creation.
