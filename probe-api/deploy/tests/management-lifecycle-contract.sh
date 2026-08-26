#!/bin/sh
# shellcheck disable=SC2016

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH='' cd -- "$SCRIPT_DIR/../../.." && pwd)
INSTALL_COMMON=$ROOT_DIR/install/common.sh
DEPLOY_COMMON=$ROOT_DIR/probe-api/deploy/scripts/deploy-common.sh
BUILDER=$ROOT_DIR/probe-api/deploy/scripts/build-release-bundles.sh
VALIDATOR=$ROOT_DIR/probe-api/deploy/scripts/validate-management.sh
RESTORE_COORDINATOR=$ROOT_DIR/probe-api/deploy/scripts/restore-management.sh
UNINSTALLER=$ROOT_DIR/probe-api/deploy/scripts/uninstall-management.sh
BACKUP=$ROOT_DIR/probe-api/deploy/scripts/backup-postgres.sh
RESTORE=$ROOT_DIR/probe-api/deploy/scripts/restore-postgres.sh

fail() {
    printf '%s\n' "management lifecycle contract: $*" >&2
    exit 1
}

assert_contains() {
    needle=$1
    file=$2
    grep -Fq -- "$needle" "$file" || fail "$file is missing required contract: $needle"
}

line_of() {
    needle=$1
    file=$2
    grep -Fn -- "$needle" "$file" | head -n 1 | cut -d: -f1
}

last_line_of() {
    needle=$1
    file=$2
    grep -Fn -- "$needle" "$file" | tail -n 1 | cut -d: -f1
}

line_after() {
    needle=$1
    after=$2
    file=$3
    grep -Fn -- "$needle" "$file" | cut -d: -f1 | awk -v after="$after" '$1 > after { print; exit }'
}

assert_before() {
    first=$1
    second=$2
    file=$3
    first_line=$(line_of "$first" "$file")
    second_line=$(line_of "$second" "$file")
    [ -n "$first_line" ] && [ -n "$second_line" ] && [ "$first_line" -lt "$second_line" ] ||
        fail "$file must order '$first' before '$second'"
}

for file in \
    "$INSTALL_COMMON" "$DEPLOY_COMMON" "$BUILDER" "$VALIDATOR" \
    "$RESTORE_COORDINATOR" "$UNINSTALLER" "$BACKUP" "$RESTORE"; do
    [ -f "$file" ] && [ ! -L "$file" ] || fail "missing regular lifecycle source: $file"
    bash -n "$file"
done
sh -n "$0"

# The public entry only dispatches management lifecycle commands. It never
# calls the historical full-source upgrade/validator.
for contract in \
    'Usage: ${PROGRAM} [install [--accept-eol]|upgrade [--accept-eol]|validate|status|uninstall]' \
    'upgrade) upgrade_action ;;' \
    'validate) validate_action ;;' \
    'uninstall) uninstall_action ;;' \
    '"$MANAGEMENT_VALIDATE_BINARY" host' \
    '"$install_release" --bundle-root "$bundle_root" --release-id "$PANEL_VERSION" --profile management' \
    '"$MANAGEMENT_VALIDATE_BINARY" all' \
    '"$MANAGEMENT_UNINSTALL_BINARY"'; do
    assert_contains "$contract" "$INSTALL_COMMON"
done
upgrade_block=$(sed -n '/^upgrade_action() {$/,/^uninstall_action() {$/p' "$INSTALL_COMMON")
if printf '%s\n' "$upgrade_block" | grep -Eiq 'probe-agent|probe-web|deploy/scripts/(upgrade|validate-production)[.]sh|--profile[[:space:]]+full'; then
    fail 'management upgrade dispatch contains historical full, Agent, or visitor logic'
fi

