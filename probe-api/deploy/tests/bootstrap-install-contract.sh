#!/bin/sh

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH='' cd -- "$SCRIPT_DIR/../../.." && pwd)
INSTALLER=$ROOT_DIR/install.sh
SETUP_UNIT=$ROOT_DIR/probe-api/deploy/setup/probe-panel-setup.service
SETUP_SOCKET=$ROOT_DIR/probe-api/deploy/setup/probe-panel-setup.socket
FINALIZER_UNIT=$ROOT_DIR/probe-api/deploy/setup/probe-panel-finalizer.service
FINALIZER_PATH=$ROOT_DIR/probe-api/deploy/setup/probe-panel-finalizer.path
DEPLOY_COMMON=$ROOT_DIR/probe-api/deploy/scripts/deploy-common.sh
DEPLOY_INSTALL=$ROOT_DIR/probe-api/deploy/scripts/install.sh
DEPLOY_INSTALL_RELEASE=$ROOT_DIR/probe-api/deploy/scripts/install-release.sh
DEPLOY_UPGRADE=$ROOT_DIR/probe-api/deploy/scripts/upgrade.sh
DEPLOY_VALIDATE=$ROOT_DIR/probe-api/deploy/scripts/validate-production.sh
DOMAIN_NGINX=$ROOT_DIR/probe-api/deploy/nginx/nginx.conf
IP_NGINX=$ROOT_DIR/probe-api/deploy/nginx/nginx-ip.conf

fail() {
    printf '%s\n' "bootstrap installer contract: $*" >&2
    exit 1
}

assert_contains() {
    needle=$1
    file=$2
    grep -Fq -- "$needle" "$file" || fail "$file is missing required contract: $needle"
}

[ -f "$INSTALLER" ] || fail "missing installer: $INSTALLER"
[ -f "$SETUP_UNIT" ] || fail "missing setup unit: $SETUP_UNIT"
[ -f "$SETUP_SOCKET" ] || fail "missing setup socket unit: $SETUP_SOCKET"
[ -f "$FINALIZER_UNIT" ] || fail "missing finalizer unit: $FINALIZER_UNIT"
[ -f "$FINALIZER_PATH" ] || fail "missing finalizer path unit: $FINALIZER_PATH"
[ -f "$DEPLOY_COMMON" ] || fail "missing deployment helpers: $DEPLOY_COMMON"
[ -f "$DOMAIN_NGINX" ] || fail "missing domain Nginx template: $DOMAIN_NGINX"
[ -f "$IP_NGINX" ] || fail "missing IP Nginx template: $IP_NGINX"
bash -n "$INSTALLER"
for deployment_script in "$DEPLOY_COMMON" "$DEPLOY_INSTALL" "$DEPLOY_INSTALL_RELEASE" "$DEPLOY_UPGRADE" "$DEPLOY_VALIDATE"; do
    [ -f "$deployment_script" ] || fail "missing deployment script: $deployment_script"
    bash -n "$deployment_script"
done
sh -n "$0"

last_line=$(awk 'NF { line = $0 } END { print line }' "$INSTALLER")
[ "$last_line" = 'main "$@"' ] || fail 'installer entrypoint must be its final non-empty line'

TEST_ROOT=$(mktemp -d /tmp/probe-panel-bootstrap-contract.XXXXXX)
trap 'rm -rf -- "$TEST_ROOT"' EXIT

# Exercise the production helper through the host's real awk implementation.
# Debian 13 uses mawk by default, so source-grep contracts cannot catch parser
# incompatibilities in this security-critical listener allowlist.
IP_LISTENERS=$TEST_ROOT/nginx-ip.listeners
IP_MISSING_LISTENER=$TEST_ROOT/nginx-ip-missing.listeners
IP_OUTSIDE_LISTENER=$TEST_ROOT/nginx-ip-outside.listeners
DOMAIN_LISTENERS=$TEST_ROOT/nginx-domain.listeners
RUNTIME_IP_LISTENERS=$TEST_ROOT/runtime-ip.listeners
RUNTIME_IP_MISSING_LISTENER=$TEST_ROOT/runtime-ip-missing.listeners
RUNTIME_API_EXTERNAL_LISTENER=$TEST_ROOT/runtime-api-external.listeners
RUNTIME_POSTGRES_EXTERNAL_LISTENER=$TEST_ROOT/runtime-postgres-external.listeners
RUNTIME_DOMAIN_LISTENERS=$TEST_ROOT/runtime-domain.listeners
printf '%s\n' \
    '    listen 18453 ssl default_server;' \
    '    listen [::]:18453 ssl default_server;' \
    '    listen 18454 ssl default_server;' \
    '    listen [::]:18454 ssl default_server;' \
    '    listen 18455 ssl default_server;' \
    '    listen [::]:18455 ssl default_server;' > "$IP_LISTENERS"
printf '%s\n' \
    '    listen 18453 ssl default_server;' \
    '    listen [::]:18453 ssl default_server;' \
    '    listen 18454 ssl default_server;' \
    '    listen [::]:18454 ssl default_server;' \
    '    listen 18455 ssl default_server;' > "$IP_MISSING_LISTENER"
