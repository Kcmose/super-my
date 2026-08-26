#!/bin/sh

# Backward-compatible entrypoint for the single authoritative SELinux
# candidate contract. Keep old CI callers from running stale policy claims.

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
exec bash "$SCRIPT_DIR/selinux-contract.sh"
