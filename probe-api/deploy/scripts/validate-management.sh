#!/usr/bin/env bash

# Read-only validation for an installed management-only Probe Panel product.
# This is packaged with the immutable management bundle and installed beside
# its generated runtime helper; it never builds source code on the target.

set -Eeuo pipefail
umask 077
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
probe_management_lifecycle_self_check() {
    local manifest="${SCRIPT_DIR}/management-lifecycle.sha256"
    local line lifecycle_name lifecycle_path checksum index=0
    local -a expected_names=(
        deploy-common.sh
        validate-management.sh
        restore-management.sh
        uninstall-management.sh
    )

    [[ "$SCRIPT_DIR" == /usr/local/lib/probe-panel ]] || return 1
    [[ -d "$SCRIPT_DIR" && ! -L "$SCRIPT_DIR" ]] || return 1
    [[ "$(stat -c '%u:%g:%a' -- "$SCRIPT_DIR")" == 0:0:755 ]] || return 1
    [[ -f "$manifest" && ! -L "$manifest" ]] || return 1
    [[ "$(stat -c '%u:%g:%a' -- "$manifest")" == 0:0:644 ]] || return 1
    while IFS= read -r line || [[ -n "$line" ]]; do
        (( index < ${#expected_names[@]} )) || return 1
        lifecycle_name="${expected_names[index]}"
        checksum="${line:0:64}"
        [[ "$checksum" =~ ^[0-9a-f]{64}$ &&
           "${line:64:2}" == "  " &&
           "${line:66}" == "$lifecycle_name" &&
           "${#line}" -eq $((66 + ${#lifecycle_name})) ]] || return 1
        lifecycle_path="${SCRIPT_DIR}/${lifecycle_name}"
        [[ -f "$lifecycle_path" && ! -L "$lifecycle_path" && -x "$lifecycle_path" ]] || return 1
        [[ "$(stat -c '%u:%g:%a' -- "$lifecycle_path")" == 0:0:755 ]] || return 1
        ((index += 1))
    done < "$manifest"
    (( index == ${#expected_names[@]} )) || return 1
    (cd -- "$SCRIPT_DIR" && sha256sum --check --strict --status -- "$manifest")
}
if ! probe_management_lifecycle_self_check; then
    printf '%s\n' 'management lifecycle integrity check failed before loading the shared runtime' >&2
    exit 70
fi
unset -f probe_management_lifecycle_self_check
# shellcheck source-path=SCRIPTDIR
# shellcheck source=deploy-common.sh
source "${SCRIPT_DIR}/deploy-common.sh"

usage() {
    cat <<'EOF'
Usage: validate-management.sh [host|runtime|all]

Read-only checks for the installed management product:
  host     configuration, release hashes, lifecycle tools, systemd and Nginx
  runtime  service health, TLS state and listener boundaries
  all      host and runtime checks (default)
EOF
}

MODE="${1:-all}"
if (($# > 1)); then
    usage >&2
    exit 2
fi
case "$MODE" in
    host|runtime|all) ;;
    -h|--help|help) usage; exit 0 ;;
    *) usage >&2; die 'validation mode must be host, runtime, or all' ;;
esac

require_root
require_supported_runtime_platform
require_commands bash curl flock grep nginx python3 readlink sed sha256sum ss stat \
    systemctl systemd-analyze
acquire_deployment_lock

case "$MODE" in
    host)
        validate_installed_management_host
        ;;
    runtime)
        clear_exported_probe_environment
        load_probe_env
        verify_running_services management
        log "installed management runtime is healthy on $RUNTIME_PLATFORM_ID"
        ;;
    all)
        validate_installed_management_host
        verify_running_services management
        log "installed management host and runtime checks passed on $RUNTIME_PLATFORM_ID"
        ;;
esac