printf '%s\n' \
    '    listen 18453 ssl default_server;' \
    '    listen [::]:18453 ssl default_server;' \
    '    listen 18454 ssl default_server;' \
    '    listen [::]:18454 ssl default_server;' \
    '    listen 18455 ssl default_server;' \
    '    listen [::]:18455 ssl default_server;' \
    '    listen 8080;' > "$IP_OUTSIDE_LISTENER"
printf '%s\n' \
    '    listen 80;' \
    '    listen [::]:80;' \
    '    listen 443 ssl;' \
    '    listen [::]:443 ssl;' > "$DOMAIN_LISTENERS"
printf '%s\n' \
    'LISTEN 0 511 0.0.0.0:18453 0.0.0.0:*' \
    'LISTEN 0 511 [::]:18453 [::]:*' \
    'LISTEN 0 511 0.0.0.0:18454 0.0.0.0:*' \
    'LISTEN 0 511 [::]:18454 [::]:*' \
    'LISTEN 0 511 0.0.0.0:18455 0.0.0.0:*' \
    'LISTEN 0 511 [::]:18455 [::]:*' \
    'LISTEN 0 4096 127.0.0.1:8080 0.0.0.0:*' \
    'LISTEN 0 244 127.0.0.1:5432 0.0.0.0:*' \
    'LISTEN 0 244 [::1]:5432 [::]:*' > "$RUNTIME_IP_LISTENERS"
printf '%s\n' \
    'LISTEN 0 511 0.0.0.0:18453 0.0.0.0:*' \
    'LISTEN 0 511 [::]:18453 [::]:*' \
    'LISTEN 0 511 0.0.0.0:18454 0.0.0.0:*' \
    'LISTEN 0 511 [::]:18454 [::]:*' \
    'LISTEN 0 4096 127.0.0.1:8080 0.0.0.0:*' \
    'LISTEN 0 244 127.0.0.1:5432 0.0.0.0:*' > "$RUNTIME_IP_MISSING_LISTENER"
sed 's/127[.]0[.]0[.]1:8080/0.0.0.0:8080/' \
    "$RUNTIME_IP_LISTENERS" > "$RUNTIME_API_EXTERNAL_LISTENER"
sed 's/127[.]0[.]0[.]1:5432/192.0.2.10:5432/' \
    "$RUNTIME_IP_LISTENERS" > "$RUNTIME_POSTGRES_EXTERNAL_LISTENER"
printf '%s\n' \
    'LISTEN 0 511 0.0.0.0:80 0.0.0.0:*' \
    'LISTEN 0 511 [::]:80 [::]:*' \
    'LISTEN 0 511 0.0.0.0:443 0.0.0.0:*' \
    'LISTEN 0 511 [::]:443 [::]:*' \
    'LISTEN 0 4096 127.0.0.1:8080 0.0.0.0:*' \
    'LISTEN 0 244 [::1]:5432 [::]:*' > "$RUNTIME_DOMAIN_LISTENERS"

run_nginx_listener_contract() {
    listener_mode=$1
    listener_dump=$2
    /bin/bash -c '
        source "$1"
        PROBE_INGRESS_MODE=$2
        validate_nginx_listen_ports "$3"
    ' probe-nginx-listener-contract "$DEPLOY_COMMON" "$listener_mode" "$listener_dump"
}

run_runtime_listener_contract() {
    listener_mode=$1
    listener_dump=$2
    /bin/bash -c '
        source "$1"
        PROBE_INGRESS_MODE=$2
        RUNTIME_LISTENER_DUMP=$3
        ss() {
            [[ "$#" -eq 2 && "$1" == -H && "$2" == -lnt ]] || return 64
            /bin/cat "$RUNTIME_LISTENER_DUMP"
        }
        validate_runtime_listeners
    ' probe-runtime-listener-contract "$DEPLOY_COMMON" "$listener_mode" "$listener_dump"
}

run_nginx_listener_contract ip "$IP_LISTENERS" ||
    fail 'system awk rejected the complete six-listener IP contract'
run_nginx_listener_contract domain "$DOMAIN_LISTENERS" ||
    fail 'system awk rejected the domain 80/443 listener contract'
if run_nginx_listener_contract ip "$IP_MISSING_LISTENER" >/dev/null 2>&1; then
    fail 'system awk listener validation accepted a missing IP-mode listener'
fi
if run_nginx_listener_contract ip "$IP_OUTSIDE_LISTENER" >/dev/null 2>&1; then
    fail 'system awk listener validation accepted an out-of-contract port'
fi
run_runtime_listener_contract ip "$RUNTIME_IP_LISTENERS" ||
    fail 'system awk rejected valid IP-mode listeners without process metadata'
run_runtime_listener_contract domain "$RUNTIME_DOMAIN_LISTENERS" ||
    fail 'system awk rejected valid domain-mode listeners without process metadata'
if run_runtime_listener_contract ip "$RUNTIME_IP_MISSING_LISTENER" >/dev/null 2>&1; then
    fail 'system awk runtime validation accepted a missing IP-mode Nginx port'
