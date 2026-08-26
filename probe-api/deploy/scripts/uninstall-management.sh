#!/usr/bin/env bash

# Ordinary management-product uninstall.  It removes active application links,
# Probe-owned units and the Nginx include while deliberately retaining release
# directories, configuration, TLS material, PostgreSQL data and backups.

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
Usage: uninstall-management.sh

Deactivate and uninstall the management application while preserving:
  /srv/probe/config, /etc/probe-panel, /var/lib/probe-panel,
  /var/backups/probe-panel, PostgreSQL databases and immutable release copies.

Purge is intentionally not implemented.
EOF
}

if (($# > 0)); then
    case "$1" in
        -h|--help|help) usage; exit 0 ;;
        *) usage >&2; exit 2 ;;
    esac
fi

require_root
require_supported_runtime_platform
require_commands bash flock mv nginx readlink rm stat systemctl systemd-analyze
acquire_deployment_lock
validate_installed_management_host
acquire_database_maintenance_lock

API_ENABLED="$(systemctl is-enabled probe-api.service 2>/dev/null || :)"
BACKUP_TIMER_ENABLED="$(systemctl is-enabled probe-postgres-backup.timer 2>/dev/null || :)"
capture_management_service_activity
API_ACTIVE="$MANAGEMENT_API_WAS_ACTIVE"
BACKUP_TIMER_ACTIVE="$MANAGEMENT_BACKUP_TIMER_WAS_ACTIVE"
NGINX_ACTIVE="$MANAGEMENT_NGINX_WAS_ACTIVE"

declare -a REMOVAL_ORIGINALS=()
declare -a REMOVAL_TEMPORARIES=()
UNINSTALL_COMMITTED=0

validate_enablement_snapshot() {
    local unit="$1" state="$2"
    case "$state" in
        enabled|enabled-runtime|disabled) ;;
        *) die "refusing uninstall with unsupported enablement state for $unit: ${state:-empty}" ;;
    esac
}

validate_enablement_snapshot probe-api.service "$API_ENABLED"
validate_enablement_snapshot probe-postgres-backup.timer "$BACKUP_TIMER_ENABLED"

require_management_unit_inactive_or_absent() {
    local unit="$1" load_state active_state
    load_state="$(management_systemd_property "$unit" LoadState)" ||
        die "could not read LoadState while confirming $unit is inactive"
    active_state="$(management_systemd_property "$unit" ActiveState)" ||
        die "could not read ActiveState while confirming $unit is inactive"
    case "$load_state:$active_state" in
        loaded:inactive|not-found:inactive) ;;
        *) die "$unit did not reach a proven inactive state (found $load_state/$active_state)" ;;
    esac
}

stage_removal() {
    local original="$1" temporary="${1}.probe-uninstall.$$"
    case "$original" in
        "$PROBE_NGINX_LINK"|\
        "$PROBE_API_DIR/probe-api"|\
        "$PROBE_ROOT/admin"|\
        "$PROBE_ROOT/migrations"|\
        "$PROBE_SYSTEMD_UNIT"|\
        "$PROBE_BACKUP_SERVICE_UNIT"|\
        "$PROBE_BACKUP_TIMER_UNIT"|\
        "$PROBE_BACKUP_SCRIPT_DIR/backup-postgres.sh"|\
        "$PROBE_BACKUP_SCRIPT_DIR/restore-postgres.sh") ;;
        *) die "refusing an unreviewed management uninstall target: $original" ;;
    esac
    [[ -e "$original" || -L "$original" ]] || die "uninstall target disappeared: $original"
    [[ ! -e "$temporary" && ! -L "$temporary" ]] ||
        die "uninstall staging path already exists: $temporary"
    # Journal both sides before the move. If a signal arrives before mv there
    # is no temporary to restore; after mv the rollback inventory is complete.
    REMOVAL_TEMPORARIES+=("$temporary")
    REMOVAL_ORIGINALS+=("$original")
    mv -T -- "$original" "$temporary"
}

restore_enablement() {
    local unit="$1" state="$2"
    case "$state" in
        enabled) systemctl enable "$unit" >/dev/null 2>&1 ;;
        enabled-runtime) systemctl enable --runtime "$unit" >/dev/null 2>&1 ;;
        disabled) systemctl disable "$unit" >/dev/null 2>&1 ;;
        *) warn "cannot restore unknown enablement state for $unit: ${state:-empty}"; return 1 ;;
    esac
}