# The bundle installs a persistent validator, restore coordinator and ordinary
# uninstaller beside their exact generated ABI v2 runtime helper.
for lifecycle in validate-management.sh restore-management.sh uninstall-management.sh; do
    assert_contains "deploy/scripts/$lifecycle" "$BUILDER"
    assert_contains "source/probe-api/deploy/scripts/$lifecycle" "$INSTALL_COMMON"
    assert_contains "source/probe-api/deploy/scripts/$lifecycle" "$DEPLOY_COMMON"
done
assert_contains 'deploy-common.sh validate-management.sh restore-management.sh uninstall-management.sh' "$DEPLOY_COMMON"
assert_contains '"$PROBE_MANAGEMENT_RESTORE"' "$DEPLOY_COMMON"
assert_contains 'readonly PROBE_MANAGEMENT_LIFECYCLE_MANIFEST="${PROBE_MANAGEMENT_LIB_DIR}/management-lifecycle.sha256"' "$DEPLOY_COMMON"
assert_contains '[[ "$index" -eq 14 ]]' "$DEPLOY_COMMON"

# The four lifecycle programs form one committed ABI unit.  Each executable
# entrypoint verifies the root-owned, canonical four-entry manifest before it
# can source the shared helper.  Installation validates all staged files and
# atomically commits the manifest only after the helper is the final script
# replacement.
for lifecycle_entry in "$VALIDATOR" "$RESTORE_COORDINATOR" "$UNINSTALLER"; do
    for contract in \
        'probe_management_lifecycle_self_check()' \
        'local manifest="${SCRIPT_DIR}/management-lifecycle.sha256"' \
        '[[ "$(stat -c '\''%u:%g:%a'\'' -- "$manifest")" == 0:0:644 ]]' \
        'sha256sum --check --strict --status -- "$manifest"' \
        'if ! probe_management_lifecycle_self_check; then'; do
        assert_contains "$contract" "$lifecycle_entry"
    done
    self_check_line=$(line_of 'if ! probe_management_lifecycle_self_check; then' "$lifecycle_entry")
    source_line=$(line_of 'source "${SCRIPT_DIR}/deploy-common.sh"' "$lifecycle_entry")
    [ -n "$self_check_line" ] && [ -n "$source_line" ] && [ "$self_check_line" -lt "$source_line" ] ||
        fail "$lifecycle_entry must verify the complete lifecycle ABI before sourcing deploy-common.sh"
done
for contract in \
    'management_lifecycle_asset_names()' \
    'validate_management_lifecycle_manifest()' \
    'printf '\''%s  %s\n'\'' "$lifecycle_hash" "$lifecycle_name"' \
    'validate_management_lifecycle_manifest "$lifecycle_manifest_tmp" ".new.$$"' \
    'validate-management.sh restore-management.sh uninstall-management.sh deploy-common.sh; do' \
    'mv -Tf -- "$lifecycle_manifest_tmp" "$PROBE_MANAGEMENT_LIFECYCLE_MANIFEST"' \
    'sha256sum --check --strict --status -- "$PROBE_MANAGEMENT_LIFECYCLE_MANIFEST"'; do
    assert_contains "$contract" "$DEPLOY_COMMON"
done
lifecycle_stage_verify_line=$(line_of 'validate_management_lifecycle_manifest "$lifecycle_manifest_tmp" ".new.$$"' "$DEPLOY_COMMON")
lifecycle_script_commit_line=$(line_of 'mv -Tf -- "${lifecycle_destination}.new.$$" "$lifecycle_destination"' "$DEPLOY_COMMON")
lifecycle_manifest_commit_line=$(line_of 'mv -Tf -- "$lifecycle_manifest_tmp" "$PROBE_MANAGEMENT_LIFECYCLE_MANIFEST"' "$DEPLOY_COMMON")
[ -n "$lifecycle_stage_verify_line" ] && [ -n "$lifecycle_script_commit_line" ] &&
    [ -n "$lifecycle_manifest_commit_line" ] &&
    [ "$lifecycle_stage_verify_line" -lt "$lifecycle_script_commit_line" ] &&
    [ "$lifecycle_script_commit_line" -lt "$lifecycle_manifest_commit_line" ] ||
    fail 'management lifecycle installation must validate the complete stage and commit its manifest last'

