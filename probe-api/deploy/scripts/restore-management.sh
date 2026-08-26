#!/usr/bin/env bash

# Root-only management restore coordinator.  It keeps the deployment lock,
# quiesces the API and backup timer, delegates archive validation/restoration
# to the unprivileged restore script, reapplies forward migrations, and only
# then returns the production services to their previous enablement state.

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
Usage: restore-management.sh --confirm-database DATABASE /absolute/path/to/probe-TIMESTAMP.dump

The archive must be a managed daily or weekly backup. On a restore or
post-restore migration failure, the API and backup timer remain stopped and
disabled for explicit operator recovery.
EOF
}

if (($# == 1)) && [[ "$1" == -h || "$1" == --help || "$1" == help ]]; then
    usage
    exit 0
fi
if (($# != 3)) || [[ "$1" != --confirm-database ]]; then
    usage >&2
    exit 2
fi

CONFIRMED_DATABASE="$2"
ARCHIVE_PATH="$3"
[[ "$CONFIRMED_DATABASE" =~ ^[A-Za-z_][A-Za-z0-9_.-]*$ ]] ||
    die 'database confirmation must be a plain database name'
[[ "$ARCHIVE_PATH" == /* && "$ARCHIVE_PATH" != *$'\n'* && "$ARCHIVE_PATH" != *$'\r'* ]] ||
    die 'backup archive must be an absolute path without control characters'

require_root
require_supported_runtime_platform
require_commands bash flock sleep stat systemctl systemd-analyze
acquire_deployment_lock
validate_installed_management_host

require_restore_dependency_active() {
    local unit="$1" load_state active_state
    load_state="$(management_systemd_property "$unit" LoadState)" ||
        die "could not read LoadState for required restore dependency $unit"
    active_state="$(management_systemd_property "$unit" ActiveState)" ||
        die "could not read ActiveState for required restore dependency $unit"
    [[ "$load_state:$active_state" == loaded:active ]] ||
        die "restore dependency $unit must be loaded/active (found $load_state/$active_state)"
}

capture_restore_application_activity() {
    local unit="$1" load_state active_state
    load_state="$(management_systemd_property "$unit" LoadState)" ||
        die "could not read LoadState while capturing restore state for $unit"
    active_state="$(management_systemd_property "$unit" ActiveState)" ||
        die "could not read ActiveState while capturing restore state for $unit"
    [[ "$load_state" == loaded ]] ||
        die "restore requires a loaded $unit unit (found $load_state/$active_state)"
    case "$active_state" in
        active) printf '%s\n' 1 ;;
        inactive|failed) printf '%s\n' 0 ;;
        *) die "refusing restore while $unit is in unsupported state $load_state/$active_state" ;;
    esac
}

# Database recovery must remain usable when the API is already inactive or
# failed after a forward migration. PostgreSQL and Nginx are still required to
# be stable dependencies; API/timer state is captured exactly and then the
# coordinator enters its own maintenance window.
require_restore_dependency_active "$(runtime_postgres_service)"
require_restore_dependency_active nginx.service
API_WAS_ACTIVE="$(capture_restore_application_activity probe-api.service)"
BACKUP_TIMER_WAS_ACTIVE="$(capture_restore_application_activity probe-postgres-backup.timer)"

API_ENABLED="$(systemctl is-enabled probe-api.service 2>/dev/null || :)"
BACKUP_TIMER_ENABLED="$(systemctl is-enabled probe-postgres-backup.timer 2>/dev/null || :)"
RESTORE_DEACTIVATED=0
RESTORE_STARTED=0

validate_enablement_snapshot() {
    local unit="$1" state="$2"
    case "$state" in
        enabled|enabled-runtime|disabled) ;;
        *) die "refusing restore with unsupported enablement state for $unit: ${state:-empty}" ;;
    esac
}

restore_enablement() {
    local unit="$1" state="$2"
    case "$state" in
        enabled) systemctl enable "$unit" >/dev/null ;;
        enabled-runtime) systemctl enable --runtime "$unit" >/dev/null ;;
        disabled) systemctl disable "$unit" >/dev/null ;;
        *) die "cannot restore unknown enablement state for $unit: $state" ;;
    esac
}

require_loaded_management_unit_inactive() {
    local unit="$1" load_state active_state
    load_state="$(management_systemd_property "$unit" LoadState)" ||
        die "could not read LoadState while confirming $unit is inactive"
    active_state="$(management_systemd_property "$unit" ActiveState)" ||
        die "could not read ActiveState while confirming $unit is inactive"
    [[ "$load_state:$active_state" == loaded:inactive ]] ||
        die "$unit did not reach the required loaded/inactive state (found $load_state/$active_state)"
}

restore_coordinator_exit() {
    local status=$? rollback_failed=0
    trap - EXIT
    trap '' HUP INT TERM
    if (( status != 0 && RESTORE_DEACTIVATED == 1 )); then
        set +e
        if (( RESTORE_STARTED == 0 )); then
            if ! restore_enablement probe-api.service "$API_ENABLED"; then rollback_failed=1; fi
            if ! restore_enablement probe-postgres-backup.timer "$BACKUP_TIMER_ENABLED"; then rollback_failed=1; fi
            if [[ "$API_WAS_ACTIVE" == 1 ]]; then
                if ! systemctl start probe-api.service >/dev/null 2>&1; then rollback_failed=1; fi
            fi
            if [[ "$BACKUP_TIMER_WAS_ACTIVE" == 1 ]]; then
                if ! systemctl start probe-postgres-backup.timer >/dev/null 2>&1; then rollback_failed=1; fi
            fi
            if (( rollback_failed == 0 )); then
                warn 'restore stopped before database mutation; prior service state was restored'
            else
                warn 'restore stopped before database mutation, but prior service state was not completely restored'
            fi
        else
            if ! systemctl stop probe-api.service probe-postgres-backup.timer >/dev/null 2>&1; then rollback_failed=1; fi
            if ! systemctl disable probe-api.service probe-postgres-backup.timer >/dev/null 2>&1; then rollback_failed=1; fi
            if (( rollback_failed == 0 )); then
                warn 'restore or post-restore migration failed; probe-api and the backup timer remain stopped and disabled'
            else
                warn 'restore or migration failed and the maintenance state could not be completely enforced; inspect both units immediately'
            fi
        fi
    fi
    exit "$status"
}
trap restore_coordinator_exit EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

validate_enablement_snapshot probe-api.service "$API_ENABLED"
validate_enablement_snapshot probe-postgres-backup.timer "$BACKUP_TIMER_ENABLED"

# Disable first so a reboot cannot restart a partially restored application.
RESTORE_DEACTIVATED=1
systemctl disable probe-api.service probe-postgres-backup.timer >/dev/null
systemctl stop probe-postgres-backup.timer

backup_quiesced=0
for _ in {0..60}; do
    backup_load_state="$(management_systemd_property probe-postgres-backup.service LoadState)" ||
        die 'could not read the PostgreSQL backup service LoadState'
    backup_active_state="$(management_systemd_property probe-postgres-backup.service ActiveState)" ||
        die 'could not read the PostgreSQL backup service ActiveState'
    case "$backup_load_state:$backup_active_state" in
        loaded:inactive)
            backup_quiesced=1
            break
            ;;
        loaded:active|loaded:activating|loaded:deactivating|loaded:reloading)
            if (( _ < 60 )); then
                sleep 1
            fi
            ;;
        *)
            die "refusing restore while probe-postgres-backup.service has unsupported state $backup_load_state/$backup_active_state"
            ;;
    esac
done
(( backup_quiesced == 1 )) ||
    die 'the current PostgreSQL backup did not finish within 60 seconds'
systemctl stop probe-api.service
require_loaded_management_unit_inactive probe-api.service

RESTORE_STARTED=1
run_as_probe_api_no_environment /usr/bin/env \
    PGHOST="$PROBE_VALIDATED_PGHOST" \
    PGPORT="$PROBE_VALIDATED_PGPORT" \
    PGDATABASE="$PROBE_VALIDATED_PGDATABASE" \
    PGUSER="$PROBE_VALIDATED_PGUSER" \
    PGPASSFILE="$PROBE_VALIDATED_PGPASSFILE" \
    PROBE_POSTGRES_BACKUP_DIR="$PROBE_POSTGRES_BACKUP_DIR" \
    "$PROBE_BACKUP_SCRIPT_DIR/restore-postgres.sh" \
    --confirm-database "$CONFIRMED_DATABASE" "$ARCHIVE_PATH"

# The selected immutable API owns the forward-only migration contract.  A
# migration failure intentionally leaves production disabled for inspection.
run_migrations "$PROBE_API_DIR/probe-api"
restore_enablement probe-api.service "$API_ENABLED"
restore_enablement probe-postgres-backup.timer "$BACKUP_TIMER_ENABLED"
systemctl start probe-api.service
systemctl start probe-postgres-backup.timer
verify_running_services management

trap - EXIT HUP INT TERM
printf 'Management PostgreSQL restore completed and runtime validation passed: %s\n' "$ARCHIVE_PATH"