rollback_uninstall() {
    local status=$? index rollback_failed=0
    trap - EXIT
    trap '' HUP INT TERM
    if (( status != 0 && UNINSTALL_COMMITTED == 0 )); then
        warn 'ordinary uninstall failed; restoring staged application paths and service state'
        set +e
        for ((index=${#REMOVAL_ORIGINALS[@]} - 1; index>=0; index--)); do
            if [[ -e "${REMOVAL_TEMPORARIES[index]}" || -L "${REMOVAL_TEMPORARIES[index]}" ]]; then
                if ! mv -Tf -- "${REMOVAL_TEMPORARIES[index]}" "${REMOVAL_ORIGINALS[index]}"; then
                    warn "could not restore uninstalled asset: ${REMOVAL_ORIGINALS[index]}"
                    rollback_failed=1
                fi
            fi
        done
        if ! systemctl daemon-reload >/dev/null 2>&1; then rollback_failed=1; fi
        if ! restore_enablement probe-api.service "$API_ENABLED"; then rollback_failed=1; fi
        if ! restore_enablement probe-postgres-backup.timer "$BACKUP_TIMER_ENABLED"; then rollback_failed=1; fi
        if [[ "$API_ACTIVE" == 1 ]]; then
            if ! systemctl start probe-api.service >/dev/null 2>&1; then rollback_failed=1; fi
        fi
        if [[ "$BACKUP_TIMER_ACTIVE" == 1 ]]; then
            if ! systemctl start probe-postgres-backup.timer >/dev/null 2>&1; then rollback_failed=1; fi
        fi
        if [[ "$NGINX_ACTIVE" == 1 ]]; then
            if nginx -t >/dev/null 2>&1; then
                if ! systemctl reload nginx.service >/dev/null 2>&1; then rollback_failed=1; fi
            else
                rollback_failed=1
            fi
        fi
        if (( rollback_failed != 0 )); then
            warn 'ordinary uninstall rollback was incomplete; inspect the retained Probe assets before retrying'
        fi
    fi
    exit "$status"
}
trap rollback_uninstall EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# The database-maintenance lock proves that a one-shot backup or restore is not
# running before its timer and scripts are deactivated.
systemctl stop probe-postgres-backup.timer probe-postgres-backup.service
systemctl stop probe-api.service
systemctl disable probe-postgres-backup.timer probe-api.service >/dev/null

stage_removal "$PROBE_NGINX_LINK"
stage_removal "$PROBE_API_DIR/probe-api"
stage_removal "$PROBE_ROOT/admin"
stage_removal "$PROBE_ROOT/migrations"
stage_removal "$PROBE_SYSTEMD_UNIT"
stage_removal "$PROBE_BACKUP_SERVICE_UNIT"
stage_removal "$PROBE_BACKUP_TIMER_UNIT"
stage_removal "$PROBE_BACKUP_SCRIPT_DIR/backup-postgres.sh"
stage_removal "$PROBE_BACKUP_SCRIPT_DIR/restore-postgres.sh"

systemctl daemon-reload
nginx -t
if [[ "$NGINX_ACTIVE" == 1 ]]; then
    systemctl reload nginx.service
fi
require_management_unit_inactive_or_absent probe-api.service
require_management_unit_inactive_or_absent probe-postgres-backup.timer

# The live activation is gone at this commit point.  Cleanup of rollback files
# is deliberately post-commit so a disk error or signal cannot trigger an
# impossible rollback after some originals have already been deleted.
UNINSTALL_COMMITTED=1
cleanup_failed=0
set +e
for temporary in "${REMOVAL_TEMPORARIES[@]}"; do
    rm -f -- "$temporary" || cleanup_failed=1
done
set -e
trap - EXIT HUP INT TERM
(( cleanup_failed == 0 )) ||
    die 'management uninstall committed, but one or more root-only rollback files require manual removal'

printf '%s\n' \
    'Probe Panel management application was uninstalled.' \
    'Preserved: configuration, TLS, setup state, PostgreSQL databases, backups,' \
    'the probe-api service account, shared Nginx/PostgreSQL packages and inactive release directories.'