fi
if run_runtime_listener_contract ip "$RUNTIME_API_EXTERNAL_LISTENER" >/dev/null 2>&1; then
    fail 'system awk runtime validation accepted an externally bound API port'
fi
if run_runtime_listener_contract ip "$RUNTIME_POSTGRES_EXTERNAL_LISTENER" >/dev/null 2>&1; then
    fail 'system awk runtime validation accepted an externally bound PostgreSQL port'
fi
# This is a literal source-code contract and must not expand in this process.
# shellcheck disable=SC2016
assert_contains 'listeners="$(ss -H -lnt)"' "$DEPLOY_COMMON"
if grep -Fq 'ss -H -lntp' "$DEPLOY_COMMON"; then
    fail 'runtime listener validation must not request process metadata from ss'
fi
# This is the former process-name attribution expression and must not return.
# shellcheck disable=SC2016
if grep -Fq '$0 ~ /\(\("nginx"/' "$DEPLOY_COMMON"; then
    fail 'runtime listener validation must not depend on Nginx process metadata'
fi

STUB_ROOT=$TEST_ROOT/bin
MARKER=$TEST_ROOT/external-command-ran
mkdir "$STUB_ROOT"
# The generated stub expands this variable only when the stub runs.
# shellcheck disable=SC2016
printf '%s\n' \
    '#!/bin/sh' \
    ': > "${PROBE_BOOTSTRAP_TRUNCATION_MARKER:?}"' \
    'exit 97' > "$STUB_ROOT/stub"
chmod 0755 "$STUB_ROOT/stub"
for name in addgroup adduser apt-get awk chown chmod cp curl find getent grep id install journalctl mktemp mv nginx psql python3 rm runuser setpriv sha256sum sleep ss stat systemctl systemd-analyze uname wc; do
    ln -s stub "$STUB_ROOT/$name"
done

TRUNCATED=$TEST_ROOT/truncated.sh
WITHOUT_ENTRYPOINT=$TEST_ROOT/without-entrypoint.sh
awk '
    { print }
    /^[[:space:]]*INSTALL_COMPLETED=1[[:space:]]*$/ { found = 1; exit }
    END { if (!found) exit 1 }
' "$INSTALLER" > "$TRUNCATED"
if PATH=$STUB_ROOT PROBE_BOOTSTRAP_TRUNCATION_MARKER=$MARKER /bin/bash "$TRUNCATED" >/dev/null 2>&1; then
    fail 'body-truncated installer unexpectedly succeeded'
fi
[ ! -e "$MARKER" ] || fail 'body-truncated installer executed an external command'

sed '$d' "$INSTALLER" > "$WITHOUT_ENTRYPOINT"
PATH=$STUB_ROOT PROBE_BOOTSTRAP_TRUNCATION_MARKER=$MARKER /bin/bash "$WITHOUT_ENTRYPOINT" >/dev/null 2>&1 ||
    fail 'function-only installer should parse without executing'
[ ! -e "$MARKER" ] || fail 'installer without final entrypoint executed an external command'

ENTRY_SIM_ROOT=$TEST_ROOT/directory-entry-validation
mkdir -p "$ENTRY_SIM_ROOT/empty" "$ENTRY_SIM_ROOT/allowed" \
    "$ENTRY_SIM_ROOT/unexpected-visible" "$ENTRY_SIM_ROOT/unexpected-hidden"
: > "$ENTRY_SIM_ROOT/allowed/release"
: > "$ENTRY_SIM_ROOT/allowed/.managed"
: > "$ENTRY_SIM_ROOT/allowed/..double-dot-name"
: > "$ENTRY_SIM_ROOT/unexpected-visible/release"
: > "$ENTRY_SIM_ROOT/unexpected-visible/foreign"
: > "$ENTRY_SIM_ROOT/unexpected-hidden/release"
: > "$ENTRY_SIM_ROOT/unexpected-hidden/.foreign"
/bin/bash -c '
    source "$1"
    assert_directory_contains_only empty "$2/empty"
    assert_directory_contains_only allowed "$2/allowed" release .managed ..double-dot-name
' probe-directory-entry-allowed "$WITHOUT_ENTRYPOINT" "$ENTRY_SIM_ROOT" ||
    fail 'directory entry validation rejected an empty or exact allowlisted layout'
if /bin/bash -c '
    source "$1"
    assert_directory_contains_only visible "$2/unexpected-visible" release
' probe-directory-entry-visible "$WITHOUT_ENTRYPOINT" "$ENTRY_SIM_ROOT" >/dev/null 2>&1; then
    fail 'directory entry validation accepted an unexpected visible entry'
fi
if /bin/bash -c '
    source "$1"
    assert_directory_contains_only hidden "$2/unexpected-hidden" release
' probe-directory-entry-hidden "$WITHOUT_ENTRYPOINT" "$ENTRY_SIM_ROOT" >/dev/null 2>&1; then
    fail 'directory entry validation accepted an unexpected hidden entry'
fi

