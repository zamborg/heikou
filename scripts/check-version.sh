#!/bin/sh
# Check that the version Heikou reports agrees with the tag people installed.
#
# Installation is `go install …@latest`, which resolves to the newest release
# tag. Nothing in the repository has to be edited to point at a new release, so
# the install command cannot go stale. What can still go wrong is tagging
# without bumping: v0.4.0 then ships a binary whose `h --version` says 0.3.6,
# and there is no way to tell from the outside which number is wrong.
#
# Usage:
#   scripts/check-version.sh            check the source alone (make check, CI)
#   scripts/check-version.sh v0.4.0     also require the tag to match
set -eu

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"

source_version="$(
	sed -n 's/^var version = "\([^"]*\)"$/\1/p' "$root/cmd/h/main.go"
)"
if [ -z "$source_version" ]; then
	echo "check-version: could not read the version from cmd/h/main.go" >&2
	echo "check-version: it is matched literally, so a change to that line needs one here too" >&2
	exit 1
fi

# A version that is not semver breaks `go install module@vX.Y.Z` for everyone,
# and the failure appears on the user's machine rather than in this repository.
if ! printf '%s' "$source_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
	echo "check-version: version $source_version in cmd/h/main.go is not semver" >&2
	exit 1
fi

# The install command is only self-maintaining while it says @latest. A pinned
# version would need editing on every release, which is the maintenance this
# arrangement exists to avoid.
if ! grep -qF 'go install github.com/zamborg/heikou/cmd/h@latest' "$root/README.md"; then
	echo "check-version: README no longer installs cmd/h@latest" >&2
	echo "check-version: a pinned install command goes stale on the next release" >&2
	exit 1
fi

if [ "$#" -eq 0 ]; then
	echo "check-version: source reports $source_version and the README installs @latest"
	exit 0
fi

tag="$1"
tag_version="${tag#v}"
if [ "$tag_version" != "$source_version" ]; then
	echo "check-version: tag $tag does not match version $source_version in cmd/h/main.go" >&2
	echo "check-version: bump the source first, then tag the commit that carries it" >&2
	exit 1
fi

echo "check-version: tag $tag matches the version the binary reports"
