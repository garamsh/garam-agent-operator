#!/bin/sh
# gaon-build.sh CLONE COMMIT BINDIR: builds gaon's services and the
# gaonharness of this directory's testdata from COMMIT, in CLONE.
#
# CLONE is a fresh `git clone --no-hardlinks` of gaon made by the
# caller, so gaon's own checkout is neither changed nor checked out. The
# four services are built as gaon's Makefile builds them for
# `make e2e-extensions` (its GAON_SERVICES), and the local storage
# plugin with them. The harness is copied into the clone's tooling/e2e
# module, which already requires etcd's server library and mvmd's
# runtime API, and built there with its build tag.
set -eu

clone=$1
commit=$2
bindir=$3
here=$(cd "$(dirname "$0")" && pwd)

git -C "$clone" checkout --quiet --detach "$commit"
[ "$(git -C "$clone" rev-parse HEAD)" = "$commit" ] || {
	echo "gaon-build: the clone is not at $commit" >&2
	exit 1
}
mkdir -p "$bindir"
cd "$clone"
for service in gaon-apiserver:./apiserver/cmd/gaon-apiserver gaon-scheduler:./scheduler/cmd/gaon-scheduler \
	gaon-controllers:./controllers/cmd/gaon-controllers gaon-node-agent:./node-agent/cmd/gaon-node-agent \
	gaon-storage-local:./storage-local/cmd/gaon-storage-local; do
	CGO_ENABLED=0 go build -o "$bindir/${service%%:*}" "${service#*:}"
done
mkdir -p tooling/e2e/gaonharness1862
cp "$here/testdata/gaonharness/main.go" tooling/e2e/gaonharness1862/main.go
(cd tooling/e2e && go build -tags gaonharness -o "$bindir/gaonharness1862" ./gaonharness1862)
