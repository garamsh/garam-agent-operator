#!/bin/sh
# Accepts the command line this operator gives sherlock's agent, refuses one
# without the agent ID sherlock refuses to start without, and then waits.
# Given no arguments at all it stands in for the workspace instead, which this
# operator starts with no command, and only waits.
set -eu

if [ "$#" -eq 0 ]; then
  echo "agent-stand-in: serving a workspace"
  trap 'exit 0' TERM INT
  while :; do
    sleep 3600 &
    wait $!
  done
fi

if [ "${1:-}" != agent ]; then
  echo "agent-stand-in: expected the agent subcommand, got: $*" >&2
  exit 64
fi
shift

agent_id=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --agent-id) agent_id=${2:-}; shift 2 ;;
    --ego-file | --assignment-epoch) shift 2 ;;
    *) echo "agent-stand-in: unknown argument: $1" >&2; exit 64 ;;
  esac
done

if [ -z "$agent_id" ]; then
  echo "agent-stand-in: --agent-id is required" >&2
  exit 64
fi

echo "agent-stand-in: serving $agent_id"
trap 'exit 0' TERM INT
while :; do
  sleep 3600 &
  wait $!
done