SOCKET_SIM_ROOT=$TEST_ROOT/socket-rollback
SOCKET_SIM_LOG=$SOCKET_SIM_ROOT/systemctl.log
mkdir "$SOCKET_SIM_ROOT"
/bin/bash -c '
    source "$1"
    SETUP_SOCKET_UNIT="$2/probe-panel-setup.socket"
    SETUP_SOCKET_SERVICE=probe-panel-setup.socket
    MIGRATION_SOCKET_UNIT_INSTALLED=0
    SYSTEMCTL_LOG=$3
    systemctl() { printf "%s\n" "$*" >> "$SYSTEMCTL_LOG"; }

    rollback_migration_setup_socket
    [[ ! -e "$SYSTEMCTL_LOG" ]] || exit 10

    MIGRATION_SOCKET_UNIT_INSTALLED=1
    rollback_migration_setup_socket
    grep -Fxq "stop probe-panel-setup.socket" "$SYSTEMCTL_LOG"
    grep -Fxq "disable probe-panel-setup.socket" "$SYSTEMCTL_LOG"

    : > "$SYSTEMCTL_LOG"
    MIGRATION_SOCKET_UNIT_INSTALLED=0
    : > "$SETUP_SOCKET_UNIT"
    rollback_migration_setup_socket
    grep -Fxq "stop probe-panel-setup.socket" "$SYSTEMCTL_LOG"
    grep -Fxq "disable probe-panel-setup.socket" "$SYSTEMCTL_LOG"
' probe-socket-rollback "$WITHOUT_ENTRYPOINT" "$SOCKET_SIM_ROOT" "$SOCKET_SIM_LOG" ||
    fail 'migration rollback socket simulation failed'

help_output=$(bash "$INSTALLER" --help)
for command_name in install migrate-bootstrap status uninstall purge; do
    printf '%s\n' "$help_output" | grep -Fq -- "$command_name" || fail "help is missing $command_name"
done
if bash "$INSTALLER" purge >/dev/null 2>&1; then
    fail 'purge must fail closed'
fi
if bash "$INSTALLER" install unexpected >/dev/null 2>&1; then
    fail 'installer accepted extra command-line values'
fi

# These are literal source-code contracts and must not expand in this process.
# shellcheck disable=SC2016
for contract in \
    'PANEL_VERSION="${PROBE_PANEL_RELEASE_VERSION:-v1.1.0}"' \
    'SUPER_MY_REF="refs/tags/v1.1.0"' \
    'WEB_REF="refs/tags/v1.0.0"' \
    'AGENT_REF="refs/tags/v1.0.2"' \
    'LEGACY_PANEL_VERSION="v1.0.0"' \
    'LEGACY_SUPER_MY_REF="refs/tags/v1.0.0"' \
    'LEGACY_AGENT_REF="refs/tags/v1.0.1"' \
    'probe-panel-${PANEL_VERSION}-linux-${architecture}.tar.gz' \
    "https://github.com/Kcmose/super-my/releases/download/" \
    "--proto '=https'" \
    'sha256sum' \
    'BUNDLE-SHA256SUMS must cover every source, artifacts, and setup file exactly once' \
    '[[ -f "$root/$required" && ! -L "$root/$required" ]]' \
    'release migrations path is not a real directory' \
    'artifacts/api/probe-api' \
    'setup/probe-setup' \
    'artifacts/migrations' \
    'source/probe-api/config/probe-postgres-backup.env.example' \
    'source/probe-api/deploy/scripts/build-release-bundles.sh' \
    'source/probe-api/deploy/scripts/backup-postgres.sh' \
    'source/probe-api/deploy/scripts/restore-postgres.sh' \
    'source/probe-api/deploy/systemd/probe-postgres-backup.service' \
    'source/probe-api/deploy/systemd/probe-postgres-backup.timer' \
    'nginx postgresql postgresql-client certbot' \
    'iproute2 util-linux' \
    'systemctl mask nginx.service' \
    'systemctl unmask nginx.service' \
    'systemctl stop nginx.service' \
    'systemctl disable nginx.service' \
    "disabled the stock Debian Nginx default site" \
    'systemctl enable --now postgresql.service' \
    "psql --no-psqlrc -Atqc 'SHOW listen_addresses'" \
    'source/probe-api/deploy/setup/probe-panel-setup.socket' \
    'source/probe-api/deploy/nginx/nginx.conf' \
    'source/probe-api/deploy/nginx/nginx-ip.conf' \
    'source/probe-api/deploy/setup/probe-panel-finalizer.service' \
    'source/probe-api/deploy/setup/probe-panel-finalizer.path' \
    'setup finalizer must not make the entire Nginx configuration directory writable' \
    'setup finalizer must have exactly five reviewed ingress bind rules' \
    'for finalizer_bind_rule in tcp:80 tcp:443 tcp:18453 tcp:18454 tcp:18455; do' \
    'FINALIZER_REQUEST_FILE="${FINALIZER_RUNTIME_ROOT}/finalize.json"' \
    'FINALIZER_RESULT_FILE="${FINALIZER_RUNTIME_ROOT}/result.json"' \
    'PROBE_SETUP_FINALIZE_REQUEST_FILE=%s' \
    'PROBE_SETUP_FINALIZE_RESULT_FILE=%s' \
    'PROBE_SETUP_BUNDLE_ROOT=%s' \
    'PROBE_SETUP_RELEASE_ID=%s' \
    'PurePosixPath' \
    'path.is_absolute()' \
    '".." in path.parts' \
    'member.issym() or member.islnk()' \
    'special files are forbidden' \
    'PROBE_SETUP_SOCKET_PATH=%s' \
    'PROBE_SETUP_SERVER_IP=%s' \
    '"$SETUP_BINARY" init' \
    'ssh -N -o ExitOnForwardFailure=yes -L 127.0.0.1:18080:/run/probe-panel-setup/setup.sock' \
    'migrate-bootstrap) migrate_bootstrap_action' \
    'validate_legacy_release_bundle()' \
    'validate_legacy_bootstrap_metadata()' \
    'assert_directory_contains_only()' \
    'assert_root_regular_file "$root/BUNDLE-SHA256SUMS" 600' \
    'require_dir(release_ui, 0o700)' \
    'only pending or configuring v1.0.0 bootstrap state can be migrated' \
    'the v1.0.0 immutable release failed its internal SHA256 verification' \
    'the active setup binary does not match the immutable v1.0.0 release' \
    'active setup UI does not exactly match the immutable v1.0.0 release' \
    'formal, finalizing, or mixed installation data exists at' \
    '/etc/systemd/system/probe-postgres-backup.service' \
    '/etc/systemd/system/probe-postgres-backup.timer' \
    'unit_is_installed probe-postgres-backup.service' \
    'unit_is_installed probe-postgres-backup.timer' \
    "'the legacy /srv/probe layout' /srv/probe releases setup-ui" \
    "'the legacy releases directory' \"\$RELEASES_ROOT\" \"\${LEGACY_RELEASE##*/}\"" \
    'contains an unexpected entry' \
    'write_pending_setup_state()' \
    'remove_legacy_setup_code()' \
    'MIGRATION_BACKUP="$TEMP_ROOT/legacy-backup"' \
    'rollback_migration_setup_socket()' \
    'rollback_migration_setup_socket || rollback_ready=0' \
    'MIGRATION_SOCKET_UNIT_INSTALLED=1' \
    'MIGRATION_COMPLETED=1' \
    'legacy action: run this pinned v1.1.0 installer with migrate-bootstrap' \
    'Probe Panel v1.1 does not display or require an installation code; any legacy setup-code record is handled only by migrate-bootstrap and is never displayed.' \
    'main() {' \
    'main "$@"'; do
    assert_contains "$contract" "$INSTALLER"
