#!/bin/sh
# Runs the gaon operator's image and checks what a cluster relies on:
# it runs as a numeric account other than root, it ships no shell, and
# started with no environment it exits non-zero naming the settings it
# lacks rather than starting half-configured. The subject is the image
# `make operator-image` builds, which the operator's suites never
# open: they build the binary from this tree.
#
# No database or cluster is started: the operator's own suites hold
# its behaviour, and this holds what only the image can break.
#
# GAON_OPERATOR_IMAGE names the image, which
# `make operator-image-check` takes from `make operator-image`.
set -eu

image=${GAON_OPERATOR_IMAGE:?GAON_OPERATOR_IMAGE names the image to check}
passed=0

fail() {
	echo "operator-image-check: $1" >&2
	exit 1
}

user=$(docker image inspect -f '{{.Config.User}}' "$image")
case $user in
'' | root | 0 | 0:* | root:*) fail "the image runs as '${user:-root}', want a numeric account other than root" ;;
[1-9]*) ;;
*) fail "the image runs as '$user', want a numeric account other than root" ;;
esac
passed=$((passed + 1))

if docker run --rm --entrypoint sh "$image" -c true >/dev/null 2>&1; then
	fail "the image ships a shell"
fi
passed=$((passed + 1))

status=0
out=$(docker run --rm --name "operator-image-check-$$" "$image" 2>&1) || status=$?
[ "$status" -ne 0 ] || fail "started with no environment, the operator exited 0: $out"
case $out in
*"missing settings"*) ;;
*) fail "started with no environment, the operator did not name its missing settings: $out" ;;
esac
passed=$((passed + 1))

echo "operator-image-check: $passed checks passed against $image"
