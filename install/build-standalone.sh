#!/usr/bin/env bash

# Deterministically assemble the public curl|bash bootstrap from reviewed
# source modules. The generated root install.sh remains a single parse unit so
# a truncated download cannot execute a partially received platform adapter.

set -Eeuo pipefail
umask 077
export LC_ALL=C

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(readlink -f -- "$SCRIPT_DIR/..")"
OUTPUT="$ROOT_DIR/install.sh"
MODE='write'
readonly ENTRYPOINT="if :; then main \"\$@\" 'probe-panel-bootstrap-complete-v1'; fi"

usage() {
    cat <<'EOF'
Usage: build-standalone.sh [--check]

Without options, atomically regenerates the repository root install.sh.
--check verifies that the committed standalone installer is byte-for-byte
identical to the deterministic output without changing it.
EOF
}

die() {
    printf '[probe-bootstrap-build] ERROR: %s\n' "$*" >&2
    exit 1
}

validate_definition_only_adapter() {
    local adapter_file="$1"
    awk '
        BEGIN { in_function = 0; invalid = 0 }
        in_function {
            if ($0 == "}") {
                in_function = 0
            }
            next
        }
        /^[ \t]*$/ { next }
        /^[ \t]*#/ { next }
        /^[a-z][a-z0-9_]*_platform_[a-z0-9_]+\(\)[ \t]*\{.*\}[ \t]*$/ { next }
        /^[a-z][a-z0-9_]*_platform_[a-z0-9_]+\(\)[ \t]*\{[ \t]*$/ {
            in_function = 1
            next
        }
        { invalid = 1 }
        END { exit (invalid || in_function) }
    ' "$adapter_file" ||
        die "platform adapter must contain function definitions only: $adapter_file"
}

case "${1-}" in
    '') ;;
    --check) MODE='check' ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
esac
(($# <= 1)) || die 'unexpected positional arguments'

COMMON="$SCRIPT_DIR/common.sh"
DEBIAN="$SCRIPT_DIR/platforms/debian.sh"
UBUNTU="$SCRIPT_DIR/platforms/ubuntu.sh"
CENTOS="$SCRIPT_DIR/platforms/centos.sh"
for source_file in "$COMMON" "$DEBIAN" "$UBUNTU" "$CENTOS"; do
    [[ -f "$source_file" && ! -L "$source_file" ]] ||
        die "required standalone source is missing or unsafe: $source_file"
    bash -n "$source_file" || die "standalone source has invalid syntax: $source_file"
done
for adapter_file in "$DEBIAN" "$UBUNTU" "$CENTOS"; do
    validate_definition_only_adapter "$adapter_file"
done

[[ "$(awk 'NF { line=$0 } END { print line }' "$COMMON")" != "$ENTRYPOINT" ]] ||
    die 'common source must not invoke the installer entrypoint'

TEMP_OUTPUT="$(mktemp "$ROOT_DIR/.install.sh.generated.XXXXXX")"
cleanup() {
    local status=$?
    trap - EXIT HUP INT TERM
    rm -f -- "$TEMP_OUTPUT"
    exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

{
    cat "$COMMON"
    printf '\n# --- BEGIN GENERATED PLATFORM ADAPTER: debian ---\n'
    cat "$DEBIAN"
    printf '\n# --- END GENERATED PLATFORM ADAPTER: debian ---\n'
    printf '\n# --- BEGIN GENERATED PLATFORM ADAPTER: ubuntu ---\n'
    cat "$UBUNTU"
    printf '\n# --- END GENERATED PLATFORM ADAPTER: ubuntu ---\n'
    printf '\n# --- BEGIN GENERATED PLATFORM ADAPTER: centos ---\n'
    cat "$CENTOS"
    printf '\n# --- END GENERATED PLATFORM ADAPTER: centos ---\n\n'
    printf '%s\n' "$ENTRYPOINT"
} > "$TEMP_OUTPUT"
chmod 0755 "$TEMP_OUTPUT"
bash -n "$TEMP_OUTPUT" || die 'generated standalone installer has invalid syntax'
[[ "$(awk 'NF { line=$0 } END { print line }' "$TEMP_OUTPUT")" == "$ENTRYPOINT" ]] ||
    die 'generated standalone installer lost its final parse barrier'

if [[ "$MODE" == check ]]; then
    [[ -f "$OUTPUT" && ! -L "$OUTPUT" ]] || die "standalone installer is missing: $OUTPUT"
    cmp -s -- "$TEMP_OUTPUT" "$OUTPUT" ||
        die 'root install.sh is stale; run install/build-standalone.sh and review the result'
else
    mv -f -- "$TEMP_OUTPUT" "$OUTPUT"
    TEMP_OUTPUT=''
    chmod 0755 "$OUTPUT"
fi