done

if grep -Fq 'No installation code, database credential, or administrator credential exists or is displayed.' "$INSTALLER"; then
    fail 'status must not claim that a preserved legacy setup-code record does not exist'
fi

[ "$(grep -Fc 'source/probe-api/deploy/nginx/nginx.conf' "$INSTALLER")" -ge 2 ] ||
    fail 'current and legacy bundle validation must both require the domain Nginx template'
[ "$(grep -Fc 'source/probe-api/deploy/nginx/nginx-ip.conf' "$INSTALLER")" -ge 1 ] ||
    fail 'current bundle validation must require the IP Nginx template'

for account_script in "$INSTALLER" "$DEPLOY_COMMON"; do
    for contract in \
        'login_defs_number()' \
        "login_defs_number SYS_UID_MAX \"\$((uid_min - 1))\"" \
        'login.defs(5) defines the omitted SYS_UID_MAX default as UID_MIN-1' \
        '/etc/login.defs must define at most one valid numeric' \
        'assert_probe_api_service_account()' \
        'probe-api must have exactly one passwd record and one same-name group record' \
        '/etc/login.defs SYS_UID_MAX must be below UID_MIN' \
        'probe-api or login.defs contains an out-of-range numeric identifier' \
        'probe-api UID is outside the Debian system-account range' \
        'probe-api must use /nonexistent as its home directory' \
        'probe-api must use /usr/sbin/nologin as its shell' \
        'probe-api must have a unique UID' \
        'the probe-api primary GID must belong to exactly one group' \
        'the probe-api UID is also used by another account' \
        'the probe-api primary GID is also used by another group' \
        'probe-api must not have supplementary groups' \
        'the probe-api group must not contain other or duplicate explicit members' \
        'the probe-api primary group is also used by another account' \
        'a partial probe-api service account or group already exists; refusing to repair it'; do
        assert_contains "$contract" "$account_script"
    done
done
[ "$(grep -Fc 'assert_probe_api_service_account' "$INSTALLER")" -ge 3 ] ||
    fail 'fresh bootstrap and legacy migration must both enforce the strict probe-api account contract'
[ "$(grep -Fc 'assert_probe_api_service_account' "$DEPLOY_COMMON")" -ge 4 ] ||
    fail 'deployment preparation, activation, and runtime validation must enforce the strict probe-api account contract'
