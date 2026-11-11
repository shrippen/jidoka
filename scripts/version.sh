#!/bin/sh
# Prints the build version for "About Jidoka": "0.5.0" on a release tag,
# otherwise "<last tag>/<branch>/#<build>", e.g. "0.5.0/main/#25".
# The build number counts the commits since the tag, so it restarts with
# every release. BRANCH overrides the branch (CI checks out a detached HEAD).
set -eu
cd "$(dirname "$0")/.."

tag=$(git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)
ver=${tag#v}

# Release: the tag alone.
if git describe --tags --exact-match >/dev/null 2>&1; then
	echo "$ver"
	exit 0
fi

branch=${BRANCH:-$(git rev-parse --abbrev-ref HEAD)}
build=$(git rev-list --count "$tag"..HEAD 2>/dev/null || echo 0)
echo "$ver/$branch/#$build"
