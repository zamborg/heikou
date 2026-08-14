# Developing Heikou

Read this before pushing. It is short, and one part of it is not obvious.

## You never build anything

There is no build step to run before pushing, and nothing compiled is committed.

Users install with `go install github.com/zamborg/heikou/cmd/h@latest`, which
compiles from source **on their machine**. The repository holds no binaries, no
generated code, and no vendored dependencies — `git grep go:generate` returns
nothing, deliberately.

That includes the agent instructions. `skills/manage-heikou/AGENTS.md`,
`skills/manage-heikou/SKILL.md`, and `skills/learn-heikou/SKILL.md` reach the
binary through `//go:embed`, which reads them **at compile time**. Editing the
Markdown is the whole change; there is nothing to regenerate.

So: change `quickstartPrompt`, change a help string, change a skill file — none
of them need a build.

## What does gate a change: the tag

This is the part that surprises people.

`@latest` resolves to the newest **git tag**, not to `main`. A change merged to
`main` without a new tag reaches nobody, and nothing looks broken while that is
true: the code is on `main`, the install command still works, and it still
installs the previous release.

So there is exactly one thing to remember:

> **If a change should reach users, bump `version` in
> [`cmd/h/main.go`](../cmd/h/main.go) in the same pull request.**

Merging it is what ships. The `Tag` workflow watches `main`, and when CI passes
on a commit whose `version` has no tag yet, it creates and pushes that tag.
`@latest` then resolves to it. You do not tag by hand, and you do not build or
upload anything.

If you forget, nothing breaks — your change simply waits on `main` until the
next bump carries it along. The `Tag` run on `main` says so in its summary:
`main is 4 commit(s) ahead of v0.3.6`.

### Choosing the number

The version is `major.minor.patch`, and **which component moves is the owner's
call, not a rule to derive**. Zubin usually names it with the request — "this is
just a `0.7.1`" — and that number is the answer even when the change looks
larger from inside the diff.

When he does not name one, move the third component and say so in the pull
request. A minor bump claims the tool grew a capability worth being told about
and a major bump claims something changed out from under existing use; both are
claims about the product rather than about the code, so they are his to make and
cheap for him to correct. Heikou is pre-1.0 and the third component is doing
most of the work.

`make check` refuses a version that is not semver, because a version that is not
semver cannot be a tag and the failure would land on someone running
`go install` rather than here.

### Releasing by hand

You should not need to, but a hand-pushed `v*` tag still runs the `Release`
workflow, which checks the tag against `version` and builds it. That check can
only report a bad tag, not prevent one — the tag is public the moment it exists.
Prefer bumping and merging.

## Local commands

```sh
make check
```

That is the one to run before pushing. It is the same set CI runs, in the same
order, so a green `make check` should mean the pull request has nothing left to
discover:

| Target | What it does |
| --- | --- |
| `make check` | everything below, in CI's order |
| `make build` | build `bin/h` |
| `make install` | install `heikou` to `~/.local/bin` with `h` / `H` symlinks |
| `make fmt` | `go fmt ./...` |
| `make fmt-check` | fail if anything is unformatted |
| `make tidy-check` | fail if `go.mod` / `go.sum` are stale |
| `make vet` | `go vet ./...` |
| `make staticcheck` | staticcheck, pinned to a reviewed release |
| `make version-check` | version is semver, README still installs `@latest` |
| `make test` | `go test ./...` |
| `make race` | tests under the race detector, with tmux required |
| `make clean` | remove `bin/h` |

Two notes on the test suites:

- The tmux-dependent suites **skip themselves** when tmux is missing. Set
  `HEIKOU_TEST_REQUIRE_TMUX=1` to turn that skip into a failure — `make race`
  and CI both do — so a run cannot report green over a suite that never ran.
- `go build ./cmd/...` writes an `h` binary into the working directory rather
  than `bin/`. It is gitignored, because it reached a commit once.

## What CI does

On every pull request:

- `Verify` on **ubuntu** and **macOS**: gofmt, `go mod tidy`, vet, tests, race
  tests, build — with real tmux installed on both.
- `Cross-build` for `darwin/amd64` and `linux/arm64`, the platforms neither
  runner covers. Users compile locally, so a platform that stops building is a
  broken install for whoever is on it, with no artifact in between to fail first.
- `Staticcheck`, pinned rather than `@latest`, so a new release is a deliberate
  upgrade rather than a red build on a morning nobody touched the code.
- `Version agreement`: the version is semver and the README still installs
  `@latest`.

On `main`, after CI passes: `Tag`, described above.

## Where the shape of the module is written down

`internal/architecture` declares which package may import which, and which
concerns are allowed exactly one home. It is a test, so a new import that
contradicts the layering fails rather than being discovered during a later
refactor. Adding a package means placing it in that map on purpose.

[`docs/DESIGN.md`](DESIGN.md) explains why the boundaries are where they are,
and what each test layer is protecting.