# This assertion matches a literal source snippet; expansion would weaken the contract.
# shellcheck disable=SC2016
assert_contains 'ensure_secure_directory "$SETUP_CONFIG_ROOT" 0755' "$INSTALLER"
assert_contains 'install -d -o root -g root -m 0755 /etc/probe-panel' "$DEPLOY_COMMON"
# This assertion matches a literal diagnostic string in the installer source.
# shellcheck disable=SC2016
assert_contains '$SETUP_CONFIG_ROOT must be root:root mode 0755' "$INSTALLER"
assert_contains '/etc/probe-panel must be a root:root directory with mode 0755' "$DEPLOY_COMMON"
# This assertion matches a literal source snippet; expansion would weaken the contract.
# shellcheck disable=SC2016
assert_contains 'chmod 0600 "$temporary"' "$INSTALLER"
# This assertion matches a literal source snippet; expansion would weaken the contract.
# shellcheck disable=SC2016
assert_contains 'install -o root -g probe-api -m 0640 /dev/null "$PROBE_ALLOWLIST_FILE"' "$DEPLOY_COMMON"
bootstrap_account_line=$(grep -n '^[[:space:]]*prepare_probe_api_account[[:space:]]*$' "$INSTALLER" | tail -n 1 | cut -d: -f1)
bootstrap_runtime_line=$(grep -n '^[[:space:]]*prepare_runtime_services[[:space:]]*$' "$INSTALLER" | tail -n 1 | cut -d: -f1)
if [ -z "$bootstrap_account_line" ] ||
    [ -z "$bootstrap_runtime_line" ] ||
    [ "$bootstrap_account_line" -ge "$bootstrap_runtime_line" ]; then
    fail 'strict probe-api account validation must precede Nginx/PostgreSQL service mutations'
fi
migration_action_line=$(grep -n '^migrate_bootstrap_action()[[:space:]]*{' "$INSTALLER" | cut -d: -f1)
migration_account_line=$(grep -n '^[[:space:]]*assert_probe_api_service_account[[:space:]]*$' "$INSTALLER" | tail -n 1 | cut -d: -f1)
if [ -z "$migration_action_line" ] ||
    [ -z "$migration_account_line" ] ||
    [ "$migration_account_line" -le "$migration_action_line" ]; then
    fail 'legacy bootstrap migration must invoke the strict probe-api account validator'
fi

legacy_code_removal_line=$(grep -n '^[[:space:]]*remove_legacy_setup_code[[:space:]]*$' "$INSTALLER" | tail -n 1 | cut -d: -f1)
migration_ready_line=$(grep -n '^[[:space:]]*assert_setup_listener[[:space:]]*$' "$INSTALLER" | tail -n 1 | cut -d: -f1)
if [ -z "$legacy_code_removal_line" ] || [ -z "$migration_ready_line" ]; then
    fail 'migration readiness/code-removal ordering markers are missing'
fi
[ "$legacy_code_removal_line" -gt "$migration_ready_line" ] ||
    fail 'legacy setup code must be removed only after the migrated socket passes readiness checks'

assert_contains "printf 'PROBE_SETUP_FINALIZE_REQUEST_FILE=%s\\n' \"\$FINALIZER_REQUEST_FILE\"" "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_FINALIZE_RESULT_FILE=%s\\n' \"\$FINALIZER_RESULT_FILE\"" "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_BUNDLE_ROOT=%s\\n' \"\$INSTALLED_RELEASE\"" "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_RELEASE_ID=%s\\n' \"\$PANEL_VERSION\"" "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_SOCKET_PATH=%s\\n' \"\$SETUP_SOCKET_PATH\"" "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_SERVER_IP=%s\\n' \"\$SETUP_SERVER_IP\"" "$INSTALLER"

if grep -Eq '(^|[[:space:]])(golang-go|nodejs|npm)([[:space:]\\]|$)' "$INSTALLER"; then
    fail 'server bootstrap must use prebuilt assets and must not install Go, Node.js, or npm'
fi

if grep -Eiq -- '(database|db|admin)[_-]?(password|secret)[=:]' "$INSTALLER"; then
    fail 'installer appears to define a database or administrator secret'
fi
if grep -Eq -- '--(database|db|admin)-(password|secret)' "$INSTALLER"; then
    fail 'installer must not accept database or administrator secret arguments'
fi
if grep -Eq 'refs/heads/|/main/' "$INSTALLER"; then
    fail 'installer must not use a moving GitHub branch ref'
fi

for preserved in /etc/probe-panel /var/lib/probe-panel /var/backups/probe-panel 'all PostgreSQL databases'; do
    assert_contains "$preserved" "$INSTALLER"
done
if grep -Eq 'rm[[:space:]].*(/etc/probe-panel|/var/lib/probe-panel|/var/backups/probe-panel)' "$INSTALLER"; then
    fail 'ordinary uninstall must preserve configuration, state, and backups'
fi

for contract in \
    'Wants=probe-panel-setup.socket' \
    'User=root' \
    'Group=root' \
    'EnvironmentFile=/etc/probe-panel/setup.env' \
    'ExecStart=/usr/local/lib/probe-panel/probe-setup serve' \
    'Restart=on-failure' \
    'ProtectSystem=strict' \
    'ReadOnlyPaths=/srv/probe/setup-ui' \
    'ReadWritePaths=/var/lib/probe-panel/setup' \
    'ReadWritePaths=/run/probe-panel-setup' \
    'CapabilityBoundingSet=' \
    'RestrictAddressFamilies=AF_UNIX' \
    'PrivateNetwork=true' \
    'SocketBindDeny=any'; do
    assert_contains "$contract" "$SETUP_UNIT"