# Installed backup scripts use rendered platform-exact command paths; PATH
# cannot silently select a deb wrapper on RPM or a PGDG command on deb.
assert_contains "readonly PG_DUMP_BINARY='@PROBE_PG_DUMP@'" "$BACKUP"
assert_contains "readonly PG_RESTORE_BINARY='@PROBE_PG_RESTORE@'" "$BACKUP"
assert_contains "readonly PG_RESTORE_BINARY='@PROBE_PG_RESTORE@'" "$RESTORE"
assert_contains "readonly PSQL_BINARY='@PROBE_PSQL@'" "$RESTORE"
for token_contract in \
    "$BACKUP|readonly PG_DUMP_BINARY='@PROBE_PG_DUMP@'" \
    "$BACKUP|readonly PG_RESTORE_BINARY='@PROBE_PG_RESTORE@'" \
    "$RESTORE|readonly PSQL_BINARY='@PROBE_PSQL@'" \
    "$RESTORE|readonly PG_RESTORE_BINARY='@PROBE_PG_RESTORE@'"; do
    token_file=${token_contract%%|*}
    token_line=${token_contract#*|}
    [ "$(grep -Fxc -- "$token_line" "$token_file")" -eq 1 ] ||
        fail "$token_file must contain its render declaration exactly once: $token_line"
done
backup_tokens=$(grep -Eo '@PROBE_[A-Z0-9_]+@' "$BACKUP" | sort -u)
restore_tokens=$(grep -Eo '@PROBE_[A-Z0-9_]+@' "$RESTORE" | sort -u)
[ "$backup_tokens" = "$(printf '%s\n' @PROBE_PG_DUMP@ @PROBE_PG_RESTORE@ | sort)" ] ||
    fail 'backup source has an unexpected render-token set'
[ "$restore_tokens" = "$(printf '%s\n' @PROBE_PG_RESTORE@ @PROBE_PSQL@ | sort)" ] ||
    fail 'restore source has an unexpected render-token set'
assert_contains 'pg_dump_binary="$(runtime_postgres_command pg_dump)"' "$DEPLOY_COMMON"
assert_contains 'pg_restore_binary="$(runtime_postgres_command pg_restore)"' "$DEPLOY_COMMON"
assert_contains 'psql_binary="$(runtime_postgres_command psql)"' "$DEPLOY_COMMON"
assert_contains 'installed PostgreSQL backup script has an unexpected pg_dump command path' "$DEPLOY_COMMON"
if grep -Eq '^[[:space:]]*((/usr/bin/)?env[[:space:]]+)?(pg_dump|pg_restore|psql)([[:space:]]|$)' "$BACKUP" "$RESTORE"; then
    fail 'backup or restore still executes a PATH-selected PostgreSQL client'
fi

# Restore is a root coordinator with explicit maintenance and failure state;
# it does not rely on systemd-run options unavailable in systemd 219.
for contract in \
    'acquire_deployment_lock' \
    'validate_installed_management_host' \
    'verify_running_services management' \
    'validate_enablement_snapshot probe-api.service "$API_ENABLED"' \
    'validate_enablement_snapshot probe-postgres-backup.timer "$BACKUP_TIMER_ENABLED"' \
    'systemctl disable probe-api.service probe-postgres-backup.timer' \
    'RESTORE_STARTED=1' \
    'run_as_probe_api_no_environment /usr/bin/env' \
    'run_migrations "$PROBE_API_DIR/probe-api"' \
    'restore or post-restore migration failed; probe-api and the backup timer remain stopped and disabled'; do
    assert_contains "$contract" "$RESTORE_COORDINATOR"
done
if grep -Fq 'systemd-run' "$RESTORE_COORDINATOR"; then
    fail 'management restore coordinator must not require systemd-run compatibility options'
fi
assert_before 'validate_installed_management_host' 'systemctl disable probe-api.service probe-postgres-backup.timer' "$RESTORE_COORDINATOR"
assert_before 'validate_enablement_snapshot probe-api.service "$API_ENABLED"' 'RESTORE_DEACTIVATED=1' "$RESTORE_COORDINATOR"
assert_before 'validate_enablement_snapshot probe-postgres-backup.timer "$BACKUP_TIMER_ENABLED"' 'RESTORE_DEACTIVATED=1' "$RESTORE_COORDINATOR"
[ "$(grep -Fc 'systemctl disable probe-api.service probe-postgres-backup.timer' "$RESTORE_COORDINATOR")" -eq 2 ] ||
    fail 'restore must contain exactly one failure cleanup and one main-flow disable operation'
main_disable_line=$(last_line_of 'systemctl disable probe-api.service probe-postgres-backup.timer' "$RESTORE_COORDINATOR")
restore_started_line=$(line_of 'RESTORE_STARTED=1' "$RESTORE_COORDINATOR")
[ -n "$main_disable_line" ] && [ -n "$restore_started_line" ] &&
    [ "$main_disable_line" -lt "$restore_started_line" ] ||
    fail 'main restore flow must disable API/timer before database mutation starts'
assert_before 'RESTORE_STARTED=1' 'run_as_probe_api_no_environment /usr/bin/env' "$RESTORE_COORDINATOR"
assert_before 'run_as_probe_api_no_environment /usr/bin/env' 'run_migrations "$PROBE_API_DIR/probe-api"' "$RESTORE_COORDINATOR"
migration_line=$(line_of 'run_migrations "$PROBE_API_DIR/probe-api"' "$RESTORE_COORDINATOR")
final_verify_line=$(grep -Fn -- 'verify_running_services management' "$RESTORE_COORDINATOR" | tail -n 1 | cut -d: -f1)
[ -n "$migration_line" ] && [ -n "$final_verify_line" ] && [ "$migration_line" -lt "$final_verify_line" ] ||
    fail 'management restore must run post-restore migrations before final runtime validation'
assert_contains 'management_systemd_property probe-postgres-backup.service ActiveState' "$RESTORE_COORDINATOR"
assert_contains 'require_loaded_management_unit_inactive probe-api.service' "$RESTORE_COORDINATOR"
assert_contains 'require_restore_dependency_active "$(runtime_postgres_service)"' "$RESTORE_COORDINATOR"
assert_contains 'require_restore_dependency_active nginx.service' "$RESTORE_COORDINATOR"
assert_contains 'API_WAS_ACTIVE="$(capture_restore_application_activity probe-api.service)"' "$RESTORE_COORDINATOR"
assert_contains 'BACKUP_TIMER_WAS_ACTIVE="$(capture_restore_application_activity probe-postgres-backup.timer)"' "$RESTORE_COORDINATOR"
first_verify_line=$(grep -Fn -- 'verify_running_services management' "$RESTORE_COORDINATOR" | head -n 1 | cut -d: -f1)
if [ "$first_verify_line" != "$final_verify_line" ]; then
    fail 'database recovery must not require API readiness before entering maintenance mode'
fi
if grep -Fq -- 'systemctl is-active --quiet probe-postgres-backup.service' "$RESTORE_COORDINATOR" ||
   grep -Fq -- 'systemctl is-active --quiet probe-api.service' "$RESTORE_COORDINATOR"; then
    fail 'management restore must not collapse systemd errors or transitional states into inactive'
fi

# Ordinary uninstall validates ownership first, holds both lifecycle locks,
# removes only Probe-owned activation assets and preserves all data roots and
# shared packages/services.
for contract in \
    'acquire_deployment_lock' \
    'validate_installed_management_host' \
    'acquire_database_maintenance_lock' \
    'validate_enablement_snapshot probe-api.service "$API_ENABLED"' \
    'validate_enablement_snapshot probe-postgres-backup.timer "$BACKUP_TIMER_ENABLED"' \
    'stage_removal "$PROBE_NGINX_LINK"' \
    'stage_removal "$PROBE_SYSTEMD_UNIT"' \
    'Preserved: configuration, TLS, setup state, PostgreSQL databases, backups'; do
    assert_contains "$contract" "$UNINSTALLER"
done
assert_contains 'case "$original" in' "$UNINSTALLER"
actual_uninstall_targets=$(sed -n 's/^[[:space:]]*stage_removal //p' "$UNINSTALLER" | sort)
expected_uninstall_targets=$(printf '%s\n' \
    '"$PROBE_NGINX_LINK"' \
    '"$PROBE_API_DIR/probe-api"' \
    '"$PROBE_ROOT/admin"' \
    '"$PROBE_ROOT/migrations"' \
    '"$PROBE_SYSTEMD_UNIT"' \
    '"$PROBE_BACKUP_SERVICE_UNIT"' \
    '"$PROBE_BACKUP_TIMER_UNIT"' \
    '"$PROBE_BACKUP_SCRIPT_DIR/backup-postgres.sh"' \
    '"$PROBE_BACKUP_SCRIPT_DIR/restore-postgres.sh"' | sort)
[ "$actual_uninstall_targets" = "$expected_uninstall_targets" ] ||
    fail 'ordinary uninstall target set differs from its exact nine-path allowlist'
assert_before 'UNINSTALL_COMMITTED=1' 'rm -f -- "$temporary"' "$UNINSTALLER"
assert_before 'validate_installed_management_host' 'systemctl stop probe-postgres-backup.timer probe-postgres-backup.service' "$UNINSTALLER"
assert_contains 'require_management_unit_inactive_or_absent probe-api.service' "$UNINSTALLER"
assert_contains 'require_management_unit_inactive_or_absent probe-postgres-backup.timer' "$UNINSTALLER"
if grep -Fq -- 'systemctl is-active --quiet probe-api.service' "$UNINSTALLER" ||
   grep -Fq -- 'systemctl is-active --quiet probe-postgres-backup.timer' "$UNINSTALLER"; then
    fail 'management uninstall must prove exact inactive state instead of accepting any systemctl error'
fi
for forbidden in \
    'rm -rf' 'apt-get' 'dnf ' 'yum ' 'systemctl stop nginx.service' \
    'systemctl stop postgresql.service' 'systemctl stop postgresql-14.service' \
    'rm -f -- /etc/probe-panel' 'rm -f -- /var/lib/probe-panel' \
    'rm -f -- /var/backups/probe-panel'; do
    if grep -Fq -- "$forbidden" "$UNINSTALLER"; then
        fail "ordinary management uninstall contains forbidden shared/data mutation: $forbidden"
    fi
done

for file in "$VALIDATOR" "$RESTORE_COORDINATOR" "$UNINSTALLER"; do
    if grep -Eiq 'probe-agent|probe-web|artifacts/(agent|web)|/srv/probe/(agent|web)|--profile[[:space:]]+full' "$file"; then
        fail "management lifecycle entry contains Agent, visitor, or full-profile logic: $file"
    fi
done

# Management release activation owns an explicit phase journal.  The API and
# backup timer are quiesced before backup/migration; every failure after that
# point restores their prior activity. Failures after service mutation also
# restore exact links, host assets and enablement. EXIT and signals share the
# same rollback path, and commit precedes snapshot cleanup.
for transaction_source in "$DEPLOY_COMMON" "$ROOT_DIR/probe-api/deploy/scripts/deploy-common-management-runtime.sh"; do
    for contract in \
        'MANAGEMENT_ACTIVATION_ROLLBACK_STATE="maintenance"' \
        'MANAGEMENT_ACTIVATION_ROLLBACK_STATE="links"' \
        'MANAGEMENT_ACTIVATION_ROLLBACK_STATE="runtime"' \
        'MANAGEMENT_ACTIVATION_ROLLBACK_STATE="none"' \
        'rollback_pending_management_activation' \
        'restore_management_postgres_activity' \
        'run_management_rollback_step' \
        "trap '' HUP INT TERM" \
        'could not snapshot management host assets'; do
        assert_contains "$contract" "$transaction_source"
    done
    capture_line=$(last_line_of 'capture_management_service_activity' "$transaction_source")
    maintenance_state_line=$(last_line_of 'MANAGEMENT_ACTIVATION_ROLLBACK_STATE="maintenance"' "$transaction_source")
    timer_stop_line=$(line_after 'stop_management_unit_to_inactive probe-postgres-backup.timer' "$maintenance_state_line" "$transaction_source")
    api_stop_line=$(line_after 'stop_management_unit_to_inactive probe-api.service' "$timer_stop_line" "$transaction_source")
    maintenance_start_line=$(line_after 'systemctl start "$(runtime_postgres_service)"' "$api_stop_line" "$transaction_source")
    backup_line=$(line_after 'backup_path="$(create_database_backup "$unique_release_id")"' "$maintenance_start_line" "$transaction_source")
    migration_line=$(line_after 'run_migrations "$release_dir/api/probe-api"' "$backup_line" "$transaction_source")
    [ -n "$capture_line" ] && [ -n "$maintenance_state_line" ] && [ -n "$timer_stop_line" ] &&
        [ -n "$api_stop_line" ] && [ -n "$maintenance_start_line" ] && [ -n "$backup_line" ] &&
        [ -n "$migration_line" ] && [ "$capture_line" -lt "$maintenance_state_line" ] &&
        [ "$maintenance_state_line" -lt "$timer_stop_line" ] && [ "$timer_stop_line" -lt "$api_stop_line" ] &&
        [ "$api_stop_line" -lt "$maintenance_start_line" ] && [ "$maintenance_start_line" -lt "$backup_line" ] &&
        [ "$backup_line" -lt "$migration_line" ] ||
        fail "$transaction_source must quiesce API and backup timer before backup/migration"
    assert_before 'MANAGEMENT_ACTIVATION_ROLLBACK_STATE="links"' 'activate_release "$release_dir" "$release_profile"' "$transaction_source"
    runtime_state_line=$(last_line_of 'MANAGEMENT_ACTIVATION_ROLLBACK_STATE="runtime"' "$transaction_source")
    api_restart_line=$(line_after 'systemctl restart probe-api.service' "$runtime_state_line" "$transaction_source")
    [ -n "$runtime_state_line" ] && [ -n "$api_restart_line" ] && [ "$runtime_state_line" -lt "$api_restart_line" ] ||
        fail "$transaction_source must journal runtime rollback before restarting the API"
    commit_line=$(last_line_of 'MANAGEMENT_ACTIVATION_ROLLBACK_STATE="none"' "$transaction_source")
    discard_line=$(last_line_of 'discard_management_service_asset_snapshot "$service_asset_snapshot"' "$transaction_source")
    [ -n "$commit_line" ] && [ -n "$discard_line" ] && [ "$commit_line" -lt "$discard_line" ] ||
        fail "$transaction_source must commit before deleting its rollback snapshot"
    assert_contains 'if rm -rf -- "$verify_root"; then' "$transaction_source"
    assert_contains 'verification workspace cleanup requires manual removal' "$transaction_source"
done
assert_contains 'management_systemd_property' "$DEPLOY_COMMON"
if grep -Fq -- 'systemctl show --property=LoadState --value' "$DEPLOY_COMMON"; then
    fail 'management activity capture must remain compatible with systemd 219 without show --value'
fi

runtime_function_contract=$(grep '^[[:space:]]*runtime_functions=' "$BUILDER")
for runtime_function in \
    management_lifecycle_asset_names \
    validate_management_lifecycle_manifest \
    validate_management_lifecycle_assets; do
    printf '%s\n' "$runtime_function_contract" | grep -Fq "$runtime_function" ||
        fail "management runtime function closure is missing $runtime_function"
done

printf '%s\n' 'management lifecycle contract: PASS'