done

if grep -Fqx 'Restart=always' "$SETUP_UNIT"; then
    fail 'installed/recovery setup shutdown must not enter a systemd restart loop'
fi

if grep -Fq -- 'Requires=probe-panel-setup.socket' "$SETUP_UNIT"; then
    fail 'stopping the setup socket must not propagate a stop to the setup service'
fi
if grep -Eq '^RuntimeDirectory' "$SETUP_UNIT"; then
    fail 'the setup service must not remove the socket-owned runtime directory when it exits'
fi

for contract in \
    'ListenStream=/run/probe-panel-setup/setup.sock' \
    'SocketUser=root' \
    'SocketGroup=root' \
    'SocketMode=0600' \
    'DirectoryMode=0700' \
    'Accept=no' \
    'RemoveOnStop=yes'; do
    assert_contains "$contract" "$SETUP_SOCKET"
done

if grep -Eq 'SocketBindAllow=tcp:' "$SETUP_UNIT"; then
    fail 'setup service must not bind a TCP socket'
fi

if grep -Eq 'SocketBindAllow=tcp:(80|443)$' "$SETUP_UNIT"; then
    fail 'setup service must not bind production HTTP/HTTPS ports'
fi

for contract in \
    'User=root' \
    'Group=root' \
    'EnvironmentFile=/etc/probe-panel/setup.env' \
    'ExecStart=/usr/local/lib/probe-panel/probe-setup finalize' \
    'ExecStopPost=/usr/bin/sleep 20' \
    'ExecStopPost=/usr/bin/systemctl stop probe-panel-setup.socket' \
    'ExecStopPost=/usr/bin/systemctl --no-block stop probe-panel-setup.service' \
    'TimeoutStartSec=30min' \
    'CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE CAP_FOWNER CAP_NET_BIND_SERVICE CAP_SETGID CAP_SETUID' \
    'AmbientCapabilities=CAP_SETGID CAP_SETUID' \
    'NoNewPrivileges=true' \
    'ProtectSystem=strict' \
    'ReadOnlyPaths=/srv/probe/setup-ui' \
    'ReadWritePaths=/etc/probe-panel' \
    'ReadWritePaths=/var/lib/probe-panel/setup' \
    'ReadWritePaths=/etc/nginx/conf.d' \
    'ReadWritePaths=/etc/nginx/sites-enabled' \
    'ReadWritePaths=/etc/systemd/system' \
    'ReadWritePaths=/etc/letsencrypt' \
    'ReadWritePaths=/var/lib/letsencrypt' \
    'ReadWritePaths=/var/log/letsencrypt' \
    'ReadWritePaths=/var/log/nginx' \
    'ReadWritePaths=/var/lib/nginx' \
    'ReadWritePaths=/srv/probe' \
    'ReadWritePaths=/var/backups/probe-panel' \
    'ReadWritePaths=/run/lock' \
    'ReadWritePaths=/run/probe-panel-setup' \
    'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK' \
    'SocketBindAllow=tcp:80' \
    'SocketBindAllow=tcp:443' \
    'SocketBindAllow=tcp:18453' \
    'SocketBindAllow=tcp:18454' \
    'SocketBindAllow=tcp:18455' \
    'SocketBindDeny=any'; do
    assert_contains "$contract" "$FINALIZER_UNIT"
done

if grep -Fxq 'ReadWritePaths=/etc/nginx' "$FINALIZER_UNIT"; then
    fail 'finalizer must not make the entire Nginx configuration directory writable'
fi

[ "$(grep -Fc 'SocketBindAllow=' "$FINALIZER_UNIT")" -eq 5 ] ||
    fail 'finalizer must contain exactly five reviewed ingress bind rules'

[ "$(grep -Fc 'ExecStopPost=' "$FINALIZER_UNIT")" -eq 3 ] ||
    fail 'finalizer must contain exactly three reviewed ExecStopPost actions'
finalizer_sleep_line=$(grep -Fnx 'ExecStopPost=/usr/bin/sleep 20' "$FINALIZER_UNIT" | cut -d: -f1)
finalizer_socket_stop_line=$(grep -Fnx 'ExecStopPost=/usr/bin/systemctl stop probe-panel-setup.socket' "$FINALIZER_UNIT" | cut -d: -f1)
finalizer_service_stop_line=$(grep -Fnx 'ExecStopPost=/usr/bin/systemctl --no-block stop probe-panel-setup.service' "$FINALIZER_UNIT" | cut -d: -f1)
if [ -z "$finalizer_sleep_line" ] ||
    [ -z "$finalizer_socket_stop_line" ] ||
    [ -z "$finalizer_service_stop_line" ]; then
    fail 'finalizer delayed stop sequence is incomplete'
fi
if [ "$finalizer_sleep_line" -ge "$finalizer_socket_stop_line" ] ||
    [ "$finalizer_socket_stop_line" -ge "$finalizer_service_stop_line" ]; then
    fail 'finalizer must delay, stop the socket, then stop the setup service'
fi

# These are literal shell snippets whose variables must remain unexpanded.
# shellcheck disable=SC2016
assert_contains 'PGHOST="$PROBE_VALIDATED_PGHOST"' "$DEPLOY_COMMON"
# shellcheck disable=SC2016
assert_contains 'PGPASSFILE="$PROBE_VALIDATED_PGPASSFILE"' "$DEPLOY_COMMON"
assert_contains '--no-password' "$DEPLOY_COMMON"
assert_contains '/usr/bin/env -i' "$DEPLOY_COMMON"
# shellcheck disable=SC2016
if grep -Fq 'PGDATABASE="$PROBE_DATABASE_URL" pg_dump' "$DEPLOY_COMMON"; then
    fail 'pre-migration backup must not pass a PostgreSQL URL through PGDATABASE'
fi
if grep -Eq '^ReadWritePaths=/etc/rc([0-6]|S)[.]d$' "$FINALIZER_UNIT"; then
    fail 'setup finalizer must not write Debian SysV boot-link directories'
fi
if grep -Eq 'systemctl enable.*nginx[.]service' "$DEPLOY_COMMON"; then
    fail 'Nginx persistence must not invoke Debian SysV enable synchronization'
fi
assert_contains 'systemctl add-wants multi-user.target nginx.service' "$DEPLOY_COMMON"
# These are literal source-code contracts and must not expand in this process.
# shellcheck disable=SC2016
for contract in \
    'readonly PROBE_PRIVATE_CA_FILE="/etc/probe-panel/tls/private-ca/ca.pem"' \
    'PROBE_INGRESS_MODE must be exactly domain or ip' \
    'PROBE_AGENT_INSTALL_CA_FILE must be set explicitly' \
    'selected_nginx_template()' \
    'validate_closed_install_routes()' \
    'probe-api/deploy/nginx/nginx-ip.conf' \
    'validate_nginx_template_contract "${source_root}/probe-api/deploy/nginx/nginx.conf" domain' \
    'validate_nginx_template_contract "${source_root}/probe-api/deploy/nginx/nginx-ip.conf" ip' \
    'canonical_ip_from_origin "$PROBE_ADMIN_ORIGIN" 18455' \
    'canonical_ip_from_origin "$PROBE_AGENT_PUBLIC_URL" 18454' \
    'validate_ingress_tls_with_binary()' \
    'config validate-ingress-tls domain' \
    'config validate-ingress-tls ip' \
    'validate_certbot_timer_state()' \
    'domain mode requires certbot.timer to be enabled' \
    'domain mode requires certbot.timer to be active' \
    'IP mode requires certbot.timer to be disabled' \
    'IP mode requires certbot.timer to be inactive' \
    'validate_ingress_tls_with_binary "$artifact_root/api/probe-api"' \
    'validate_ingress_tls_with_binary "${PROBE_API_DIR}/probe-api"' \
    'PROBE_SETUP_SERVER_IP' \
    'nginx-ip.conf.example' \
    'port == "18453" || port == "18454" || port == "18455"' \
    'host != "127.0.0.1" && host != "::1"' \
    'PROBE_INGRESS_MODE changed during prebuilt release validation' \
    'PROBE_INGRESS_MODE changed during release build; refusing to switch ingress mode'; do
    assert_contains "$contract" "$DEPLOY_COMMON"
done
# These are literal source-code contracts and must not expand in this process.
# shellcheck disable=SC2016
for contract in \
    'validate_nginx_template_contract "${resolved}/probe-api/deploy/nginx/nginx.conf" domain' \
    'validate_nginx_template_contract "${resolved}/probe-api/deploy/nginx/nginx-ip.conf" ip' \
    'selected_nginx_template "$resolved_source"' \
    'validate_ingress_tls_with_binary "$api_real"' \
    'validate_certbot_timer_state'; do
    assert_contains "$contract" "$DEPLOY_VALIDATE"
done
assert_contains 'PROBE_INGRESS_MODE must explicitly select domain or ip' "$DEPLOY_INSTALL"
assert_contains 'does not issue certificates, rewrite the active fragment, or switch modes' "$DEPLOY_INSTALL_RELEASE"
assert_contains 'upgrade never issues certificates or switches modes' "$DEPLOY_UPGRADE"
for template in "$DOMAIN_NGINX" "$IP_NGINX"; do
    [ "$(grep -Fxc '    location = /install {' "$template")" -eq 2 ] ||
        fail "$template must close the exact installation route on visitor and administrator entries"
    [ "$(grep -Fxc '    location ^~ /install/ {' "$template")" -eq 2 ] ||
        fail "$template must close the installation route prefix on visitor and administrator entries"
done
if grep -Fq 'SocketBindAllow=tcp:18080' "$FINALIZER_UNIT"; then
    fail 'non-HTTP finalizer must not bind the setup HTTP port'
fi

for contract in \
    'Requires=probe-panel-setup.service' \
    'PathExists=/run/probe-panel-setup/finalize.json' \
    'Unit=probe-panel-finalizer.service'; do
    assert_contains "$contract" "$FINALIZER_PATH"
done

printf '%s\n' 'bootstrap installer contract: PASS'
