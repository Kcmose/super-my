#!/bin/sh
# This file intentionally searches for literal shell variables in source text.
# shellcheck disable=SC2016

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH='' cd -- "$SCRIPT_DIR/../../.." && pwd)
INSTALLER=$ROOT_DIR/install.sh
INSTALL_COMMON=$ROOT_DIR/install/common.sh
INSTALL_GENERATOR=$ROOT_DIR/install/build-standalone.sh
PLATFORM_DEBIAN=$ROOT_DIR/install/platforms/debian.sh
PLATFORM_UBUNTU=$ROOT_DIR/install/platforms/ubuntu.sh
PLATFORM_CENTOS=$ROOT_DIR/install/platforms/centos.sh
SETUP_UNIT=$ROOT_DIR/probe-api/deploy/setup/probe-panel-setup.service
SETUP_SOCKET=$ROOT_DIR/probe-api/deploy/setup/probe-panel-setup.socket
SETUP_LEGACY_UNIT=$ROOT_DIR/probe-api/deploy/setup/probe-panel-setup-legacy.service
SETUP_LEGACY_SOCKET=$ROOT_DIR/probe-api/deploy/setup/probe-panel-setup-legacy.socket
FINALIZER_UNIT=$ROOT_DIR/probe-api/deploy/setup/probe-panel-finalizer-management.service
FINALIZER_LEGACY_UNIT=$ROOT_DIR/probe-api/deploy/setup/probe-panel-finalizer-management-legacy.service
FINALIZER_PATH=$ROOT_DIR/probe-api/deploy/setup/probe-panel-finalizer.path
API_SYSTEMD_UNIT=$ROOT_DIR/probe-api/deploy/systemd/probe-api.service
API_LEGACY_SYSTEMD_UNIT=$ROOT_DIR/probe-api/deploy/systemd/probe-api-legacy.service
BACKUP_SYSTEMD_UNIT=$ROOT_DIR/probe-api/deploy/systemd/probe-postgres-backup.service
BACKUP_LEGACY_SYSTEMD_UNIT=$ROOT_DIR/probe-api/deploy/systemd/probe-postgres-backup-legacy.service
BACKUP_TIMER=$ROOT_DIR/probe-api/deploy/systemd/probe-postgres-backup.timer
BACKUP_LEGACY_TIMER=$ROOT_DIR/probe-api/deploy/systemd/probe-postgres-backup-legacy.timer
SETUP_MAIN=$ROOT_DIR/probe-api/cmd/probe-setup/main.go
ADMIN_SOURCE_ROOT=$ROOT_DIR
if [ ! -f "$ADMIN_SOURCE_ROOT/src/views/Install.vue" ]; then
    ADMIN_SOURCE_ROOT=$ROOT_DIR/probe-admin
fi
ADMIN_INSTALL=$ADMIN_SOURCE_ROOT/src/views/Install.vue
ADMIN_SETUP_UTILS=$ADMIN_SOURCE_ROOT/src/utils/setup.js
ADMIN_SETUP_API=$ADMIN_SOURCE_ROOT/src/api/setup.js
OPENAPI=$ROOT_DIR/probe-api/api/openapi.yaml
DEPLOY_COMMON=$ROOT_DIR/probe-api/deploy/scripts/deploy-common.sh
DEPLOY_MANAGEMENT_RUNTIME=$ROOT_DIR/probe-api/deploy/scripts/deploy-common-management-runtime.sh
DEPLOY_INSTALL=$ROOT_DIR/probe-api/deploy/scripts/install.sh
DEPLOY_INSTALL_RELEASE=$ROOT_DIR/probe-api/deploy/scripts/install-release.sh
DEPLOY_UPGRADE=$ROOT_DIR/probe-api/deploy/scripts/upgrade.sh
DEPLOY_VALIDATE=$ROOT_DIR/probe-api/deploy/scripts/validate-production.sh
DOMAIN_NGINX=$ROOT_DIR/probe-api/deploy/nginx/nginx.conf
IP_NGINX=$ROOT_DIR/probe-api/deploy/nginx/nginx-ip.conf
MANAGEMENT_NGINX=$ROOT_DIR/probe-api/deploy/nginx/nginx-management.conf
MANAGEMENT_IP_NGINX=$ROOT_DIR/probe-api/deploy/nginx/nginx-management-ip.conf
MANAGEMENT_LEGACY_NGINX=$ROOT_DIR/probe-api/deploy/nginx/nginx-management-legacy.conf
MANAGEMENT_LEGACY_IP_NGINX=$ROOT_DIR/probe-api/deploy/nginx/nginx-management-ip-legacy.conf
MANAGEMENT_CLASSIC_NGINX=$ROOT_DIR/probe-api/deploy/nginx/nginx-management-classic.conf
MANAGEMENT_CLASSIC_IP_NGINX=$ROOT_DIR/probe-api/deploy/nginx/nginx-management-ip-classic.conf

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
[ -f "$INSTALL_COMMON" ] || fail "missing installer common source: $INSTALL_COMMON"
[ -f "$INSTALL_GENERATOR" ] || fail "missing standalone installer generator: $INSTALL_GENERATOR"
[ -f "$PLATFORM_DEBIAN" ] || fail "missing Debian installer adapter: $PLATFORM_DEBIAN"
[ -f "$PLATFORM_UBUNTU" ] || fail "missing Ubuntu installer adapter: $PLATFORM_UBUNTU"
[ -f "$PLATFORM_CENTOS" ] || fail "missing CentOS installer adapter: $PLATFORM_CENTOS"
[ -f "$SETUP_UNIT" ] || fail "missing setup unit: $SETUP_UNIT"
[ -f "$SETUP_SOCKET" ] || fail "missing setup socket unit: $SETUP_SOCKET"
[ -f "$SETUP_LEGACY_UNIT" ] || fail "missing legacy setup unit: $SETUP_LEGACY_UNIT"
[ -f "$SETUP_LEGACY_SOCKET" ] || fail "missing legacy setup socket unit: $SETUP_LEGACY_SOCKET"
[ -f "$FINALIZER_UNIT" ] || fail "missing finalizer unit: $FINALIZER_UNIT"
[ -f "$FINALIZER_LEGACY_UNIT" ] || fail "missing legacy finalizer unit: $FINALIZER_LEGACY_UNIT"
[ -f "$FINALIZER_PATH" ] || fail "missing finalizer path unit: $FINALIZER_PATH"
[ -f "$API_SYSTEMD_UNIT" ] || fail "missing API systemd unit: $API_SYSTEMD_UNIT"
[ -f "$API_LEGACY_SYSTEMD_UNIT" ] || fail "missing legacy API systemd unit: $API_LEGACY_SYSTEMD_UNIT"
[ -f "$BACKUP_SYSTEMD_UNIT" ] || fail "missing backup systemd unit: $BACKUP_SYSTEMD_UNIT"
[ -f "$BACKUP_LEGACY_SYSTEMD_UNIT" ] || fail "missing legacy backup systemd unit: $BACKUP_LEGACY_SYSTEMD_UNIT"
[ -f "$BACKUP_TIMER" ] || fail "missing backup timer: $BACKUP_TIMER"
[ -f "$BACKUP_LEGACY_TIMER" ] || fail "missing legacy backup timer: $BACKUP_LEGACY_TIMER"
[ -f "$SETUP_MAIN" ] || fail "missing setup command source: $SETUP_MAIN"
[ -f "$ADMIN_INSTALL" ] || fail "missing management install view: $ADMIN_INSTALL"
[ -f "$ADMIN_SETUP_UTILS" ] || fail "missing management setup utilities: $ADMIN_SETUP_UTILS"
[ -f "$ADMIN_SETUP_API" ] || fail "missing management setup client: $ADMIN_SETUP_API"
[ -f "$OPENAPI" ] || fail "missing OpenAPI contract: $OPENAPI"
[ -f "$DEPLOY_COMMON" ] || fail "missing deployment helpers: $DEPLOY_COMMON"
[ -f "$DEPLOY_MANAGEMENT_RUNTIME" ] || fail "missing management runtime helpers: $DEPLOY_MANAGEMENT_RUNTIME"
[ -f "$DOMAIN_NGINX" ] || fail "missing domain Nginx template: $DOMAIN_NGINX"
[ -f "$IP_NGINX" ] || fail "missing IP Nginx template: $IP_NGINX"
for management_template in \
    "$MANAGEMENT_NGINX" "$MANAGEMENT_IP_NGINX" \
    "$MANAGEMENT_LEGACY_NGINX" "$MANAGEMENT_LEGACY_IP_NGINX" \
    "$MANAGEMENT_CLASSIC_NGINX" "$MANAGEMENT_CLASSIC_IP_NGINX"; do
    [ -f "$management_template" ] || fail "missing management Nginx compatibility template: $management_template"
done
for installer_source in \
    "$INSTALLER" "$INSTALL_COMMON" "$INSTALL_GENERATOR" \
    "$PLATFORM_DEBIAN" "$PLATFORM_UBUNTU" "$PLATFORM_CENTOS"; do
    [ -f "$installer_source" ] && [ ! -L "$installer_source" ] ||
        fail "installer source must be a regular non-link file: $installer_source"
    bash -n "$installer_source"
done
bash "$INSTALL_GENERATOR" --check
bash "$INSTALL_GENERATOR" --check
for deployment_script in "$DEPLOY_COMMON" "$DEPLOY_MANAGEMENT_RUNTIME" "$DEPLOY_INSTALL" "$DEPLOY_INSTALL_RELEASE" "$DEPLOY_UPGRADE" "$DEPLOY_VALIDATE"; do
    [ -f "$deployment_script" ] || fail "missing deployment script: $deployment_script"
    bash -n "$deployment_script"
done
sh -n "$0"

# V1.2 exposes one management-product setup entry. Compatibility wire fields
# remain empty, but no browser, command, or documented API path may select the
# historical all-in-one profile or configure independent product ingress.
assert_contains "profile: 'management'" "$ADMIN_SETUP_UTILS"
assert_contains "payload?.profile !== 'management'" "$ADMIN_SETUP_API"
assert_contains 'PROBE_SETUP_PROFILE must be exactly management' "$SETUP_MAIN"
assert_contains 'privileged setup request must be exactly management' "$SETUP_MAIN"
if grep -Eq 'panel-domain|agent-domain|setupProfile|isManagementProfile|18453|18454' "$ADMIN_INSTALL"; then
    fail 'management install view still contains a historical multi-product setup path'
fi
if grep -Eq "SETUP_PROFILES|normalizedSetupProfile|profile[[:space:]]*=[[:space:]]*'full'|selectedProfile" "$ADMIN_SETUP_UTILS"; then
    fail 'management setup utilities still contain selectable profile logic'
fi
if grep -Eq 'InstallationProfileFull|ParseInstallationProfile' "$SETUP_MAIN"; then
    fail 'probe-setup command still accepts or defaults the historical full profile'
fi
setup_openapi=$(sed -n '/^    SetupDefaults:/,/^    ErrorResponse:/p' "$OPENAPI")
printf '%s\n' "$setup_openapi" | grep -Fq 'const: management' ||
    fail 'Setup OpenAPI does not bind requests to management'
printf '%s\n' "$setup_openapi" | grep -Fq 'required: [profile, database, domains, network, tls, allowlist, administrator]' ||
    fail 'Setup OpenAPI does not require the immutable management profile'
if printf '%s\n' "$setup_openapi" | grep -Eq 'SetupFullDefaults|const: full|enum: \[management, full\]|18453|18454|panel_url|agent_url'; then
    fail 'Setup OpenAPI still documents a historical multi-product installation path'
fi

last_line=$(awk 'NF { line = $0 } END { print line }' "$INSTALLER")
[ "$last_line" = 'if :; then main "$@" '\''probe-panel-bootstrap-complete-v1'\''; fi' ] ||
    fail 'installer entrypoint must be a sentinel-protected complete compound command'

for adapter in debian ubuntu centos; do
    adapter_file=$ROOT_DIR/install/platforms/$adapter.sh
    [ "$(grep -Fc "# --- BEGIN GENERATED PLATFORM ADAPTER: $adapter ---" "$INSTALLER")" -eq 1 ] ||
        fail "standalone installer must contain exactly one generated $adapter adapter"
    for operation in \
        configure preflight_commands native_unit_paths assert_packaged_file \
        assert_postgresql_clients preflight_security runtime_packages install_packages \
        initialize_postgresql create_service_account validate_nologin_shell \
        disable_default_nginx_site; do
        grep -Eq "^${adapter}_platform_${operation}[(][)]" "$adapter_file" ||
            fail "$adapter adapter is missing operation: $operation"
        [ "$(grep -Ec "^${adapter}_platform_${operation}[(][)]" "$INSTALLER")" -eq 1 ] ||
            fail "standalone installer must contain exactly one $adapter $operation operation"
    done
    if grep -Eq '^(source|[.][[:space:]]|trap|set|umask|main)([[:space:](]|$)' "$adapter_file"; then
        fail "$adapter adapter performs work outside function definitions"
    fi
done
if grep -Eq '^configure_(deb|rpm)_platform[[:space:]]+(debian|ubuntu|centos)-' "$INSTALL_COMMON"; then
    fail 'common installer source still owns an exact distribution-version mapping'
fi
if grep -Eq 'install/platforms/(debian|ubuntu|centos)[.]sh|https?://[^[:space:]]+(debian|ubuntu|centos)[.]sh' "$INSTALLER"; then
    fail 'standalone installer tries to load a platform adapter at runtime'
fi
if grep -Eq '(^|[[:space:]])(source|[.])[[:space:]]+.*platforms/' "$INSTALLER"; then
    fail 'standalone installer sources a platform adapter instead of embedding it'
fi

TEST_ROOT=$(mktemp -d /tmp/probe-panel-bootstrap-contract.XXXXXX)
trap 'rm -rf -- "$TEST_ROOT"' EXIT

DEFINITION_ONLY_FIXTURE=$TEST_ROOT/definition-only-installer
UNSAFE_ADAPTER_MARKER=$TEST_ROOT/unsafe-platform-adapter-import
mkdir -p "$DEFINITION_ONLY_FIXTURE/install/platforms"
cp "$INSTALL_GENERATOR" "$DEFINITION_ONLY_FIXTURE/install/build-standalone.sh"
cp "$INSTALL_COMMON" "$DEFINITION_ONLY_FIXTURE/install/common.sh"
cp "$PLATFORM_DEBIAN" "$DEFINITION_ONLY_FIXTURE/install/platforms/debian.sh"
cp "$PLATFORM_UBUNTU" "$DEFINITION_ONLY_FIXTURE/install/platforms/ubuntu.sh"
cp "$PLATFORM_CENTOS" "$DEFINITION_ONLY_FIXTURE/install/platforms/centos.sh"
printf 'touch %s\n' "$UNSAFE_ADAPTER_MARKER" >> \
    "$DEFINITION_ONLY_FIXTURE/install/platforms/debian.sh"
if bash "$DEFINITION_ONLY_FIXTURE/install/build-standalone.sh" --check \
    >/dev/null 2>"$DEFINITION_ONLY_FIXTURE/error"; then
    fail 'standalone generator accepted a platform adapter with a top-level command'
fi
assert_contains 'platform adapter must contain function definitions only' \
    "$DEFINITION_ONLY_FIXTURE/error"
[ ! -e "$UNSAFE_ADAPTER_MARKER" ] ||
    fail 'standalone generator executed an imported platform adapter'

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
    '    listen 18455 ssl default_server;' \
    '    listen [::]:18455 ssl default_server;' > "$IP_LISTENERS"
printf '%s\n' \
    '    listen 18455 ssl default_server;' > "$IP_MISSING_LISTENER"
printf '%s\n' \
    '    listen 18455 ssl default_server;' \
    '    listen [::]:18455 ssl default_server;' \
    '    listen 8080;' > "$IP_OUTSIDE_LISTENER"
printf '%s\n' \
    '    listen 80;' \
    '    listen [::]:80;' \
    '    listen 443 ssl;' \
    '    listen [::]:443 ssl;' > "$DOMAIN_LISTENERS"
printf '%s\n' \
    'LISTEN 0 511 0.0.0.0:18455 0.0.0.0:*' \
    'LISTEN 0 511 [::]:18455 [::]:*' \
    'LISTEN 0 4096 127.0.0.1:8080 0.0.0.0:*' \
    'LISTEN 0 244 127.0.0.1:5432 0.0.0.0:*' \
    'LISTEN 0 244 [::1]:5432 [::]:*' > "$RUNTIME_IP_LISTENERS"
printf '%s\n' \
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
        validate_nginx_listen_ports "$3" management
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
        validate_runtime_listeners management
    ' probe-runtime-listener-contract "$DEPLOY_COMMON" "$listener_mode" "$listener_dump"
}

run_nginx_listener_contract ip "$IP_LISTENERS" ||
    fail 'system awk rejected the complete two-listener management IP contract'
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

# Exercise the deployment lock helper against disposable roots. stat ownership
# is normalized so this contract remains runnable by non-root developers while
# inode/mode checks and the actual flock still use the host implementation.
run_deploy_lock_contract() {
    lock_root=$1
    lock_file=$2
    /bin/bash -c '
        source "$1"
        TEST_LOCK_ROOT=$2
        TEST_LOCK_FILE=$3
        stat() {
            if [[ "$#" -eq 3 && "$1" == -c && "$2" == "%u:%g" && "$3" == "$TEST_LOCK_ROOT" ]]; then
                printf "%s\n" 0:0
                return
            fi
            if [[ "$#" -eq 3 && "$1" == -c && "$2" == "%u:%g:%a" && "$3" == "$TEST_LOCK_FILE" ]]; then
                printf "0:0:%s\n" "$(command stat -c "%a" "$3")"
                return
            fi
            command stat "$@"
        }
        acquire_root_lock "$TEST_LOCK_FILE"
    ' probe-deploy-lock-contract "$DEPLOY_COMMON" "$lock_root" "$lock_file"
}

DEPLOY_LOCK_ROOT=$TEST_ROOT/deploy-lock-root
DEPLOY_LOCK_FILE=$DEPLOY_LOCK_ROOT/probe-panel-deploy.lock
mkdir "$DEPLOY_LOCK_ROOT"
chmod 1777 "$DEPLOY_LOCK_ROOT"
run_deploy_lock_contract "$DEPLOY_LOCK_ROOT" "$DEPLOY_LOCK_FILE" ||
    fail 'deployment lock rejected a root-owned-equivalent 1777 sticky parent'
if [ ! -f "$DEPLOY_LOCK_FILE" ] || [ -L "$DEPLOY_LOCK_FILE" ] ||
    [ "$(stat -c '%a' "$DEPLOY_LOCK_FILE")" != 600 ]; then
    fail 'deployment lock was not atomically created as a regular mode-0600 file'
fi

DEPLOY_SAFE_LOCK_ROOT=$TEST_ROOT/deploy-safe-lock-root
DEPLOY_SAFE_LOCK_FILE=$DEPLOY_SAFE_LOCK_ROOT/probe-panel-deploy.lock
mkdir "$DEPLOY_SAFE_LOCK_ROOT"
chmod 0755 "$DEPLOY_SAFE_LOCK_ROOT"
run_deploy_lock_contract "$DEPLOY_SAFE_LOCK_ROOT" "$DEPLOY_SAFE_LOCK_FILE" ||
    fail 'deployment lock rejected a non-group/world-writable parent'

DEPLOY_UNSAFE_LOCK_ROOT=$TEST_ROOT/deploy-unsafe-lock-root
DEPLOY_UNSAFE_LOCK_FILE=$DEPLOY_UNSAFE_LOCK_ROOT/probe-panel-deploy.lock
mkdir "$DEPLOY_UNSAFE_LOCK_ROOT"
chmod 0777 "$DEPLOY_UNSAFE_LOCK_ROOT"
if run_deploy_lock_contract "$DEPLOY_UNSAFE_LOCK_ROOT" "$DEPLOY_UNSAFE_LOCK_FILE" >/dev/null 2>&1; then
    fail 'deployment lock accepted a world-writable parent without the sticky bit'
fi
[ ! -e "$DEPLOY_UNSAFE_LOCK_FILE" ] ||
    fail 'deployment lock created a file under an unsafe lock parent'

DEPLOY_LOCK_VICTIM=$TEST_ROOT/deploy-lock-victim
DEPLOY_LOCK_SYMLINK=$DEPLOY_LOCK_ROOT/preoccupied.lock
printf '%s\n' preserve-me > "$DEPLOY_LOCK_VICTIM"
ln -s "$DEPLOY_LOCK_VICTIM" "$DEPLOY_LOCK_SYMLINK"
if run_deploy_lock_contract "$DEPLOY_LOCK_ROOT" "$DEPLOY_LOCK_SYMLINK" >/dev/null 2>&1; then
    fail 'deployment lock followed a preoccupied symbolic link'
fi
grep -Fxq preserve-me "$DEPLOY_LOCK_VICTIM" ||
    fail 'deployment lock truncated the target of a preoccupied symbolic link'

# Hold the production lock inode on fd 8 while a second helper process tries
# fd 9. The second acquisition must fail immediately instead of waiting.
DEPLOY_LOCK_READY=$TEST_ROOT/deploy-lock-ready
DEPLOY_LOCK_RELEASE=$TEST_ROOT/deploy-lock-release
mkfifo "$DEPLOY_LOCK_RELEASE"
(
    exec 8>>"$DEPLOY_LOCK_FILE"
    flock --exclusive 8
    : > "$DEPLOY_LOCK_READY"
    read -r _ < "$DEPLOY_LOCK_RELEASE"
) &
deploy_lock_holder=$!
deploy_lock_wait=0
while [ ! -e "$DEPLOY_LOCK_READY" ] && [ "$deploy_lock_wait" -lt 50 ]; do
    sleep 0.1
    deploy_lock_wait=$((deploy_lock_wait + 1))
done
if [ ! -e "$DEPLOY_LOCK_READY" ]; then
    kill "$deploy_lock_holder" 2>/dev/null || true
    wait "$deploy_lock_holder" || true
    fail 'could not establish the deployment-lock contention fixture'
fi
if run_deploy_lock_contract "$DEPLOY_LOCK_ROOT" "$DEPLOY_LOCK_FILE" >/dev/null 2>&1; then
    printf '%s\n' release > "$DEPLOY_LOCK_RELEASE"
    wait "$deploy_lock_holder" || true
    fail 'deployment lock did not fail non-blocking under contention'
fi
printf '%s\n' release > "$DEPLOY_LOCK_RELEASE"
wait "$deploy_lock_holder" ||
    fail 'deployment-lock contention fixture exited unexpectedly'

assert_prebuilt_activation_order() {
    activation_source=$1
    activation_label=$2
    activation_block=$TEST_ROOT/prebuilt-$activation_label.sh
    sed -n '/^deploy_prebuilt_release() {$/,/^}$/p' "$activation_source" > "$activation_block"
    # This is a literal source-code contract; $source_root must not expand here.
    # shellcheck disable=SC2016
    [ "$(grep -Fc 'install_service_assets "$source_root"' "$activation_block")" -eq 1 ] ||
        fail "$activation_label prebuilt deployment must install service assets exactly once"
    activation_migrate_line=$(grep -n '^[[:space:]]*if ! run_migrations ' "$activation_block" | cut -d: -f1)
    activation_link_line=$(grep -n '^[[:space:]]*if ! activate_release ' "$activation_block" | cut -d: -f1)
    activation_assets_line=$(grep -n '^[[:space:]]*install_service_assets ' "$activation_block" | cut -d: -f1)
    activation_nginx_line=$(grep -n '^[[:space:]]*validate_nginx_runtime_config ' "$activation_block" | cut -d: -f1)
    activation_asset_failure_line=$(grep -n '^[[:space:]]*if (( service_asset_status != 0 )); then' "$activation_block" | cut -d: -f1)
    activation_reload_line=$(grep -n '^[[:space:]]*systemctl daemon-reload' "$activation_block" | cut -d: -f1)
    activation_runtime_state_line=$(grep -n '^[[:space:]]*MANAGEMENT_ACTIVATION_ROLLBACK_STATE="runtime"' "$activation_block" | cut -d: -f1)
    activation_commit_line=$(grep -n '^[[:space:]]*MANAGEMENT_ACTIVATION_ROLLBACK_STATE="none"' "$activation_block" | cut -d: -f1)
    if [ -z "$activation_migrate_line" ] || [ -z "$activation_link_line" ] ||
        [ -z "$activation_assets_line" ] || [ -z "$activation_nginx_line" ] ||
        [ -z "$activation_asset_failure_line" ] || [ -z "$activation_reload_line" ] ||
        [ -z "$activation_runtime_state_line" ] || [ -z "$activation_commit_line" ] ||
        [ "$activation_migrate_line" -ge "$activation_link_line" ] ||
        [ "$activation_link_line" -ge "$activation_assets_line" ] ||
        [ "$activation_assets_line" -ge "$activation_nginx_line" ] ||
        [ "$activation_nginx_line" -ge "$activation_asset_failure_line" ] ||
        [ "$activation_asset_failure_line" -ge "$activation_reload_line" ] ||
        [ "$activation_reload_line" -ge "$activation_runtime_state_line" ] ||
        [ "$activation_runtime_state_line" -ge "$activation_commit_line" ]; then
        fail "$activation_label prebuilt deployment exposes formal service/Nginx assets before migration and release-link activation"
    fi
    rollback_block=$TEST_ROOT/prebuilt-rollback-$activation_label.sh
    sed -n '/^rollback_pending_management_activation() {$/,/^}$/p' "$activation_source" > "$rollback_block"
    grep -Fq 'restore_release_links ' "$rollback_block" ||
        fail "$activation_label prebuilt deployment does not restore prior release links through its transaction journal"
    grep -Fq 'restore_management_service_assets "$snapshot"' "$rollback_block" ||
        fail "$activation_label prebuilt deployment does not restore its exact host-asset snapshot"
    grep -Fq 'systemctl daemon-reload' "$rollback_block" ||
        fail "$activation_label prebuilt deployment does not reload systemd after journal rollback"
    [ "$(grep -Fc 'rollback_pending_management_activation' "$activation_source")" -ge 2 ] ||
        fail "$activation_label deployment cleanup does not invoke the centralized activation rollback"
    # Literal diagnostic source; $backup_path must remain unexpanded.
    # shellcheck disable=SC2016
    grep -Fq 'release service asset activation failed; the forward database migration remains applied and backup is $backup_path' "$activation_block" ||
        fail "$activation_label prebuilt deployment does not explain the forward-migration recovery boundary"
}

assert_prebuilt_activation_order "$DEPLOY_COMMON" common
assert_prebuilt_activation_order "$DEPLOY_MANAGEMENT_RUNTIME" management

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
for name in addgroup adduser apt-get awk chown chmod cp curl find getent grep id install journalctl mktemp mv nginx psql python3 rm runuser setpriv sha256sum sleep ss stat systemctl systemd-analyze uname wc wget; do
    ln -s stub "$STUB_ROOT/$name"
done

TRUNCATED=$TEST_ROOT/truncated.sh
WITHOUT_ENTRYPOINT=$TEST_ROOT/without-entrypoint.sh
awk '
    { print }
    /^[ \t]*INSTALL_COMPLETED=1[ \t]*$/ { found = 1; exit }
    END { if (!found) exit 1 }
' "$INSTALLER" > "$TRUNCATED"
if PATH=$STUB_ROOT PROBE_BOOTSTRAP_TRUNCATION_MARKER=$MARKER /bin/bash "$TRUNCATED" >/dev/null 2>&1; then
    fail 'body-truncated installer unexpectedly succeeded'
fi
[ ! -e "$MARKER" ] || fail 'body-truncated installer executed an external command'

ADAPTER_TRUNCATED=$TEST_ROOT/adapter-truncated.sh
awk '
    { print }
    /^centos_platform_install_packages\(\) \{$/ { found = 1; exit }
    END { if (!found) exit 1 }
' "$INSTALLER" > "$ADAPTER_TRUNCATED"
if PATH=$STUB_ROOT PROBE_BOOTSTRAP_TRUNCATION_MARKER=$MARKER \
    /bin/bash "$ADAPTER_TRUNCATED" >/dev/null 2>&1; then
    fail 'platform-adapter-truncated installer unexpectedly succeeded'
fi
[ ! -e "$MARKER" ] || fail 'platform-adapter-truncated installer executed an external command'

sed '$d' "$INSTALLER" > "$WITHOUT_ENTRYPOINT"
PATH=$STUB_ROOT PROBE_BOOTSTRAP_TRUNCATION_MARKER=$MARKER /bin/bash "$WITHOUT_ENTRYPOINT" >/dev/null 2>&1 ||
    fail 'function-only installer should parse without executing'
[ ! -e "$MARKER" ] || fail 'installer without final entrypoint executed an external command'

# Every strict prefix of the final compound entrypoint must remain a syntax
# error. Override main in the fixture so the old unsafe bare `main` prefix would
# leave an unmistakable marker even on an EOL host that otherwise fails early.
ENTRYPOINT_PREFIX=$TEST_ROOT/entrypoint-prefix.sh
while IFS= read -r entrypoint_prefix; do
    cp "$WITHOUT_ENTRYPOINT" "$ENTRYPOINT_PREFIX"
    printf '%s\n' \
        'main() { : > "${PROBE_BOOTSTRAP_TRUNCATION_MARKER:?}"; }' \
        "$entrypoint_prefix" >> "$ENTRYPOINT_PREFIX"
    if PATH=$STUB_ROOT PROBE_BOOTSTRAP_TRUNCATION_MARKER=$MARKER \
        /bin/bash "$ENTRYPOINT_PREFIX" >/dev/null 2>&1; then
        fail "truncated entrypoint prefix unexpectedly parsed: $entrypoint_prefix"
    fi
    [ ! -e "$MARKER" ] ||
        fail "truncated entrypoint prefix executed main: $entrypoint_prefix"
done <<'EOF'
if
if :
if :;
if :; then
if :; then main
if :; then main "$@"
if :; then main "$@" 'probe-panel-bootstrap-complete-v1'
if :; then main "$@" 'probe-panel-bootstrap-complete-v1';
if :; then main "$@" 'probe-panel-bootstrap-complete-v1'; f
EOF

cp "$WITHOUT_ENTRYPOINT" "$ENTRYPOINT_PREFIX"
printf '%s\n' \
    'main() { : > "${PROBE_BOOTSTRAP_TRUNCATION_MARKER:?}"; }' \
    "$last_line" >> "$ENTRYPOINT_PREFIX"
PATH=$STUB_ROOT PROBE_BOOTSTRAP_TRUNCATION_MARKER=$MARKER \
    /bin/bash "$ENTRYPOINT_PREFIX" >/dev/null 2>&1 ||
    fail 'complete sentinel-protected entrypoint did not execute'
[ -e "$MARKER" ] || fail 'complete sentinel-protected entrypoint did not call main'
rm -f -- "$MARKER"

if PROBE_BOOTSTRAP_TRUNCATION_MARKER=$MARKER /bin/bash -c '
    source "$1"
    install_action() { : > "${PROBE_BOOTSTRAP_TRUNCATION_MARKER:?}"; }
    main
' probe-missing-entrypoint-sentinel "$WITHOUT_ENTRYPOINT" >/dev/null 2>&1; then
    fail 'main accepted a call without the complete entrypoint sentinel'
fi
[ ! -e "$MARKER" ] || fail 'main performed installation work before validating its sentinel'
/bin/bash -c '
    source "$1"
    PROBE_BOOTSTRAP_TRUNCATION_MARKER=$2
    install_action() { : > "${PROBE_BOOTSTRAP_TRUNCATION_MARKER:?}"; }
    main probe-panel-bootstrap-complete-v1
' probe-complete-entrypoint-sentinel "$WITHOUT_ENTRYPOINT" "$MARKER" ||
    fail 'main rejected its complete final-argument sentinel'
[ -e "$MARKER" ] || fail 'main did not enter the requested action after validating its sentinel'
rm -f -- "$MARKER"

# Debian 9 provides Python 3.5 and may have wget before curl. Keep both the IP
# detector and the immutable-release downloader inside that oldest-host floor.
DETECT_SERVER_IP_SOURCE=$TEST_ROOT/detect-server-ip.source
sed -n '/^detect_server_ip()/,/^}/p' "$INSTALLER" > "$DETECT_SERVER_IP_SOURCE"
assert_contains '.format(error)' "$DETECT_SERVER_IP_SOURCE"
if grep -Eq "f[\"']" "$DETECT_SERVER_IP_SOURCE"; then
    fail 'server IP detection contains a Python f-string and is not Python 3.5 compatible'
fi

PREFLIGHT_CA_FIXTURE=$TEST_ROOT/preflight-ca.pem
printf '%s\n' fixture-ca > "$PREFLIGHT_CA_FIXTURE"
PROBE_PREFLIGHT_CA_FIXTURE=$PREFLIGHT_CA_FIXTURE /bin/bash -c '
    source "$1"
    PLATFORM_ADAPTER=debian
    PLATFORM_PACKAGE_FAMILY=deb
    PLATFORM_PACKAGE_MANAGER=apt-get
    PLATFORM_CA_BUNDLE=$PROBE_PREFLIGHT_CA_FIXTURE
    require_command() { [[ "$1" == python3 ]]; }
    command() {
        [[ "$1" == -v ]] || return 96
        case "$2" in
            curl) return 1 ;;
            wget) return 0 ;;
            *) return 0 ;;
        esac
    }
    require_preflight_commands
' probe-wget-only-preflight "$WITHOUT_ENTRYPOINT" ||
    fail 'release preflight rejected a host with wget but no curl'
if PROBE_PREFLIGHT_CA_FIXTURE=$PREFLIGHT_CA_FIXTURE /bin/bash -c '
    source "$1"
    PLATFORM_ADAPTER=debian
    PLATFORM_PACKAGE_FAMILY=deb
    PLATFORM_PACKAGE_MANAGER=apt-get
    PLATFORM_CA_BUNDLE=$PROBE_PREFLIGHT_CA_FIXTURE
    require_command() { [[ "$1" == python3 ]]; }
    command() {
        [[ "$1" == -v ]] || return 96
        case "$2" in
            curl|wget) return 1 ;;
            *) return 0 ;;
        esac
    }
    require_preflight_commands
' probe-no-downloader-preflight "$WITHOUT_ENTRYPOINT" >/dev/null 2>&1; then
    fail 'release preflight accepted a host with neither curl nor wget'
fi

/bin/bash -c '
    source "$1"
    PLATFORM_ADAPTER=centos
    PLATFORM_PACKAGE_FAMILY=rpm
    getenforce() { printf "%s\n" Enforcing; }
    preflight_platform_security
' probe-selinux-enforcing-rejection "$WITHOUT_ENTRYPOINT" >/dev/null 2>&1 &&
    fail 'CentOS SELinux Enforcing reached the unverified mutation path'
/bin/bash -c '
    source "$1"
    PLATFORM_ADAPTER=centos
    PLATFORM_PACKAGE_FAMILY=rpm
    getenforce() { printf "%s\n" Permissive; }
    warn() { :; }
    preflight_platform_security
' probe-selinux-permissive-candidate "$WITHOUT_ENTRYPOINT" ||
    fail 'CentOS SELinux Permissive candidate was rejected before isolated testing'
/bin/bash -c '
    source "$1"
    PLATFORM_ADAPTER=debian
    PLATFORM_PACKAGE_FAMILY=deb
    getenforce() { printf "%s\n" Enforcing; }
    preflight_platform_security
' probe-non-rpm-selinux-scope "$WITHOUT_ENTRYPOINT" ||
    fail 'SELinux candidate gate leaked into the deb-family path'

DOWNLOAD_SOURCE=$TEST_ROOT/download-file.source
sed -n '/^download_file()/,/^}/p' "$INSTALLER" > "$DOWNLOAD_SOURCE"
# These are literal downloader source contracts.
# shellcheck disable=SC2016
for downloader_contract in \
    "--proto '=https'" \
    "--proto-redir '=https'" \
    '--tlsv1.2' \
    '--https-only' \
    '--max-redirect=5' \
    '--output-document="$destination"'; do
    assert_contains "$downloader_contract" "$DOWNLOAD_SOURCE"
done
assert_contains 'curl -q --tlsv1.2 --version' "$INSTALLER"
DOWNLOADER_STUB_ROOT=$TEST_ROOT/downloader-bin
DOWNLOADER_LOG=$TEST_ROOT/downloader.log
mkdir "$DOWNLOADER_STUB_ROOT"
# The generated stub expands these variables only when it runs.
# shellcheck disable=SC2016
printf '%s\n' \
    '#!/bin/sh' \
    'printf "%s\n" "$*" > "${PROBE_DOWNLOADER_LOG:?}"' \
    'exit 0' > "$DOWNLOADER_STUB_ROOT/wget"
chmod 0755 "$DOWNLOADER_STUB_ROOT/wget"
PATH=$DOWNLOADER_STUB_ROOT PROBE_DOWNLOADER_LOG=$DOWNLOADER_LOG /bin/bash -c '
    source "$1"
    download_file "$2" https://downloads.example.invalid/probe.tar.gz 60
' probe-wget-download "$WITHOUT_ENTRYPOINT" "$TEST_ROOT/wget-download" ||
    fail 'wget-only immutable release download fallback failed'
for wget_argument in \
    '--https-only' '--timeout=15' '--tries=3' '--max-redirect=5' \
    "--output-document=$TEST_ROOT/wget-download" \
    'https://downloads.example.invalid/probe.tar.gz'; do
    grep -Fq -- "$wget_argument" "$DOWNLOADER_LOG" ||
        fail "wget fallback omitted a reviewed HTTPS download argument: $wget_argument"
done

printf '%s\n' '#!/bin/sh' 'exit 2' > "$DOWNLOADER_STUB_ROOT/curl"
chmod 0755 "$DOWNLOADER_STUB_ROOT/curl"
: > "$DOWNLOADER_LOG"
PATH=$DOWNLOADER_STUB_ROOT PROBE_DOWNLOADER_LOG=$DOWNLOADER_LOG /bin/bash -c '
    source "$1"
    download_file "$2" https://downloads.example.invalid/probe.tar.gz 60
' probe-legacy-curl-wget-fallback "$WITHOUT_ENTRYPOINT" "$TEST_ROOT/wget-download-old-curl" ||
    fail 'an unsupported curl prevented the reviewed wget fallback'
grep -Fq -- 'https://downloads.example.invalid/probe.tar.gz' "$DOWNLOADER_LOG" ||
    fail 'wget did not receive the release URL after legacy curl feature detection failed'

# The release extractor must remain usable with Python 3.5 on the oldest
# accepted hosts. It performs descriptor-relative extraction itself; modern
# tarfile filters and extractall are intentionally outside this contract.
SAFE_EXTRACT_SOURCE=$TEST_ROOT/safe-extract-source
sed -n '/^safe_extract_archive()/,/^}/p' "$INSTALLER" > "$SAFE_EXTRACT_SOURCE"
for extraction_contract in \
    'MAX_MEMBERS = 20000' \
    'MAX_FILE_BYTES = 536870912' \
    'MAX_EXPANDED_BYTES = 2147483648' \
    'member.pax_headers.get("path")' \
    'member.pax_headers.get("linkpath")' \
    'member.issym() or member.islnk()' \
    'devices, FIFOs, and other special files are forbidden' \
    'os.O_NOFOLLOW' \
    'dir_fd=' \
    'os.O_EXCL' \
    'bundle.extractfile(member)' \
    'os.fchmod'; do
    assert_contains "$extraction_contract" "$SAFE_EXTRACT_SOURCE"
done
for forbidden_extraction_contract in \
    pathlib PurePosixPath commonpath extractall 'filter=' getmembers; do
    if grep -Fq -- "$forbidden_extraction_contract" "$SAFE_EXTRACT_SOURCE"; then
        fail "safe release extraction uses unsupported or unsafe helper: $forbidden_extraction_contract"
    fi
done
if grep -Eq "f[\"']" "$SAFE_EXTRACT_SOURCE"; then
    fail 'safe release extraction contains a Python f-string and is not Python 3.5 compatible'
fi

ARCHIVE_FIXTURE_ROOT=$TEST_ROOT/archive-fixtures
ARCHIVE_DEST_ROOT=$TEST_ROOT/archive-destinations
mkdir "$ARCHIVE_FIXTURE_ROOT" "$ARCHIVE_DEST_ROOT"
python3 - "$ARCHIVE_FIXTURE_ROOT" <<'PY'
import gzip
import io
import os
import sys
import tarfile

fixture_root = sys.argv[1]


def archive_path(name):
    return os.path.join(fixture_root, name + ".tar.gz")


def add_directory(bundle, name, mode=0o755, pax_headers=None):
    member = tarfile.TarInfo(name)
    member.type = tarfile.DIRTYPE
    member.mode = mode
    member.size = 0
    member.pax_headers = dict(pax_headers or {})
    bundle.addfile(member)


def add_file(bundle, name, payload=b"fixture", mode=0o644, pax_headers=None):
    member = tarfile.TarInfo(name)
    member.mode = mode
    member.size = len(payload)
    member.pax_headers = dict(pax_headers or {})
    bundle.addfile(member, io.BytesIO(payload))


def add_link(bundle, name, linkname, link_type, pax_headers=None):
    member = tarfile.TarInfo(name)
    member.type = link_type
    member.linkname = linkname
    member.mode = 0o777
    member.size = 0
    member.pax_headers = dict(pax_headers or {})
    bundle.addfile(member)


def one_file_archive(name, member_name, pax_headers=None):
    with tarfile.open(archive_path(name), "w:gz", format=tarfile.PAX_FORMAT) as bundle:
        add_file(bundle, member_name, pax_headers=pax_headers)


with tarfile.open(archive_path("good"), "w:gz", format=tarfile.PAX_FORMAT) as bundle:
    add_directory(bundle, "bundle/", 0o777)
    add_directory(bundle, "bundle/bin/", 0o777)
    add_file(bundle, "bundle/bin/app", b"application", 0o6777)
    add_file(bundle, "bundle/config", b"configuration", 0o666)
    add_file(bundle, "bundle/share/implicit-parent", b"implicit", 0o644)
    add_file(
        bundle,
        "pax-placeholder",
        b"pax-safe",
        0o644,
        {"path": "bundle/pax-safe"},
    )

one_file_archive("traversal", "../outside-created-by-archive")
one_file_archive("absolute", os.path.join(fixture_root, "absolute-escape"))
one_file_archive("dot-component", "bundle/./file")
one_file_archive("pax-traversal", "bundle/safe", {"path": "../pax-escape"})

with tarfile.open(archive_path("duplicate"), "w:gz", format=tarfile.PAX_FORMAT) as bundle:
    add_file(bundle, "bundle/duplicate", b"first")
    add_file(bundle, "bundle/duplicate", b"second")

with tarfile.open(archive_path("duplicate-alias"), "w:gz", format=tarfile.PAX_FORMAT) as bundle:
    add_directory(bundle, "bundle")
    add_directory(bundle, "bundle/")

with tarfile.open(archive_path("symlink"), "w:gz", format=tarfile.PAX_FORMAT) as bundle:
    add_link(bundle, "bundle/link", "../../outside", tarfile.SYMTYPE)

with tarfile.open(archive_path("hardlink"), "w:gz", format=tarfile.PAX_FORMAT) as bundle:
    add_link(bundle, "bundle/link", "bundle/target", tarfile.LNKTYPE)

with tarfile.open(archive_path("pax-linkpath"), "w:gz", format=tarfile.PAX_FORMAT) as bundle:
    add_link(
        bundle,
        "bundle/link",
        "bundle/target",
        tarfile.SYMTYPE,
        {"linkpath": "../pax-link-escape"},
    )

with tarfile.open(archive_path("fifo"), "w:gz", format=tarfile.PAX_FORMAT) as bundle:
    member = tarfile.TarInfo("bundle/fifo")
    member.type = tarfile.FIFOTYPE
    member.mode = 0o600
    member.size = 0
    bundle.addfile(member)

with tarfile.open(archive_path("device"), "w:gz", format=tarfile.PAX_FORMAT) as bundle:
    member = tarfile.TarInfo("bundle/device")
    member.type = tarfile.CHRTYPE
    member.devmajor = 1
    member.devminor = 3
    member.mode = 0o600
    member.size = 0
    bundle.addfile(member)

with tarfile.open(archive_path("empty"), "w:gz", format=tarfile.PAX_FORMAT):
    pass

oversized = tarfile.TarInfo("bundle/oversized")
oversized.mode = 0o600
oversized.size = 536870913
with gzip.open(archive_path("oversized"), "wb") as output:
    output.write(oversized.tobuf(format=tarfile.USTAR_FORMAT))
    output.write(b"\0" * 1024)
PY

run_safe_extract_contract() {
    fixture_archive=$1
    fixture_destination=$2
    /bin/bash -c '
        source "$1"
        safe_extract_archive "$2" "$3"
    ' probe-safe-extract "$WITHOUT_ENTRYPOINT" "$fixture_archive" "$fixture_destination"
}

GOOD_ARCHIVE_DEST=$ARCHIVE_DEST_ROOT/good
mkdir -m 0700 "$GOOD_ARCHIVE_DEST"
run_safe_extract_contract "$ARCHIVE_FIXTURE_ROOT/good.tar.gz" "$GOOD_ARCHIVE_DEST" ||
    fail 'safe release extractor rejected the valid archive fixture'
[ "$(cat "$GOOD_ARCHIVE_DEST/bundle/bin/app")" = application ] ||
    fail 'safe release extractor did not copy the regular file payload'
[ "$(cat "$GOOD_ARCHIVE_DEST/bundle/pax-safe")" = pax-safe ] ||
    fail 'safe release extractor did not validate and copy a safe PAX path override'
[ "$(stat -c '%a' "$GOOD_ARCHIVE_DEST/bundle/bin/app")" = 755 ] ||
    fail 'safe release extractor did not strip special and group/world-write file mode bits'
[ "$(stat -c '%a' "$GOOD_ARCHIVE_DEST/bundle/config")" = 644 ] ||
    fail 'safe release extractor did not sanitize a writable data-file mode'
[ "$(stat -c '%a' "$GOOD_ARCHIVE_DEST/bundle")" = 755 ] ||
    fail 'safe release extractor did not sanitize the archive directory mode'
[ "$(stat -c '%a' "$GOOD_ARCHIVE_DEST/bundle/share")" = 700 ] ||
    fail 'safe release extractor did not keep an implicit parent directory private'

for malicious_archive in \
    traversal absolute dot-component pax-traversal duplicate duplicate-alias \
    symlink hardlink pax-linkpath fifo device empty oversized; do
    malicious_destination=$ARCHIVE_DEST_ROOT/$malicious_archive
    malicious_output=$ARCHIVE_DEST_ROOT/$malicious_archive.output
    mkdir -m 0700 "$malicious_destination"
    if run_safe_extract_contract \
        "$ARCHIVE_FIXTURE_ROOT/$malicious_archive.tar.gz" \
        "$malicious_destination" >"$malicious_output" 2>&1; then
        fail "safe release extractor accepted malicious archive: $malicious_archive"
    fi
    grep -Fq 'release archive extraction' "$malicious_output" ||
        fail "malicious archive did not fail through the safe extractor: $malicious_archive"
done
[ ! -e "$ARCHIVE_DEST_ROOT/outside-created-by-archive" ] ||
    fail 'archive traversal fixture created a file outside its extraction root'
[ ! -e "$ARCHIVE_FIXTURE_ROOT/absolute-escape" ] ||
    fail 'absolute archive path fixture created its target'

ARCHIVE_PARENT_DEST=$ARCHIVE_DEST_ROOT/symlink-parent
ARCHIVE_PARENT_OUTSIDE=$ARCHIVE_DEST_ROOT/symlink-parent-outside
mkdir -m 0700 "$ARCHIVE_PARENT_DEST" "$ARCHIVE_PARENT_OUTSIDE"
ln -s "$ARCHIVE_PARENT_OUTSIDE" "$ARCHIVE_PARENT_DEST/bundle"
if run_safe_extract_contract "$ARCHIVE_FIXTURE_ROOT/good.tar.gz" "$ARCHIVE_PARENT_DEST" \
    >/dev/null 2>&1; then
    fail 'safe release extractor followed a pre-existing archive parent symlink'
fi
[ -z "$(find "$ARCHIVE_PARENT_OUTSIDE" -mindepth 1 -print -quit)" ] ||
    fail 'safe release extractor wrote through a pre-existing archive parent symlink'

ARCHIVE_INPUT_LINK=$ARCHIVE_FIXTURE_ROOT/input-link.tar.gz
ARCHIVE_DEST_LINK=$ARCHIVE_DEST_ROOT/destination-link
ARCHIVE_DEST_REAL=$ARCHIVE_DEST_ROOT/destination-real
ln -s good.tar.gz "$ARCHIVE_INPUT_LINK"
mkdir -m 0700 "$ARCHIVE_DEST_REAL"
ln -s "$ARCHIVE_DEST_REAL" "$ARCHIVE_DEST_LINK"
if run_safe_extract_contract "$ARCHIVE_INPUT_LINK" "$ARCHIVE_DEST_REAL" >/dev/null 2>&1; then
    fail 'safe release extractor followed a symlink archive input'
fi
if run_safe_extract_contract "$ARCHIVE_FIXTURE_ROOT/good.tar.gz" "$ARCHIVE_DEST_LINK" \
    >/dev/null 2>&1; then
    fail 'safe release extractor followed a symlink extraction root'
fi

/bin/bash -c '
    source "$1"
    [[ "$PANEL_PROFILE" == management ]]
    [[ "$PANEL_VERSION" == v1.2.0 ]]
    [[ "$(release_bundle_name management v1.2.0 amd64)" == probe-panel-management-v1.2.0-linux-amd64 ]]
    [[ "$(release_asset_name management v1.2.0 arm64)" == probe-panel-management-v1.2.0-linux-arm64.tar.gz ]]
    if (release_asset_name full v1.1.0 arm64) >/dev/null 2>&1; then
        exit 20
    fi
' probe-release-profile-default "$WITHOUT_ENTRYPOINT" ||
    fail 'management-only bundle naming contract failed'
if PROBE_PANEL_RELEASE_PROFILE=full /bin/bash -c '
    source "$1"
    validate_release_settings
' probe-release-profile-reject-full "$WITHOUT_ENTRYPOINT" >/dev/null 2>&1; then
    fail 'v1.2 installer accepted the historical full profile override'
fi

# Platform selection is an exact ID+VERSION_ID+NAME whitelist. ID_LIKE never
# grants candidate acceptance, CentOS Linux and Stream are distinct products,
# and the selector parses data rather than sourcing shell syntax.
CANONICAL_PLATFORM_IDS='debian-9-systemd,debian-10-systemd,debian-11-systemd,debian-12-systemd,debian-13-systemd,ubuntu-18.04-systemd,ubuntu-20.04-systemd,ubuntu-22.04-systemd,ubuntu-24.04-systemd,ubuntu-26.04-systemd,centos-linux-7-systemd,centos-linux-8-systemd,centos-stream-8-systemd,centos-stream-9-systemd,centos-stream-10-systemd'
PLATFORM_FIXTURE_ROOT=$TEST_ROOT/platform-fixtures
mkdir "$PLATFORM_FIXTURE_ROOT"
for debian_version in 9 10 11 12 13; do
    printf '%s\n' 'NAME="Debian GNU/Linux"' 'ID=debian' "VERSION_ID=\"$debian_version\"" \
        > "$PLATFORM_FIXTURE_ROOT/debian-$debian_version"
done
for ubuntu_version in 18.04 20.04 22.04 24.04 26.04; do
    printf '%s\n' 'NAME="Ubuntu"' 'ID=ubuntu' "VERSION_ID=\"$ubuntu_version\"" \
        > "$PLATFORM_FIXTURE_ROOT/ubuntu-$ubuntu_version"
done
printf '%s\n' 'NAME="CentOS Linux"' 'ID=centos' 'VERSION_ID="7"' \
    > "$PLATFORM_FIXTURE_ROOT/centos-linux-7"
printf '%s\n' 'NAME="CentOS Linux"' 'ID=centos' 'VERSION_ID="8"' \
    > "$PLATFORM_FIXTURE_ROOT/centos-linux-8"
for stream_version in 8 9 10; do
    printf '%s\n' 'NAME="CentOS Stream"' 'ID=centos' "VERSION_ID=\"$stream_version\"" \
        > "$PLATFORM_FIXTURE_ROOT/centos-stream-$stream_version"
done

run_supported_platform_contract() {
    fixture=$1
    shift
    /bin/bash -c '
        source "$1"
        select_supported_platform "$2"
        case "$3" in
            debian-*) expected_adapter=debian ;;
            ubuntu-*) expected_adapter=ubuntu ;;
            centos-*) expected_adapter=centos ;;
            *) exit 88 ;;
        esac
        [[ "$PLATFORM_ID" == "$3" ]]
        [[ "$PLATFORM_ADAPTER" == "$expected_adapter" ]]
        [[ "$PLATFORM_CONTRACT" == probe-linux-systemd-v2 ]]
        [[ "$PLATFORM_NGINX_INSTALL_PACKAGE" == "$4" ]]
        [[ "$PLATFORM_NGINX_BINARY_PACKAGE" == "$5" ]]
        [[ "$PLATFORM_NGINX_DIALECT" == "$6" ]]
        [[ "$PLATFORM_SYSTEMD_MIN_VERSION" == "$7" ]]
        [[ "$PLATFORM_SYSTEMD_PROFILE" == "$8" ]]
        [[ "$PLATFORM_PACKAGE_FAMILY" == "$9" ]]
        [[ "$PLATFORM_PACKAGE_MANAGER" == "${10}" ]]
        [[ "$PLATFORM_POSTGRES_SERVER_PACKAGE" == "${11}" ]]
        [[ "$PLATFORM_POSTGRES_CLIENT_PACKAGE" == "${12}" ]]
        [[ "$PLATFORM_POSTGRES_SERVICE" == "${13}" ]]
        [[ "$PLATFORM_POSTGRES_UNIT" == "${13}" ]]
        [[ "$PLATFORM_PSQL" == "${14}" ]]
        [[ "${PLATFORM_PG_ISREADY%/pg_isready}" == "${PLATFORM_PSQL%/psql}" ]]
        [[ "$PLATFORM_CERTBOT_TIMER" == "${15}" ]]
        [[ "$PLATFORM_EOL" == "${16}" ]]
        [[ "$SUPPORTED_PLATFORM_IDS" == "${17}" ]]
        if [[ "$PLATFORM_PACKAGE_FAMILY" == deb ]]; then
            [[ "$PLATFORM_NOLOGIN_SHELL" == /usr/sbin/nologin ]]
        else
            [[ "$PLATFORM_NOLOGIN_SHELL" == /sbin/nologin ]]
        fi
    ' probe-platform-selector "$WITHOUT_ENTRYPOINT" "$fixture" "$@" "$CANONICAL_PLATFORM_IDS"
}

while IFS='|' read -r fixture_name expected_id nginx_install nginx_owner nginx_dialect \
    systemd_minimum systemd_profile package_family package_manager postgres_server \
    postgres_client postgres_service psql_path certbot_timer eol_tier; do
    [ -n "$fixture_name" ] || continue
    run_supported_platform_contract "$PLATFORM_FIXTURE_ROOT/$fixture_name" \
        "$expected_id" "$nginx_install" "$nginx_owner" "$nginx_dialect" \
        "$systemd_minimum" "$systemd_profile" "$package_family" "$package_manager" \
        "$postgres_server" "$postgres_client" "$postgres_service" "$psql_path" \
        "$certbot_timer" "$eol_tier" ||
        fail "reviewed platform fixture was rejected or mapped incorrectly: $fixture_name"
done <<'EOF'
debian-9|debian-9-systemd|nginx-full|nginx-full|classic|232|legacy|deb|apt-get|postgresql-14|postgresql-client-14|postgresql.service|/usr/bin/psql|certbot.timer|1
debian-10|debian-10-systemd|nginx-full|nginx-full|legacy|241|legacy|deb|apt-get|postgresql-14|postgresql-client-14|postgresql.service|/usr/bin/psql|certbot.timer|1
debian-11|debian-11-systemd|nginx-core|nginx-core|legacy|247|legacy|deb|apt-get|postgresql-14|postgresql-client-14|postgresql.service|/usr/bin/psql|certbot.timer|1
debian-12|debian-12-systemd|nginx|nginx|legacy|252|modern|deb|apt-get|postgresql-14|postgresql-client-14|postgresql.service|/usr/bin/psql|certbot.timer|1
debian-13|debian-13-systemd|nginx|nginx|modern|257|modern|deb|apt-get|postgresql-14|postgresql-client-14|postgresql.service|/usr/bin/psql|certbot.timer|0
ubuntu-18.04|ubuntu-18.04-systemd|nginx-core|nginx-core|legacy|237|legacy|deb|apt-get|postgresql-14|postgresql-client-14|postgresql.service|/usr/bin/psql|certbot.timer|1
ubuntu-20.04|ubuntu-20.04-systemd|nginx-core|nginx-core|legacy|245|legacy|deb|apt-get|postgresql-14|postgresql-client-14|postgresql.service|/usr/bin/psql|certbot.timer|1
ubuntu-22.04|ubuntu-22.04-systemd|nginx-core|nginx-core|legacy|249|modern|deb|apt-get|postgresql-14|postgresql-client-14|postgresql.service|/usr/bin/psql|certbot.timer|0
ubuntu-24.04|ubuntu-24.04-systemd|nginx|nginx|legacy|255|modern|deb|apt-get|postgresql-14|postgresql-client-14|postgresql.service|/usr/bin/psql|certbot.timer|0
ubuntu-26.04|ubuntu-26.04-systemd|nginx|nginx|modern|259|modern|deb|apt-get|postgresql-14|postgresql-client-14|postgresql.service|/usr/bin/psql|certbot.timer|0
centos-linux-7|centos-linux-7-systemd|nginx|nginx|classic|219|legacy|rpm|yum|postgresql14-server|postgresql14|postgresql-14.service|/usr/pgsql-14/bin/psql|certbot-renew.timer|1
centos-linux-8|centos-linux-8-systemd|nginx|nginx-core|legacy|239|legacy|rpm|dnf|postgresql14-server|postgresql14|postgresql-14.service|/usr/pgsql-14/bin/psql|certbot-renew.timer|1
centos-stream-8|centos-stream-8-systemd|nginx|nginx-core|legacy|239|legacy|rpm|dnf|postgresql14-server|postgresql14|postgresql-14.service|/usr/pgsql-14/bin/psql|certbot-renew.timer|1
centos-stream-9|centos-stream-9-systemd|nginx|nginx-core|legacy|252|modern|rpm|dnf|postgresql14-server|postgresql14|postgresql-14.service|/usr/pgsql-14/bin/psql|certbot-renew.timer|0
centos-stream-10|centos-stream-10-systemd|nginx|nginx-core|modern|257|modern|rpm|dnf|postgresql14-server|postgresql14|postgresql-14.service|/usr/pgsql-14/bin/psql|certbot-renew.timer|0
EOF

if /bin/bash -c '
    source "$1"
    debian_platform_configure() {
        PLATFORM_ADAPTER=ubuntu
        configure_deb_platform debian-13-systemd 257 modern nginx nginx modern postgresql-14 postgresql-client-14
    }
    select_supported_platform "$2"
' probe-platform-adapter-identity "$WITHOUT_ENTRYPOINT" \
    "$PLATFORM_FIXTURE_ROOT/debian-13" >/dev/null 2>&1; then
    fail 'platform adapter was allowed to change its own dispatch identity'
fi

# The systemd and Nginx compatibility axes are independent: Debian 12 uses
# modern units with the legacy Nginx grammar, while Debian 9 uses both legacy
# units and the classic Nginx grammar.
/bin/bash -c '
    source "$1"
    select_supported_platform "$2"
    [[ "$(selected_setup_asset_name probe-panel-setup.service)" == probe-panel-setup-legacy.service ]]
    [[ "$(selected_setup_asset_name probe-panel-setup.socket)" == probe-panel-setup-legacy.socket ]]
    [[ "$(selected_setup_asset_name probe-panel-finalizer-management.service)" == probe-panel-finalizer-management-legacy.service ]]
    select_supported_platform "$3"
    [[ "$PLATFORM_NGINX_DIALECT" == legacy ]]
    [[ "$(selected_setup_asset_name probe-panel-setup.service)" == probe-panel-setup.service ]]
' probe-platform-asset-selector "$WITHOUT_ENTRYPOINT" \
    "$PLATFORM_FIXTURE_ROOT/debian-9" "$PLATFORM_FIXTURE_ROOT/debian-12" ||
    fail 'classic/legacy/modern setup asset selection contract failed'

for modern_service in "$SETUP_UNIT" "$FINALIZER_UNIT" "$API_SYSTEMD_UNIT" "$BACKUP_SYSTEMD_UNIT"; do
    assert_contains 'ProtectSystem=strict' "$modern_service"
done
assert_contains 'FileDescriptorName=setup-http' "$SETUP_SOCKET"
assert_contains 'RandomizedDelaySec=15m' "$BACKUP_TIMER"
for legacy_service in \
    "$SETUP_LEGACY_UNIT" "$FINALIZER_LEGACY_UNIT" \
    "$API_LEGACY_SYSTEMD_UNIT" "$BACKUP_LEGACY_SYSTEMD_UNIT"; do
    assert_contains 'ProtectSystem=full' "$legacy_service"
    if grep -Eq '^(AmbientCapabilities|ProtectClock|ProtectHostname|ProtectKernelLogs|ProtectProc|SocketBindAllow|SocketBindDeny)=' "$legacy_service"; then
        fail "systemd 219 compatibility unit contains a modern-only directive: $legacy_service"
    fi
done
if grep -Fq 'FileDescriptorName=' "$SETUP_LEGACY_SOCKET"; then
    fail 'systemd 219 setup socket must not require FileDescriptorName'
fi
if grep -Fq 'RandomizedDelaySec=' "$BACKUP_LEGACY_TIMER"; then
    fail 'systemd 219 backup timer must not require RandomizedDelaySec'
fi

# These are literal Nginx variables, not test-process expansions.
# shellcheck disable=SC2016
for modern_template in "$MANAGEMENT_NGINX" "$MANAGEMENT_IP_NGINX"; do
    assert_contains '    http2 on;' "$modern_template"
    assert_contains '    ssl_protocols TLSv1.2 TLSv1.3;' "$modern_template"
    assert_contains '"request_id":"$request_id"' "$modern_template"
done
assert_contains '    listen 443 ssl http2;' "$MANAGEMENT_LEGACY_NGINX"
assert_contains '    listen 18455 ssl http2 default_server;' "$MANAGEMENT_LEGACY_IP_NGINX"
# shellcheck disable=SC2016
for legacy_template in "$MANAGEMENT_LEGACY_NGINX" "$MANAGEMENT_LEGACY_IP_NGINX"; do
    assert_contains '    ssl_protocols TLSv1.2 TLSv1.3;' "$legacy_template"
    assert_contains '"request_id":"$request_id"' "$legacy_template"
    if grep -Fq '    http2 on;' "$legacy_template"; then
        fail "legacy Nginx template contains the modern standalone HTTP/2 directive: $legacy_template"
    fi
done
assert_contains '    listen 443 ssl http2;' "$MANAGEMENT_CLASSIC_NGINX"
assert_contains '    listen 18455 ssl http2 default_server;' "$MANAGEMENT_CLASSIC_IP_NGINX"
# shellcheck disable=SC2016
for classic_template in "$MANAGEMENT_CLASSIC_NGINX" "$MANAGEMENT_CLASSIC_IP_NGINX"; do
    assert_contains '    ssl_protocols TLSv1.2;' "$classic_template"
    if grep -Fq 'TLSv1.3' "$classic_template" || grep -Fq '$request_id' "$classic_template"; then
        fail "classic Nginx template requires syntax unavailable on the reviewed classic runtime: $classic_template"
    fi
done

# Lifecycle-restricted candidates require an explicit acknowledgement. The
# acknowledgement changes no package source and must never be accepted for a
# non-install command or more than once.
for eol_fixture in debian-9 debian-10 debian-11 debian-12 ubuntu-18.04 ubuntu-20.04 centos-linux-7 centos-linux-8 centos-stream-8; do
    if /bin/bash -c '
        source "$1"
        select_supported_platform "$2"
        ACCEPT_EOL=0
        validate_platform_lifecycle
    ' probe-eol-reject "$WITHOUT_ENTRYPOINT" "$PLATFORM_FIXTURE_ROOT/$eol_fixture" \
        >/dev/null 2>&1; then
        fail "EOL platform did not require --accept-eol: $eol_fixture"
    fi
    /bin/bash -c '
        source "$1"
        select_supported_platform "$2"
        ACCEPT_EOL=1
        validate_platform_lifecycle
    ' probe-eol-accept "$WITHOUT_ENTRYPOINT" "$PLATFORM_FIXTURE_ROOT/$eol_fixture" \
        >/dev/null 2>&1 || fail "--accept-eol did not acknowledge reviewed EOL platform: $eol_fixture"
done
/bin/bash -c '
    source "$1"
    select_supported_platform "$2"
    ACCEPT_EOL=0
    validate_platform_lifecycle
' probe-maintained-lifecycle "$WITHOUT_ENTRYPOINT" "$PLATFORM_FIXTURE_ROOT/debian-13" ||
    fail 'maintained platform incorrectly required --accept-eol'

mkdir -p "$PLATFORM_FIXTURE_ROOT/symlink/etc" "$PLATFORM_FIXTURE_ROOT/symlink/usr/lib"
printf '%s\n' 'NAME="Debian GNU/Linux"' 'ID=debian' 'VERSION_ID="13"' \
    > "$PLATFORM_FIXTURE_ROOT/symlink/usr/lib/os-release"
ln -s ../usr/lib/os-release "$PLATFORM_FIXTURE_ROOT/symlink/etc/os-release"
run_supported_platform_contract "$PLATFORM_FIXTURE_ROOT/symlink/etc/os-release" \
    debian-13-systemd nginx nginx modern 257 modern deb apt-get postgresql-14 \
    postgresql-client-14 postgresql.service /usr/bin/psql certbot.timer 0 ||
    fail 'selector rejected a standards-compliant relative os-release symlink'

printf '%s\n' 'ID=debian' 'VERSION_ID="8"' > "$PLATFORM_FIXTURE_ROOT/debian-8"
printf '%s\n' 'ID=debian' 'VERSION_ID="14"' > "$PLATFORM_FIXTURE_ROOT/debian-14"
printf '%s\n' 'ID=ubuntu' 'VERSION_ID="18.10"' > "$PLATFORM_FIXTURE_ROOT/ubuntu-18.10"
printf '%s\n' 'ID=ubuntu' 'VERSION_ID="28.04"' > "$PLATFORM_FIXTURE_ROOT/ubuntu-28.04"
printf '%s\n' 'NAME="CentOS Linux"' 'ID=centos' 'VERSION_ID="9"' > "$PLATFORM_FIXTURE_ROOT/centos-linux-9"
printf '%s\n' 'NAME="CentOS Stream"' 'ID=centos' 'VERSION_ID="7"' > "$PLATFORM_FIXTURE_ROOT/centos-stream-7"
printf '%s\n' 'NAME="CentOS Stream"' 'ID=centos' 'VERSION_ID="11"' > "$PLATFORM_FIXTURE_ROOT/centos-stream-11"
printf '%s\n' 'ID=centos' 'VERSION_ID="8"' > "$PLATFORM_FIXTURE_ROOT/centos-missing-name"
printf '%s\n' 'NAME="Rocky Linux"' 'ID=centos' 'VERSION_ID="8"' > "$PLATFORM_FIXTURE_ROOT/centos-wrong-name"
printf '%s\n' 'NAME="CentOS Linux"' 'NAME="CentOS Stream"' 'ID=centos' 'VERSION_ID="8"' > "$PLATFORM_FIXTURE_ROOT/duplicate-name"
printf '%s\n' 'ID=Ubuntu' 'VERSION_ID="24.04"' > "$PLATFORM_FIXTURE_ROOT/noncanonical-case"
printf '%s\n' 'ID=ubuntu' 'ID=debian' 'VERSION_ID="24.04"' > "$PLATFORM_FIXTURE_ROOT/duplicate-id"
printf '%s\n' 'ID=linuxmint' 'VERSION_ID="22"' 'ID_LIKE="ubuntu debian"' > "$PLATFORM_FIXTURE_ROOT/derived-id-like"
printf '%s\n' 'ID=ubuntu' 'ID_LIKE=debian' > "$PLATFORM_FIXTURE_ROOT/missing-version"
for rejected_platform in \
    debian-8 debian-14 ubuntu-18.10 ubuntu-28.04 centos-linux-9 centos-stream-7 \
    centos-stream-11 centos-missing-name centos-wrong-name duplicate-name \
    noncanonical-case duplicate-id derived-id-like missing-version; do
    if /bin/bash -c '
        source "$1"
        select_supported_platform "$2"
    ' probe-platform-rejection "$WITHOUT_ENTRYPOINT" "$PLATFORM_FIXTURE_ROOT/$rejected_platform" \
        >/dev/null 2>&1; then
        fail "out-of-matrix platform fixture was accepted: $rejected_platform"
    fi
done

PLATFORM_EXECUTION_MARKER=$TEST_ROOT/os-release-was-executed
printf '%s\n' \
    "ID=\$(touch $PLATFORM_EXECUTION_MARKER)" \
    'VERSION_ID="13"' \
    > "$PLATFORM_FIXTURE_ROOT/shell-syntax"
if /bin/bash -c '
    source "$1"
    select_supported_platform "$2"
' probe-platform-data-only "$WITHOUT_ENTRYPOINT" "$PLATFORM_FIXTURE_ROOT/shell-syntax" \
    >/dev/null 2>&1; then
    fail 'selector accepted executable shell syntax in os-release data'
fi
[ ! -e "$PLATFORM_EXECUTION_MARKER" ] ||
    fail 'selector executed shell syntax from os-release instead of parsing data'
PLATFORM_NAME_EXECUTION_MARKER=$TEST_ROOT/os-release-name-was-executed
printf '%s\n' \
    "NAME=\$(touch $PLATFORM_NAME_EXECUTION_MARKER)" \
    'ID=centos' \
    'VERSION_ID="8"' \
    > "$PLATFORM_FIXTURE_ROOT/name-shell-syntax"
if /bin/bash -c '
    source "$1"
    select_supported_platform "$2"
' probe-platform-name-data-only "$WITHOUT_ENTRYPOINT" \
    "$PLATFORM_FIXTURE_ROOT/name-shell-syntax" >/dev/null 2>&1; then
    fail 'selector accepted executable shell syntax in os-release NAME data'
fi
[ ! -e "$PLATFORM_NAME_EXECUTION_MARKER" ] ||
    fail 'selector executed shell syntax from os-release NAME instead of parsing data'

run_systemd_version_contract() {
    fixture=$1
    reported_version=$2
    /bin/bash -c '
        source "$1"
        select_supported_platform "$2"
        PROBE_REPORTED_SYSTEMD_VERSION=$3
        systemctl() { printf "%s\n" "systemd $PROBE_REPORTED_SYSTEMD_VERSION ($PROBE_REPORTED_SYSTEMD_VERSION.0)" "+PAM"; }
        assert_supported_systemd_version
    ' probe-systemd-minimum "$WITHOUT_ENTRYPOINT" "$fixture" "$reported_version"
}
run_systemd_version_contract "$PLATFORM_FIXTURE_ROOT/centos-linux-7" 219 ||
    fail 'CentOS Linux 7 systemd 219 minimum fixture was rejected'
run_systemd_version_contract "$PLATFORM_FIXTURE_ROOT/debian-9" 232 ||
    fail 'Debian 9 systemd 232 minimum fixture was rejected'
run_systemd_version_contract "$PLATFORM_FIXTURE_ROOT/ubuntu-26.04" 259 ||
    fail 'Ubuntu 26.04 systemd 259 minimum fixture was rejected'
for too_old_contract in \
    "$PLATFORM_FIXTURE_ROOT/centos-linux-7:218" \
    "$PLATFORM_FIXTURE_ROOT/debian-9:231" \
    "$PLATFORM_FIXTURE_ROOT/ubuntu-26.04:258"; do
    fixture=${too_old_contract%:*}
    reported_version=${too_old_contract##*:}
    if run_systemd_version_contract "$fixture" "$reported_version" >/dev/null 2>&1; then
        fail "platform accepted systemd below its exact minimum: $too_old_contract"
    fi
done

PLATFORM_MANIFEST_ROOT=$TEST_ROOT/platform-manifests
mkdir "$PLATFORM_MANIFEST_ROOT"
printf '%s\n' \
    'runtime_abi=probe-linux-systemd-v2' \
    "platform_ids=$CANONICAL_PLATFORM_IDS" \
    > "$PLATFORM_MANIFEST_ROOT/valid"
/bin/bash -c '
    source "$1"
    select_supported_platform "$2"
    validate_release_platform_metadata "$3"
' probe-platform-manifest "$WITHOUT_ENTRYPOINT" "$PLATFORM_FIXTURE_ROOT/ubuntu-22.04" \
    "$PLATFORM_MANIFEST_ROOT/valid" ||
    fail 'valid runtime ABI/platform ID manifest contract was rejected'
printf '%s\n' \
    'runtime_abi=foreign-runtime-v1' \
    "platform_ids=$CANONICAL_PLATFORM_IDS" \
    > "$PLATFORM_MANIFEST_ROOT/wrong-abi"
printf '%s\n' \
    'runtime_abi=probe-linux-systemd-v2' \
    'runtime_abi=probe-linux-systemd-v2' \
    "platform_ids=$CANONICAL_PLATFORM_IDS" \
    > "$PLATFORM_MANIFEST_ROOT/duplicate-abi"
printf '%s\n' \
    'runtime_abi=probe-linux-systemd-v2' \
    'platform_ids=centos-stream-10-systemd,debian-9-systemd,debian-10-systemd,debian-11-systemd,debian-12-systemd,debian-13-systemd,ubuntu-18.04-systemd,ubuntu-20.04-systemd,ubuntu-22.04-systemd,ubuntu-24.04-systemd,ubuntu-26.04-systemd,centos-linux-7-systemd,centos-linux-8-systemd,centos-stream-8-systemd,centos-stream-9-systemd' \
    > "$PLATFORM_MANIFEST_ROOT/reordered-platforms"
printf '%s\n' \
    'runtime_abi=probe-linux-systemd-v2' \
    "platform_ids=$CANONICAL_PLATFORM_IDS" \
    "platform_ids=$CANONICAL_PLATFORM_IDS" \
    > "$PLATFORM_MANIFEST_ROOT/duplicate-platforms"
for rejected_manifest in wrong-abi duplicate-abi reordered-platforms duplicate-platforms; do
    if /bin/bash -c '
        source "$1"
        select_supported_platform "$2"
        validate_release_platform_metadata "$3"
    ' probe-platform-manifest-rejection "$WITHOUT_ENTRYPOINT" "$PLATFORM_FIXTURE_ROOT/debian-13" \
        "$PLATFORM_MANIFEST_ROOT/$rejected_manifest" >/dev/null 2>&1; then
        fail "invalid runtime ABI/platform manifest was accepted: $rejected_manifest"
    fi
done
if /bin/bash -c '
    source "$1"
    PLATFORM_CONTRACT=probe-linux-systemd-v2
    PLATFORM_ID=linuxmint-22-systemd
    validate_release_platform_metadata "$2"
' probe-platform-manifest-current-id-rejection "$WITHOUT_ENTRYPOINT" \
    "$PLATFORM_MANIFEST_ROOT/valid" >/dev/null 2>&1; then
    fail 'release manifest accepted a current platform ID outside its exact platform_ids contract'
fi

SETUP_ENV_CONTRACT_ROOT=$TEST_ROOT/setup-env-contract
mkdir "$SETUP_ENV_CONTRACT_ROOT"
/bin/bash -c '
    source "$1"
    SETUP_CONFIG_ROOT=$2
    SETUP_ENV_FILE=$2/setup.env
    SETUP_SERVER_IP=192.0.2.10
    INSTALLED_RELEASE=/srv/probe/releases/contract-release
    PLATFORM_CONTRACT=probe-linux-systemd-v2
    PLATFORM_ID=ubuntu-22.04-systemd
    chown() {
        [[ "$#" -eq 2 && "$1" == root:root && "$2" == "$SETUP_CONFIG_ROOT"/.setup.env.* ]]
    }
    write_setup_environment
    [[ "$(grep -Fxc "PROBE_SETUP_PLATFORM_ID=ubuntu-22.04-systemd" "$SETUP_ENV_FILE")" -eq 1 ]]
' probe-setup-platform-env "$WITHOUT_ENTRYPOINT" "$SETUP_ENV_CONTRACT_ROOT" ||
    fail 'setup environment did not persist the selected candidate platform ID exactly once'
if /bin/bash -c '
    source "$1"
    SETUP_CONFIG_ROOT=$2
    SETUP_ENV_FILE=$2/rejected.env
    PLATFORM_CONTRACT=probe-linux-systemd-v2
    PLATFORM_ID=linuxmint-22-systemd
    chown() { return 0; }
    write_setup_environment
' probe-setup-platform-env-rejection "$WITHOUT_ENTRYPOINT" "$SETUP_ENV_CONTRACT_ROOT" \
    >/dev/null 2>&1; then
    fail 'setup environment accepted an out-of-matrix platform ID'
fi

# Release availability, checksum integrity, and compatible existing runtimes
# are hard gates before apt, account creation, service changes, or permanent
# directory creation. Run the real install_action with only its read-only host
# probes replaced by deterministic fixtures; every mutating command is a trap.
INVALID_NGINX_BINARY=$TEST_ROOT/invalid-existing-nginx
INVALID_NGINX_CONFIG=$TEST_ROOT/invalid-existing-nginx.conf
INVALID_NGINX_INCLUDE=$TEST_ROOT/invalid-existing-nginx-conf.d
printf '%s\n' '#!/bin/sh' 'exit 1' > "$INVALID_NGINX_BINARY"
chmod 0755 "$INVALID_NGINX_BINARY"
printf '%s\n' "include $INVALID_NGINX_INCLUDE/*.conf;" > "$INVALID_NGINX_CONFIG"
mkdir "$INVALID_NGINX_INCLUDE"
run_pre_mutation_failure_contract() {
    failure_mode=$1
    mutation_marker=$2
    PROBE_PREFLIGHT_FAILURE_MODE=$failure_mode \
        PROBE_PREFLIGHT_MUTATION_MARKER=$mutation_marker \
        PROBE_INVALID_NGINX_BINARY=$INVALID_NGINX_BINARY \
        PROBE_INVALID_NGINX_CONFIG=$INVALID_NGINX_CONFIG \
        PROBE_INVALID_NGINX_INCLUDE=$INVALID_NGINX_INCLUDE \
        PROBE_UNSUPPORTED_PLATFORM_FIXTURE=$PLATFORM_FIXTURE_ROOT/debian-14 \
        PROBE_PRIMARY_PLATFORM_FIXTURE=$PLATFORM_FIXTURE_ROOT/debian-13 \
        PROBE_DRIFT_PLATFORM_FIXTURE=$PLATFORM_FIXTURE_ROOT/ubuntu-24.04 \
        PROBE_WRONG_PLATFORM_MANIFEST=$PLATFORM_MANIFEST_ROOT/wrong-abi \
        PROBE_REORDERED_PLATFORM_MANIFEST=$PLATFORM_MANIFEST_ROOT/reordered-platforms \
        /bin/bash -c '
        source "$1"

        selector_definition="$(declare -f select_supported_platform)"
        selector_definition="${selector_definition/select_supported_platform/select_supported_platform_fixture}"
        eval "$selector_definition"

        mark_host_mutation() {
            : > "$PROBE_PREFLIGHT_MUTATION_MARKER"
            return 97
        }
        has_permanent_path() {
            local argument
            for argument in "$@"; do
                case "$argument" in
                    /etc|/etc/*|/srv|/srv/*|/usr/local/lib/probe-panel|/usr/local/lib/probe-panel/*|\
                    /var/lib/probe-panel|/var/lib/probe-panel/*|/var/backups/probe-panel|/var/backups/probe-panel/*|\
                    /run/probe-panel-setup|/run/probe-panel-setup/*|/run/systemd/system|/run/systemd/system/*)
                        return 0
                        ;;
                esac
            done
            return 1
        }
        guarded_file_mutation() {
            local command_name=$1
            shift
            if has_permanent_path "$@"; then
                mark_host_mutation
            fi
            command "$command_name" "$@"
        }
        apt-get() { mark_host_mutation; }
        addgroup() { mark_host_mutation; }
        adduser() { mark_host_mutation; }
        install() { mark_host_mutation; }
        ensure_secure_directory() { mark_host_mutation; }
        ensure_shared_root_directory() { mark_host_mutation; }
        ensure_backup_parent_directory() { mark_host_mutation; }
        mkdir() { guarded_file_mutation mkdir "$@"; }
        mktemp() { guarded_file_mutation mktemp "$@"; }
        mv() { guarded_file_mutation mv "$@"; }
        cp() { guarded_file_mutation cp "$@"; }
        chown() { guarded_file_mutation chown "$@"; }
        chmod() { guarded_file_mutation chmod "$@"; }
        ln() { guarded_file_mutation ln "$@"; }
        unlink() { guarded_file_mutation unlink "$@"; }
        rm() { guarded_file_mutation rm "$@"; }
        find() { guarded_file_mutation find "$@"; }
        systemctl() {
            case "${1-}" in
                daemon-reload|disable|enable|mask|reload|reset-failed|restart|start|stop|unmask)
                    mark_host_mutation
                    ;;
                *)
                    return 0
                    ;;
            esac
        }

        require_root() { :; }
        PROBE_PLATFORM_SELECTION_COUNT=0
        select_supported_platform() {
            PROBE_PLATFORM_SELECTION_COUNT=$((PROBE_PLATFORM_SELECTION_COUNT + 1))
            case "$PROBE_PREFLIGHT_FAILURE_MODE" in
                unsupported-platform)
                    select_supported_platform_fixture "$PROBE_UNSUPPORTED_PLATFORM_FIXTURE"
                    ;;
                platform-drift)
                    if [[ "$PROBE_PLATFORM_SELECTION_COUNT" -eq 1 ]]; then
                        select_supported_platform_fixture "$PROBE_PRIMARY_PLATFORM_FIXTURE"
                    else
                        select_supported_platform_fixture "$PROBE_DRIFT_PLATFORM_FIXTURE"
                    fi
                    ;;
                *)
                    select_supported_platform_fixture "$PROBE_PRIMARY_PLATFORM_FIXTURE"
                    ;;
            esac
        }
        require_preflight_commands() { :; }
        validate_release_settings() { :; }
        acquire_bootstrap_lock() { :; }
        assert_fresh_target() { :; }
        preflight_systemd_host() { :; }
        capture_postgresql_start_state() {
            POSTGRESQL_STATE_CAPTURED=1
            POSTGRESQL_WAS_ACTIVE=0
        }
        preflight_existing_runtimes() {
            if [[ "$PROBE_PREFLIGHT_FAILURE_MODE" == incompatible-runtime ]]; then
                die "simulated incompatible existing runtime"
            fi
            if [[ "$PROBE_PREFLIGHT_FAILURE_MODE" == invalid-existing-nginx ]]; then
                assert_secure_preexisting_path() { :; }
                validate_existing_nginx_configuration \
                    "$PROBE_INVALID_NGINX_BINARY" \
                    "$PROBE_INVALID_NGINX_CONFIG" \
                    "$PROBE_INVALID_NGINX_INCLUDE"
                return
            fi
            NGINX_PREEXISTED=0
            POSTGRESQL_PREEXISTED=0
        }
        detect_server_ip() { SETUP_SERVER_IP=192.0.2.10; }
        detect_architecture() { printf "%s\n" amd64; }
        download_file() {
            local destination=$1
            if [[ "$PROBE_PREFLIGHT_FAILURE_MODE" == unpublished-release ]]; then
                printf "%s\n" "simulated unpublished release" >&2
                return 44
            fi
            case "$destination" in
                */SHA256SUMS)
                    if [[ "$PROBE_PREFLIGHT_FAILURE_MODE" == bad-checksum ]]; then
                        printf "%064d  %s\n" 0 "$asset_name" > "$destination"
                    else
                        local synthetic_hash
                        synthetic_hash="$(printf "%s\n" synthetic-release-archive | sha256sum)"
                        synthetic_hash="${synthetic_hash%% *}"
                        printf "%s  %s\n" "$synthetic_hash" "$asset_name" > "$destination"
                    fi
                    ;;
                *)
                    printf "%s\n" synthetic-release-archive > "$destination"
                    ;;
            esac
        }
        safe_extract_archive() {
            local destination=$2 bundle_name
            bundle_name="$(release_bundle_name "$PANEL_PROFILE" "$PANEL_VERSION" amd64)"
            mkdir -p -- "$destination/$bundle_name"
        }
        validate_release_bundle() {
            case "$PROBE_PREFLIGHT_FAILURE_MODE" in
                wrong-manifest)
                    validate_release_platform_metadata "$PROBE_WRONG_PLATFORM_MANIFEST"
                    ;;
                reordered-manifest)
                    validate_release_platform_metadata "$PROBE_REORDERED_PLATFORM_MANIFEST"
                    ;;
                *)
                    return 0
                    ;;
            esac
        }

        install_action
    ' probe-pre-mutation-failure "$WITHOUT_ENTRYPOINT"
}

for pre_mutation_failure in \
    unsupported-platform unpublished-release bad-checksum incompatible-runtime \
    invalid-existing-nginx wrong-manifest reordered-manifest platform-drift; do
    PRE_MUTATION_MARKER=$TEST_ROOT/pre-mutation-$pre_mutation_failure
    PRE_MUTATION_OUTPUT=$TEST_ROOT/pre-mutation-$pre_mutation_failure.output
    if run_pre_mutation_failure_contract "$pre_mutation_failure" "$PRE_MUTATION_MARKER" \
        >"$PRE_MUTATION_OUTPUT" 2>&1; then
        fail "$pre_mutation_failure fixture unexpectedly completed installation"
    fi
    [ ! -e "$PRE_MUTATION_MARKER" ] ||
        fail "$pre_mutation_failure triggered apt, account, service, or permanent-path mutation"
    case "$pre_mutation_failure" in
        unsupported-platform) expected_failure='accepted candidate platform IDs' ;;
        unpublished-release) expected_failure='simulated unpublished release' ;;
        bad-checksum) expected_failure='release archive SHA256 verification failed' ;;
        incompatible-runtime) expected_failure='simulated incompatible existing runtime' ;;
        invalid-existing-nginx) expected_failure='the existing native deb-family Nginx configuration is invalid' ;;
        wrong-manifest) expected_failure='runtime_abi=probe-linux-systemd-v2 exactly once' ;;
        reordered-manifest) expected_failure="platform_ids=$CANONICAL_PLATFORM_IDS exactly once" ;;
        platform-drift) expected_failure='the selected platform changed during release verification' ;;
    esac
    grep -Fq -- "$expected_failure" "$PRE_MUTATION_OUTPUT" ||
        fail "$pre_mutation_failure did not reach its intended pre-mutation rejection"
done

# Existing deb-family Nginx files/directories may keep their normal read-only modes,
# while group/world-write and special permission bits remain fail-closed.
PREFLIGHT_MODE_FILE=$TEST_ROOT/preflight-nginx.conf
PREFLIGHT_MODE_DIRECTORY=$TEST_ROOT/preflight-conf.d
: > "$PREFLIGHT_MODE_FILE"
mkdir "$PREFLIGHT_MODE_DIRECTORY"
run_preexisting_path_mode_contract() {
    fixture_path=$1
    fixture_type=$2
    fixture_mode=$3
    PROBE_PREFLIGHT_MODE_PATH=$fixture_path \
        PROBE_PREFLIGHT_MODE_VALUE=$fixture_mode \
        /bin/bash -c '
        source "$1"
        stat() {
            if [[ "$#" -eq 3 && "$1" == -c && "$3" == "$PROBE_PREFLIGHT_MODE_PATH" ]]; then
                case "$2" in
                    %u:%g) printf "%s\n" 0:0 ;;
                    %a) printf "%s\n" "$PROBE_PREFLIGHT_MODE_VALUE" ;;
                    *) return 96 ;;
                esac
                return
            fi
            command stat "$@"
        }
        assert_secure_preexisting_path "$2" "$3"
    ' probe-preexisting-path-mode "$WITHOUT_ENTRYPOINT" "$fixture_path" "$fixture_type"
}
run_preexisting_path_mode_contract "$PREFLIGHT_MODE_FILE" file 644 ||
    fail 'preflight rejected a root-owned mode-0644 Nginx configuration file'
run_preexisting_path_mode_contract "$PREFLIGHT_MODE_DIRECTORY" directory 755 ||
    fail 'preflight rejected a root-owned mode-0755 Nginx include directory'
for rejected_mode in 664 775 4755; do
    if run_preexisting_path_mode_contract "$PREFLIGHT_MODE_DIRECTORY" directory "$rejected_mode" \
        >/dev/null 2>&1; then
        fail "preflight accepted unsafe existing-path mode $rejected_mode"
    fi
done

# The accepted candidate deb-family platforms expose psql and pg_isready through the
# root-owned postgresql-client-common pg_wrapper. Validate the reusable package/entrypoint
# proof against disposable symlinks, including a non-native target rejection.
PACKAGED_WRAPPER_TARGET=$TEST_ROOT/pg-wrapper
PACKAGED_WRAPPER_OTHER=$TEST_ROOT/foreign-wrapper
PACKAGED_WRAPPER_ENTRY=$TEST_ROOT/psql
printf '%s\n' '#!/bin/sh' 'exit 0' > "$PACKAGED_WRAPPER_TARGET"
printf '%s\n' '#!/bin/sh' 'exit 0' > "$PACKAGED_WRAPPER_OTHER"
chmod 0755 "$PACKAGED_WRAPPER_TARGET" "$PACKAGED_WRAPPER_OTHER"
ln -s "$PACKAGED_WRAPPER_TARGET" "$PACKAGED_WRAPPER_ENTRY"
run_packaged_wrapper_contract() {
    expected_target=$1
    package_owner=$2
    PROBE_WRAPPER_ENTRY=$PACKAGED_WRAPPER_ENTRY \
        PROBE_WRAPPER_TARGET=$expected_target \
        PROBE_WRAPPER_OWNER=$package_owner \
        /bin/bash -c '
        source "$1"
        stat() {
            if [[ "$#" -eq 3 && "$1" == -c ]]; then
                case "$2" in
                    %u:%g) printf "%s\n" 0:0 ;;
                    %a) printf "%s\n" 755 ;;
                    *) return 96 ;;
                esac
                return
            fi
            command stat "$@"
        }
        dpkg-query() {
            case "$1" in
                --search)
                    printf "%s: %s\n" "$PROBE_WRAPPER_OWNER" "$2"
                    ;;
                --show)
                    printf "%s" "install ok installed"
                    ;;
                *)
                    return 95
                    ;;
            esac
        }
        assert_deb_family_packaged_wrapper \
            "$PROBE_WRAPPER_ENTRY" "$PROBE_WRAPPER_TARGET" postgresql-client-common
    ' probe-packaged-wrapper "$WITHOUT_ENTRYPOINT"
}
run_packaged_wrapper_contract "$PACKAGED_WRAPPER_TARGET" postgresql-client-common ||
    fail 'deb-family packaged-wrapper proof rejected a valid root-owned native wrapper fixture'
if run_packaged_wrapper_contract "$PACKAGED_WRAPPER_OTHER" postgresql-client-common \
    >/dev/null 2>&1; then
    fail 'deb-family packaged-wrapper proof accepted an entrypoint resolving to another wrapper'
fi
if run_packaged_wrapper_contract "$PACKAGED_WRAPPER_TARGET" foreign-package \
    >/dev/null 2>&1; then
    fail 'deb-family packaged-wrapper proof accepted a wrapper registered to another package'
fi

run_nginx_package_owner_contract() {
    platform_fixture=$1
    reported_owner=$2
    PROBE_NGINX_OWNER_PATH=$PACKAGED_WRAPPER_TARGET \
        PROBE_NGINX_REPORTED_OWNER=$reported_owner \
        /bin/bash -c '
        source "$1"
        select_supported_platform "$2"
        stat() {
            if [[ "$#" -eq 3 && "$1" == -c ]]; then
                case "$2" in
                    %u:%g) printf "%s\n" 0:0 ;;
                    %a) printf "%s\n" 755 ;;
                    *) return 96 ;;
                esac
                return
            fi
            command stat "$@"
        }
        dpkg-query() {
            case "$1" in
                --search) printf "%s: %s\n" "$PROBE_NGINX_REPORTED_OWNER" "$2" ;;
                --show) printf "%s" "install ok installed" ;;
                *) return 95 ;;
            esac
        }
        assert_deb_family_packaged_file \
            "$PROBE_NGINX_OWNER_PATH" "$PLATFORM_NGINX_BINARY_PACKAGE"
    ' probe-nginx-package-owner "$WITHOUT_ENTRYPOINT" "$platform_fixture"
}
run_nginx_package_owner_contract "$PLATFORM_FIXTURE_ROOT/ubuntu-22.04" nginx-core ||
    fail 'Ubuntu 22.04 rejected its reviewed nginx-core binary owner'
if run_nginx_package_owner_contract "$PLATFORM_FIXTURE_ROOT/ubuntu-22.04" nginx \
    >/dev/null 2>&1; then
    fail 'Ubuntu 22.04 accepted the wrong nginx binary package owner'
fi
run_nginx_package_owner_contract "$PLATFORM_FIXTURE_ROOT/ubuntu-24.04" nginx ||
    fail 'Ubuntu 24.04 rejected its reviewed nginx binary owner'
if run_nginx_package_owner_contract "$PLATFORM_FIXTURE_ROOT/ubuntu-24.04" nginx-core \
    >/dev/null 2>&1; then
    fail 'Ubuntu 24.04 accepted the Jammy-only nginx-core binary owner'
fi

NATIVE_UNIT_FIXTURE=$TEST_ROOT/probe-contract-native.service
: > "$NATIVE_UNIT_FIXTURE"
run_native_unit_contract() {
    drop_in_paths=$1
    PROBE_NATIVE_UNIT_FIXTURE=$NATIVE_UNIT_FIXTURE \
        PROBE_NATIVE_UNIT_DROP_INS=$drop_in_paths \
        /bin/bash -c '
        source "$1"
        systemctl() {
            [[ "$#" -eq 3 && "$1" == show && "$3" == probe-contract-native.service ]] || return 96
            case "$2" in
                --property=LoadState) printf "%s\n" 'LoadState=loaded' ;;
                --property=FragmentPath) printf "%s\n" "FragmentPath=$PROBE_NATIVE_UNIT_FIXTURE" ;;
                --property=DropInPaths) printf "%s\n" "DropInPaths=$PROBE_NATIVE_UNIT_DROP_INS" ;;
                *) return 95 ;;
            esac
        }
        assert_native_systemd_unit probe-contract-native.service "$PROBE_NATIVE_UNIT_FIXTURE"
    ' probe-native-unit "$WITHOUT_ENTRYPOINT"
}
run_native_unit_contract '' || fail 'native-unit proof rejected an exact fragment without drop-ins'
if run_native_unit_contract '/etc/systemd/system/probe-contract-native.service.d/override.conf' \
    >/dev/null 2>&1; then
    fail 'native-unit proof accepted a systemd drop-in override'
fi

NATIVE_WANTS_TARGET=$TEST_ROOT/native-wants-target.service
NATIVE_WANTS_OTHER=$TEST_ROOT/native-wants-other.service
NATIVE_WANTS_LINK=$TEST_ROOT/native-wants-link.service
: > "$NATIVE_WANTS_TARGET"
: > "$NATIVE_WANTS_OTHER"
ln -s "$NATIVE_WANTS_TARGET" "$NATIVE_WANTS_LINK"
PROBE_NATIVE_WANTS_LINK=$NATIVE_WANTS_LINK /bin/bash -c '
    source "$1"
    stat() {
        if [[ "$#" -eq 3 && "$1" == -c && "$2" == "%u:%g" && "$3" == "$PROBE_NATIVE_WANTS_LINK" ]]; then
            printf "%s\n" 0:0
            return
        fi
        command stat "$@"
    }
    assert_native_unit_wants_link "$2" "$3"
' probe-native-wants "$WITHOUT_ENTRYPOINT" "$NATIVE_WANTS_LINK" "$NATIVE_WANTS_TARGET" ||
    fail 'native-unit wants proof rejected an exact root-owned symlink fixture'
rm "$NATIVE_WANTS_LINK"
ln -s "$NATIVE_WANTS_OTHER" "$NATIVE_WANTS_LINK"
if PROBE_NATIVE_WANTS_LINK=$NATIVE_WANTS_LINK /bin/bash -c '
    source "$1"
    stat() { printf "%s\n" 0:0; }
    assert_native_unit_wants_link "$2" "$3"
' probe-wrong-native-wants "$WITHOUT_ENTRYPOINT" "$NATIVE_WANTS_LINK" "$NATIVE_WANTS_TARGET" \
    >/dev/null 2>&1; then
    fail 'native-unit wants proof accepted a symlink to the wrong unit'
fi
rm "$NATIVE_WANTS_LINK"
: > "$NATIVE_WANTS_LINK"
if /bin/bash -c '
    source "$1"
    assert_native_unit_wants_link "$2" "$3"
' probe-regular-native-wants "$WITHOUT_ENTRYPOINT" "$NATIVE_WANTS_LINK" "$NATIVE_WANTS_TARGET" \
    >/dev/null 2>&1; then
    fail 'native-unit wants proof accepted a regular file in place of the enablement symlink'
fi

# Existing security-sensitive roots are an input contract. The bootstrap may
# create a missing root, but it must never repair an existing root in place.
SECURE_EXISTING_DIR=$TEST_ROOT/existing-secure-directory
SECURE_INSTALL_MARKER=$TEST_ROOT/secure-directory-install-ran
mkdir "$SECURE_EXISTING_DIR"
if ! PROBE_SECURE_TEST_PATH=$SECURE_EXISTING_DIR \
    PROBE_SECURE_INSTALL_MARKER=$SECURE_INSTALL_MARKER \
    /bin/bash -c '
        source "$1"
        install() {
            : > "$PROBE_SECURE_INSTALL_MARKER"
            return 97
        }
        stat() {
            [[ "$#" -eq 3 && "$1" == -c && "$2" == "%u:%g:%a" && "$3" == "$PROBE_SECURE_TEST_PATH" ]] || return 96
            printf "%s\n" 0:0:755
        }
        ensure_secure_directory "$2" 0755
    ' probe-existing-secure-directory "$WITHOUT_ENTRYPOINT" "$SECURE_EXISTING_DIR"; then
    fail 'ensure_secure_directory rejected a valid existing root'
fi
[ ! -e "$SECURE_INSTALL_MARKER" ] ||
    fail 'ensure_secure_directory attempted to mutate a valid existing root'
if PROBE_SECURE_TEST_PATH=$SECURE_EXISTING_DIR \
    PROBE_SECURE_INSTALL_MARKER=$SECURE_INSTALL_MARKER \
    /bin/bash -c '
        source "$1"
        install() {
            : > "$PROBE_SECURE_INSTALL_MARKER"
            return 97
        }
        stat() {
            [[ "$#" -eq 3 && "$1" == -c && "$2" == "%u:%g:%a" && "$3" == "$PROBE_SECURE_TEST_PATH" ]] || return 96
            printf "%s\n" 0:0:700
        }
        ensure_secure_directory "$2" 0755
    ' probe-invalid-existing-secure-directory "$WITHOUT_ENTRYPOINT" "$SECURE_EXISTING_DIR" >/dev/null 2>&1; then
    fail 'ensure_secure_directory accepted an existing root with the wrong mode'
fi
[ ! -e "$SECURE_INSTALL_MARKER" ] ||
    fail 'ensure_secure_directory attempted to repair an invalid existing root'

BACKUP_EXISTING_DIR=$TEST_ROOT/existing-backup-directory
BACKUP_INSTALL_MARKER=$TEST_ROOT/backup-directory-install-ran
mkdir "$BACKUP_EXISTING_DIR"
if ! PROBE_BACKUP_TEST_PATH=$BACKUP_EXISTING_DIR \
    PROBE_BACKUP_INSTALL_MARKER=$BACKUP_INSTALL_MARKER \
    /bin/bash -c '
        source "$1"
        install() {
            : > "$PROBE_BACKUP_INSTALL_MARKER"
            return 97
        }
        id() {
            [[ "$#" -eq 2 && "$1" == -g && "$2" == probe-api ]] || return 96
            printf "%s\n" 4242
        }
        stat() {
            [[ "$#" -eq 3 && "$1" == -c && "$2" == "%u:%g:%a" && "$3" == "$PROBE_BACKUP_TEST_PATH" ]] || return 95
            printf "%s\n" 0:4242:710
        }
        ensure_backup_parent_directory "$2"
    ' probe-existing-backup-directory "$WITHOUT_ENTRYPOINT" "$BACKUP_EXISTING_DIR"; then
    fail 'ensure_backup_parent_directory rejected a valid existing backup root'
fi
[ ! -e "$BACKUP_INSTALL_MARKER" ] ||
    fail 'ensure_backup_parent_directory attempted to mutate a valid existing backup root'
if PROBE_BACKUP_TEST_PATH=$BACKUP_EXISTING_DIR \
    PROBE_BACKUP_INSTALL_MARKER=$BACKUP_INSTALL_MARKER \
    /bin/bash -c '
        source "$1"
        install() {
            : > "$PROBE_BACKUP_INSTALL_MARKER"
            return 97
        }
        id() {
            printf "%s\n" 4242
        }
        stat() {
            printf "%s\n" 0:4243:710
        }
        ensure_backup_parent_directory "$2"
    ' probe-invalid-existing-backup-directory "$WITHOUT_ENTRYPOINT" "$BACKUP_EXISTING_DIR" >/dev/null 2>&1; then
    fail 'ensure_backup_parent_directory accepted an existing backup root with the wrong numeric GID'
fi
[ ! -e "$BACKUP_INSTALL_MARKER" ] ||
    fail 'ensure_backup_parent_directory attempted to repair an invalid existing backup root'

# Existing active PostgreSQL is validation-only: prepare_runtime_services must
# never restart it. A PostgreSQL instance installed by this bootstrap is the
# sole path allowed to issue systemctl start.
POSTGRES_SERVICE_LOG=$TEST_ROOT/postgres-preexisting-systemctl.log
PROBE_SERVICE_LOG=$POSTGRES_SERVICE_LOG /bin/bash -c '
    source "$1"
    NGINX_PREEXISTED=1
    POSTGRESQL_PREEXISTED=1
    PLATFORM_POSTGRES_SERVICE=postgresql-14.service
    systemctl() {
        printf "%s\n" "$*" >> "$PROBE_SERVICE_LOG"
        return 0
    }
    nginx() {
        [[ "$#" -eq 1 && "$1" == -t ]]
    }
    assert_local_postgresql() {
        [[ "$#" -eq 1 && "$1" == management ]]
    }
    prepare_runtime_services management
' probe-preexisting-postgresql "$WITHOUT_ENTRYPOINT" ||
    fail 'prepare_runtime_services rejected the simulated active pre-existing PostgreSQL service'
if grep -Eq '^start postgresql(-14)?[.]service$' "$POSTGRES_SERVICE_LOG"; then
    fail 'prepare_runtime_services attempted to start pre-existing PostgreSQL'
fi

PROFILE_SIM_ROOT=$TEST_ROOT/release-profile
mkdir -p "$PROFILE_SIM_ROOT/legacy" "$PROFILE_SIM_ROOT/management" "$PROFILE_SIM_ROOT/full" "$PROFILE_SIM_ROOT/invalid"
printf '%s\n' 'format=probe-panel-release-v1' > "$PROFILE_SIM_ROOT/legacy/RELEASE-MANIFEST"
printf '%s\n' 'format=probe-panel-release-v1' 'profile=management' > "$PROFILE_SIM_ROOT/management/RELEASE-MANIFEST"
printf '%s\n' 'format=probe-panel-release-v1' 'profile=full' > "$PROFILE_SIM_ROOT/full/RELEASE-MANIFEST"
printf '%s\n' 'format=probe-panel-release-v1' 'profile=other' > "$PROFILE_SIM_ROOT/invalid/RELEASE-MANIFEST"
/bin/bash -c '
    source "$1"
    [[ "$(release_bundle_profile "$2/legacy")" == full ]]
    [[ "$(release_bundle_profile "$2/management")" == management ]]
    [[ "$(release_bundle_profile "$2/full")" == full ]]
    if (release_bundle_profile "$2/invalid") >/dev/null 2>&1; then
        exit 20
    fi
' probe-release-profile-detection "$DEPLOY_COMMON" "$PROFILE_SIM_ROOT" ||
    fail 'prebuilt release profile detection failed'

help_output=$(bash "$INSTALLER" --help)
for command_name in install status uninstall purge; do
    printf '%s\n' "$help_output" | grep -Fq -- "$command_name" || fail "help is missing $command_name"
done
printf '%s\n' "$help_output" | grep -Fq -- '--accept-eol' ||
    fail 'help is missing the explicit EOL acknowledgement option'
if bash "$INSTALLER" status --accept-eol >/dev/null 2>&1; then
    fail '--accept-eol was accepted for a non-install command'
fi
if bash "$INSTALLER" install --accept-eol --accept-eol >/dev/null 2>&1; then
    fail '--accept-eol was accepted more than once'
fi
printf '%s\n' "$help_output" | grep -Fq -- 'migrate-bootstrap' &&
    fail 'management-only v1.2 help still advertises migrate-bootstrap'
if grep -Fq 'migrate-bootstrap) migrate_bootstrap_action' "$INSTALLER"; then
    fail 'management-only v1.2 entrypoint still exposes historical bootstrap migration'
fi
if grep -Eq '(^|[^A-Za-z0-9_])(LEGACY_|MIGRATION_|WEB_REF|AGENT_REF|migrate_bootstrap_action|validate_legacy_|cleanup_migration|rollback_migration)' "$INSTALLER"; then
    fail 'management-only v1.2 installer still contains executable historical migration symbols'
fi
if bash "$INSTALLER" purge >/dev/null 2>&1; then
    fail 'purge must fail closed'
fi
if bash "$INSTALLER" install unexpected >/dev/null 2>&1; then
    fail 'installer accepted extra command-line values'
fi
management_release_help=$(bash "$DEPLOY_INSTALL_RELEASE" --help)
if printf '%s\n' "$management_release_help" | grep -Fq -- '--disable-default-site'; then
    fail 'management release installer still advertises default-site mutation'
fi
if bash "$DEPLOY_INSTALL_RELEASE" --disable-default-site >/dev/null 2>&1; then
    fail 'management release installer accepted the forbidden default-site mutation option'
fi

# These are literal source-code contracts and must not expand in this process.
# shellcheck disable=SC2016
for contract in \
    'PANEL_PROFILE="management"' \
    'REQUESTED_PROFILE="${PROBE_PANEL_RELEASE_PROFILE:-management}"' \
    'MANAGEMENT_VERSION="v1.2.0"' \
    'SUPER_MY_REF="refs/tags/${PANEL_VERSION}"' \
    'MANAGEMENT_RUNTIME_ABI="probe-linux-systemd-v2"' \
    'SUPPORTED_PLATFORM_IDS="debian-9-systemd,debian-10-systemd,debian-11-systemd,debian-12-systemd,debian-13-systemd,ubuntu-18.04-systemd,ubuntu-20.04-systemd,ubuntu-22.04-systemd,ubuntu-24.04-systemd,ubuntu-26.04-systemd,centos-linux-7-systemd,centos-linux-8-systemd,centos-stream-8-systemd,centos-stream-9-systemd,centos-stream-10-systemd"' \
    'configure_deb_platform debian-9-systemd 232 legacy nginx-full nginx-full classic postgresql-14 postgresql-client-14 1' \
    'configure_rpm_platform centos-linux-7-systemd yum 219 legacy classic nginx 1' \
    'configure_rpm_platform centos-stream-10-systemd dnf 257 modern modern nginx-core' \
    'validate_platform_lifecycle' \
    'UNSUPPORTED_SETUP_CODE_FILE="${SETUP_STATE_ROOT}/setup-code.json"' \
    "printf 'probe-panel-management-%s-linux-%s" \
    'release_asset_name "$PANEL_PROFILE" "$PANEL_VERSION" "$architecture"' \
    "https://github.com/Kcmose/super-my/releases/download/" \
    "--proto '=https'" \
    'sha256sum' \
    'BUNDLE-SHA256SUMS must cover every source, artifacts, and setup file exactly once' \
    'release metadata must declare runtime_abi=$MANAGEMENT_RUNTIME_ABI exactly once' \
    'platform_ids=$SUPPORTED_PLATFORM_IDS' \
    'source/probe-api/deploy/nginx/nginx-management-classic.conf' \
    'source/probe-api/deploy/nginx/nginx-management-ip-classic.conf' \
    'source/probe-api/deploy/nginx/nginx-management-legacy.conf' \
    'source/probe-api/deploy/nginx/nginx-management-ip-legacy.conf' \
    'source/probe-api/deploy/setup/probe-panel-setup-legacy.service' \
    'source/probe-api/deploy/setup/probe-panel-setup-legacy.socket' \
    'source/probe-api/deploy/setup/probe-panel-finalizer-management-legacy.service' \
    'source/probe-api/deploy/systemd/probe-api-legacy.service' \
    'source/probe-api/deploy/systemd/probe-postgres-backup-legacy.service' \
    'source/probe-api/deploy/systemd/probe-postgres-backup-legacy.timer' \
    'management release bundle must not contain visitor frontend artifacts' \
    'management release bundle must not contain Agent artifacts' \
    'probe-panel-finalizer-management.service' \
    'selected_setup_asset_name probe-panel-finalizer-management.service' \
    'this v1.2 installer is management-only' \
    '[[ -f "$root/$required" && ! -L "$root/$required" ]]' \
    'release migrations path is not a real directory' \
    'artifacts/api/probe-api' \
    'setup/probe-setup' \
    'artifacts/migrations' \
    'source/probe-api/config/probe-postgres-backup.env.example' \
    'management release bundle contains a forbidden source/build asset' \
    'management release bundle deploy assets differ from the reviewed runtime allowlist' \
    'management deploy-common contains forbidden full, Agent, or visitor build logic' \
    'artifacts/(agent|web)' \
    'PROBE_(AGENT|WEB)_DIR' \
    'old_(agent|web)' \
    '/srv/probe/(agent|web)' \
    'source/probe-api/deploy/scripts/backup-postgres.sh' \
    'source/probe-api/deploy/scripts/restore-postgres.sh' \
    'source/probe-api/deploy/systemd/probe-postgres-backup.service' \
    'source/probe-api/deploy/systemd/probe-postgres-backup.timer' \
    'deb_family_platform_runtime_packages()' \
    'centos_platform_runtime_packages()' \
    'runtime_packages+=("$PLATFORM_POSTGRES_SERVER_PACKAGE" "$PLATFORM_POSTGRES_CLIENT_PACKAGE")' \
    'runtime_packages+=("$PLATFORM_NGINX_INSTALL_PACKAGE")' \
    'platform_adapter_call install_packages "${runtime_packages[@]}"' \
    "CENTOS_MANAGED_REPO_DIR='/etc/yum.repos.d/probe-panel-runtime.repos'" \
    "CENTOS_REPO_ALLOWLIST='probe-centos-baseos,probe-centos-appstream,probe-centos-builder,probe-epel,probe-pgdg14'" \
    'local -a repository_options=(' \
    '--noplugins' \
    '"--setopt=reposdir=$CENTOS_MANAGED_REPO_DIR"' \
    "--disablerepo='*'" \
    '"--enablerepo=$CENTOS_REPO_ALLOWLIST"' \
    'dnf "${repository_options[@]}" --setopt=gpgcheck=True makecache' \
    'dnf "${repository_options[@]}" --setopt=gpgcheck=True module disable -y postgresql' \
    'dnf "${repository_options[@]}" install -y --setopt=install_weak_deps=False' \
    '--setopt=gpgcheck=True --setopt=keepcache=True "$@"' \
    'yum "${repository_options[@]}" --setopt=gpgcheck=1 makecache' \
    'yum "${repository_options[@]}" install -y --setopt=gpgcheck=1 --setopt=keepcache=1 "$@"' \
    'assert_platform_packaged_file "$pg_setup" "$PLATFORM_POSTGRES_SERVER_PACKAGE"' \
    'systemctl mask nginx.service' \
    'systemctl unmask nginx.service' \
    'systemctl stop nginx.service' \
    'systemctl disable nginx.service' \
    "disabled the stock deb-family Nginx default site" \
    'install_runtime_dependencies "$PANEL_PROFILE"' \
    'prepare_runtime_services "$PANEL_PROFILE"' \
    "preserving the existing Nginx service, enablement, and site configuration" \
    'assert_local_postgresql management' \
    '"$PLATFORM_PSQL" --no-psqlrc' \
    'systemctl start "$PLATFORM_POSTGRES_SERVICE"' \
    'server_version >= 140000' \
    'source/probe-api/deploy/setup/probe-panel-setup.socket' \
    'source/probe-api/deploy/nginx/nginx.conf' \
    'source/probe-api/deploy/nginx/nginx-ip.conf' \
    'source/probe-api/deploy/setup/probe-panel-finalizer.service' \
    'source/probe-api/deploy/setup/probe-panel-finalizer.path' \
    'setup finalizer must not make the entire Nginx configuration directory writable' \
    'finalizer_bind_rules=(tcp:80 tcp:443 tcp:18455)' \
    'FINALIZER_REQUEST_FILE="${FINALIZER_RUNTIME_ROOT}/finalize.json"' \
    'FINALIZER_RESULT_FILE="${FINALIZER_RUNTIME_ROOT}/result.json"' \
    'PROBE_SETUP_FINALIZE_REQUEST_FILE=%s' \
    'PROBE_SETUP_FINALIZE_RESULT_FILE=%s' \
    'PROBE_SETUP_BUNDLE_ROOT=%s' \
    'PROBE_SETUP_RELEASE_ID=%s' \
    'PROBE_SETUP_PROFILE=%s' \
    'PROBE_SETUP_PLATFORM_ID=%s' \
    'canonical_relative_path' \
    'PAX path override' \
    'OPEN_FILE_FLAGS' \
    'member.issym() or member.islnk()' \
    'devices, FIFOs, and other special files are forbidden' \
    'PROBE_SETUP_SOCKET_PATH=%s' \
    'PROBE_SETUP_SERVER_IP=%s' \
    '"$SETUP_BINARY" init' \
    'ssh -N -o ExitOnForwardFailure=yes -L 127.0.0.1:18080:/run/probe-panel-setup/setup.sock' \
    'a legacy bootstrap record is present; this management-only installer will not migrate or overwrite it' \
    'this management-only installer will not migrate or overwrite it' \
    'Probe Panel management setup does not display or require an installation code' \
    'main() {' \
    'BOOTSTRAP_ENTRYPOINT_SENTINEL=' \
    'if :; then main "$@" '\''probe-panel-bootstrap-complete-v1'\''; fi'; do
    assert_contains "$contract" "$INSTALLER"
done

if grep -Fq 'No installation code, database credential, or administrator credential exists or is displayed.' "$INSTALLER"; then
    fail 'status must not claim that a preserved legacy setup-code record does not exist'
fi

[ "$(grep -Fc 'source/probe-api/deploy/nginx/nginx-management.conf' "$INSTALLER")" -ge 1 ] ||
    fail 'management bundle validation must require its domain Nginx template'
[ "$(grep -Fc 'source/probe-api/deploy/nginx/nginx-management-ip.conf' "$INSTALLER")" -ge 1 ] ||
    fail 'management bundle validation must require its IP Nginx template'
[ "$(grep -Fc 'source/probe-api/deploy/nginx/nginx-management-legacy.conf' "$INSTALLER")" -ge 1 ] ||
    fail 'management bundle validation must require its legacy domain Nginx template'
[ "$(grep -Fc 'source/probe-api/deploy/nginx/nginx-management-ip-legacy.conf' "$INSTALLER")" -ge 1 ] ||
    fail 'management bundle validation must require its legacy IP Nginx template'
[ "$(grep -Fc 'source/probe-api/deploy/nginx/nginx-management-classic.conf' "$INSTALLER")" -ge 1 ] ||
    fail 'management bundle validation must require its classic domain Nginx template'
[ "$(grep -Fc 'source/probe-api/deploy/nginx/nginx-management-ip-classic.conf' "$INSTALLER")" -ge 1 ] ||
    fail 'management bundle validation must require its classic IP Nginx template'

# The fresh-host Nginx mask must be gone before FragmentPath is inspected, and
# the stock service must stay stopped/disabled throughout bootstrap setup.
nginx_dependency_unmask_line=$(grep -n '^[[:space:]]*systemctl unmask nginx[.]service >/dev/null$' "$INSTALLER" | head -n 1 | cut -d: -f1)
nginx_fragment_line=$(grep -n '^[[:space:]]*assert_platform_native_unit nginx[.]service nginx[.]service$' "$INSTALLER" | tail -n 1 | cut -d: -f1)
nginx_dependency_stop_line=$(grep -n '^[[:space:]]*systemctl stop nginx[.]service$' "$INSTALLER" | head -n 1 | cut -d: -f1)
nginx_dependency_disable_line=$(grep -n '^[[:space:]]*systemctl disable nginx[.]service >/dev/null$' "$INSTALLER" | head -n 1 | cut -d: -f1)
if [ -z "$nginx_dependency_unmask_line" ] || [ -z "$nginx_fragment_line" ] ||
    [ -z "$nginx_dependency_stop_line" ] || [ -z "$nginx_dependency_disable_line" ] ||
    [ "$nginx_dependency_unmask_line" -ge "$nginx_fragment_line" ] ||
    [ "$nginx_dependency_stop_line" -ge "$nginx_fragment_line" ] ||
    [ "$nginx_dependency_disable_line" -ge "$nginx_fragment_line" ]; then
    fail 'fresh-host Nginx must be unmasked, stopped, and disabled before native FragmentPath validation'
fi
assert_contains 'NGINX_ABSENT_AT_START=1' "$INSTALLER"
assert_contains 'newly installed Nginx must remain stopped during management setup' "$INSTALLER"
assert_contains 'newly installed Nginx must remain disabled during management setup' "$INSTALLER"

# Failed bootstrap cleanup restores the PostgreSQL activity state observed
# before apt or any other installer mutation. A completed install keeps it up.
INSTALL_ACTION_SOURCE=$TEST_ROOT/install-action.source
awk '
    /^install_action\(\) \{/ { capture=1 }
    capture { print }
    capture && /^}$/ { exit }
' "$INSTALLER" > "$INSTALL_ACTION_SOURCE"
postgres_capture_line=$(grep -n '^[[:space:]]*capture_postgresql_start_state$' "$INSTALL_ACTION_SOURCE" | head -n 1 | cut -d: -f1)
runtime_dependency_line=$(grep -n '^[[:space:]]*install_runtime_dependencies "[$]PANEL_PROFILE"$' "$INSTALL_ACTION_SOURCE" | head -n 1 | cut -d: -f1)
if [ -z "$postgres_capture_line" ] || [ -z "$runtime_dependency_line" ] ||
    [ "$postgres_capture_line" -ge "$runtime_dependency_line" ]; then
    fail 'PostgreSQL activity state must be captured before runtime dependency installation'
fi
# Literal installer source; the variables must remain unexpanded.
# shellcheck disable=SC2016
assert_contains 'if [[ "$POSTGRESQL_STATE_CAPTURED" -eq 1 && "$POSTGRESQL_WAS_ACTIVE" -eq 0 ]]; then' "$INSTALLER"
assert_contains 'systemctl stop "$PLATFORM_POSTGRES_SERVICE" >/dev/null 2>&1 || :' "$INSTALLER"
assert_contains 'POSTGRESQL_PREEXISTED=1' "$INSTALLER"
assert_contains 'an existing inactive PostgreSQL service will not be started blindly' "$INSTALLER"
assert_contains 'the existing PostgreSQL service became inactive' "$INSTALLER"
[ "$(grep -Fc 'systemctl start "$PLATFORM_POSTGRES_SERVICE"' "$INSTALLER")" -eq 1 ] ||
    fail 'only the newly installed PostgreSQL branch may issue systemctl start'
if grep -Eq 'systemctl (start|stop|restart|is-active.*) postgresql(-14)?[.]service' "$INSTALLER"; then
    fail 'bootstrap service lifecycle contains a hard-coded PostgreSQL unit name'
fi

# The install action has two read-only host snapshots around immutable release
# verification. The explicit mutation boundary follows both snapshots and the
# complete bundle validator, then gates every package/account/service/path step.
first_runtime_preflight_line=$(grep -n '^[[:space:]]*preflight_existing_runtimes$' "$INSTALL_ACTION_SOURCE" | head -n 1 | cut -d: -f1)
second_runtime_preflight_line=$(grep -n '^[[:space:]]*preflight_existing_runtimes$' "$INSTALL_ACTION_SOURCE" | tail -n 1 | cut -d: -f1)
first_platform_select_line=$(grep -n '^[[:space:]]*select_supported_platform$' "$INSTALL_ACTION_SOURCE" | head -n 1 | cut -d: -f1)
second_platform_select_line=$(grep -n '^[[:space:]]*select_supported_platform$' "$INSTALL_ACTION_SOURCE" | tail -n 1 | cut -d: -f1)
first_fresh_target_line=$(grep -n '^[[:space:]]*assert_fresh_target$' "$INSTALL_ACTION_SOURCE" | head -n 1 | cut -d: -f1)
second_fresh_target_line=$(grep -n '^[[:space:]]*assert_fresh_target$' "$INSTALL_ACTION_SOURCE" | tail -n 1 | cut -d: -f1)
first_systemd_preflight_line=$(grep -n '^[[:space:]]*preflight_systemd_host$' "$INSTALL_ACTION_SOURCE" | head -n 1 | cut -d: -f1)
second_systemd_preflight_line=$(grep -n '^[[:space:]]*preflight_systemd_host$' "$INSTALL_ACTION_SOURCE" | tail -n 1 | cut -d: -f1)
manifest_download_line=$(grep -n '^[[:space:]]*download_file "[$]manifest"' "$INSTALL_ACTION_SOURCE" | cut -d: -f1)
archive_download_line=$(grep -n '^[[:space:]]*download_file "[$]archive"' "$INSTALL_ACTION_SOURCE" | cut -d: -f1)
release_validation_line=$(grep -n '^[[:space:]]*validate_release_bundle ' "$INSTALL_ACTION_SOURCE" | cut -d: -f1)
mutation_boundary_line=$(grep -n '^[[:space:]]*HOST_MUTATION_STARTED=1$' "$INSTALL_ACTION_SOURCE" | cut -d: -f1)
account_mutation_line=$(grep -n '^[[:space:]]*prepare_probe_api_account$' "$INSTALL_ACTION_SOURCE" | cut -d: -f1)
service_mutation_line=$(grep -n '^[[:space:]]*prepare_runtime_services "[$]PANEL_PROFILE"$' "$INSTALL_ACTION_SOURCE" | cut -d: -f1)
permanent_directory_line=$(grep -n '^[[:space:]]*ensure_secure_directory /srv/probe 0755$' "$INSTALL_ACTION_SOURCE" | cut -d: -f1)
if [ -z "$first_runtime_preflight_line" ] || [ -z "$second_runtime_preflight_line" ] ||
    [ -z "$first_platform_select_line" ] || [ -z "$second_platform_select_line" ] ||
    [ -z "$first_fresh_target_line" ] || [ -z "$second_fresh_target_line" ] ||
    [ -z "$first_systemd_preflight_line" ] || [ -z "$second_systemd_preflight_line" ] ||
    [ -z "$manifest_download_line" ] || [ -z "$archive_download_line" ] ||
    [ -z "$release_validation_line" ] || [ -z "$mutation_boundary_line" ] ||
    [ -z "$runtime_dependency_line" ] || [ -z "$account_mutation_line" ] ||
    [ -z "$service_mutation_line" ] || [ -z "$permanent_directory_line" ] ||
    [ "$first_platform_select_line" -ge "$first_fresh_target_line" ] ||
    [ "$first_fresh_target_line" -ge "$first_systemd_preflight_line" ] ||
    [ "$first_systemd_preflight_line" -ge "$first_runtime_preflight_line" ] ||
    [ "$first_runtime_preflight_line" -ge "$manifest_download_line" ] ||
    [ "$manifest_download_line" -ge "$archive_download_line" ] ||
    [ "$archive_download_line" -ge "$release_validation_line" ] ||
    [ "$release_validation_line" -ge "$second_platform_select_line" ] ||
    [ "$second_platform_select_line" -ge "$second_fresh_target_line" ] ||
    [ "$second_fresh_target_line" -ge "$second_systemd_preflight_line" ] ||
    [ "$second_systemd_preflight_line" -ge "$second_runtime_preflight_line" ] ||
    [ "$second_runtime_preflight_line" -ge "$mutation_boundary_line" ] ||
    [ "$mutation_boundary_line" -ge "$runtime_dependency_line" ] ||
    [ "$runtime_dependency_line" -ge "$account_mutation_line" ] ||
    [ "$account_mutation_line" -ge "$service_mutation_line" ] ||
    [ "$service_mutation_line" -ge "$permanent_directory_line" ]; then
    fail 'host mutation must remain behind compatible-host and verified-release gates'
fi
[ "$(grep -c '^[[:space:]]*preflight_existing_runtimes$' "$INSTALL_ACTION_SOURCE")" -eq 2 ] ||
    fail 'install must validate existing runtimes before and after release verification'
[ "$(grep -c '^[[:space:]]*select_supported_platform$' "$INSTALL_ACTION_SOURCE")" -eq 2 ] ||
    fail 'install must select the exact platform before and after release verification'
[ "$(grep -c '^[[:space:]]*assert_fresh_target$' "$INSTALL_ACTION_SOURCE")" -eq 2 ] ||
    fail 'install must validate the fresh target before and after release verification'
[ "$(grep -c '^[[:space:]]*preflight_systemd_host$' "$INSTALL_ACTION_SOURCE")" -eq 2 ] ||
    fail 'install must validate systemd prerequisites before and after release verification'
assert_contains 'release-verification prerequisites are missing' "$INSTALLER"
assert_contains 'select_supported_platform()' "$INSTALLER"
assert_contains 'os-release must declare exactly one ID and one VERSION_ID' "$INSTALLER"
assert_contains 'accepted candidate platform IDs' "$INSTALLER"
assert_contains 'requires systemd $PLATFORM_SYSTEMD_MIN_VERSION or newer' "$INSTALLER"
assert_contains 'PID 1 must be systemd' "$INSTALLER"
assert_contains 'the active deb-family Nginx configuration must include /etc/nginx/conf.d/*.conf' "$INSTALLER"
assert_contains 'validate_existing_nginx_configuration' "$INSTALLER"
# Literal installer source contracts; referenced variables must not expand here.
# shellcheck disable=SC2016
assert_contains '"$nginx_binary" -t -c "$nginx_config"' "$INSTALLER"
# shellcheck disable=SC2016
assert_contains '"$nginx_binary" -T -c "$nginx_config"' "$INSTALLER"
assert_contains 'the existing Nginx configuration dump exceeds the 1 MiB preflight limit' "$INSTALLER"
# shellcheck disable=SC2016
assert_contains 'output="$(systemctl show --property="$property" "$unit" 2>/dev/null)"' "$INSTALLER"
assert_contains 'has systemd drop-ins and cannot be treated as an unmodified native unit' "$INSTALLER"
assert_contains 'assert_native_unit_wants_link' "$INSTALLER"
# shellcheck disable=SC2016
assert_contains '/etc/systemd/system/multi-user.target.wants/$service' "$INSTALLER"
# shellcheck disable=SC2016
assert_contains '/run/systemd/system/multi-user.target.wants/$service' "$INSTALLER"
assert_contains 'PostgreSQL client commands must enter through /usr/bin/psql and /usr/bin/pg_isready' "$INSTALLER"
assert_contains '/usr/bin/psql /usr/share/postgresql-common/pg_wrapper postgresql-client-common' "$INSTALLER"
assert_contains '/usr/bin/pg_isready /usr/share/postgresql-common/pg_wrapper postgresql-client-common' "$INSTALLER"
# shellcheck disable=SC2016
assert_contains 'dpkg-query --search "$file_path"' "$INSTALLER"
# shellcheck disable=SC2016
assert_contains 'assert_platform_packaged_file /usr/sbin/nginx "$PLATFORM_NGINX_BINARY_PACKAGE"' "$INSTALLER"
assert_contains 'PostgreSQL client commands must use the reviewed PGDG 14 paths under /usr/pgsql-14/bin' "$INSTALLER"
assert_contains 'assert_rpm_packaged_file()' "$INSTALLER"
assert_contains 'candidate platform prerequisites exclude OpenResty and 1Panel-managed web stacks' "$INSTALLER"
assert_contains 'assert_local_postgresql management' "$INSTALLER"
assert_contains 'HOST_MUTATION_STARTED=0' "$INSTALLER"
assert_contains 'HOST_MUTATION_STARTED=1' "$INSTALLER"

for compatibility_script in "$INSTALLER" "$DEPLOY_COMMON" "$DEPLOY_MANAGEMENT_RUNTIME"; do
    if grep -Fq -- '--value' "$compatibility_script"; then
        fail "systemd compatibility script still requires systemctl --value: $compatibility_script"
    fi
    if grep -Fq -- '--reset-env' "$compatibility_script"; then
        fail "legacy util-linux compatibility script still requires setpriv --reset-env: $compatibility_script"
    fi
    if grep -Eq 'systemctl[[:space:]]+(enable|disable)[[:space:]]+--now' "$compatibility_script"; then
        fail "legacy systemd compatibility script still combines enablement with --now: $compatibility_script"
    fi
done

# Install, upgrade, and uninstall share one non-blocking exclusive lock. Each
# action must acquire it exactly once, and the install path must own it before
# even the fresh-target check, package management, or downloads can run.
assert_contains 'BOOTSTRAP_LOCK_FILE="/run/lock/probe-panel-bootstrap.lock"' "$INSTALLER"
# Literal installer source; the descriptor variable must remain unexpanded.
# shellcheck disable=SC2016
assert_contains 'flock --exclusive --nonblock "$BOOTSTRAP_LOCK_FD"' "$INSTALLER"
# Literal installer source; the mode variable must remain unexpanded.
# shellcheck disable=SC2016
assert_contains 'if [[ "$lock_root_mode" != 1777 ]]; then' "$INSTALLER"
assert_contains 'set -o noclobber' "$INSTALLER"
# Literal installer source; the lock path variable must remain unexpanded.
# shellcheck disable=SC2016
if grep -Fq 'install -o root -g root -m 0600 /dev/null "$BOOTSTRAP_LOCK_FILE"' "$INSTALLER"; then
    fail 'sticky-parent lock creation must use an exclusive create, not a check-then-install sequence'
fi
UPGRADE_ACTION_SOURCE=$TEST_ROOT/upgrade-action.source
UNINSTALL_ACTION_SOURCE=$TEST_ROOT/uninstall-action.source
sed -n '/^upgrade_action() {$/,/^}$/p' "$INSTALLER" > "$UPGRADE_ACTION_SOURCE"
sed -n '/^uninstall_action() {$/,/^}$/p' "$INSTALLER" > "$UNINSTALL_ACTION_SOURCE"
for locked_action in \
    "install:$INSTALL_ACTION_SOURCE" \
    "upgrade:$UPGRADE_ACTION_SOURCE" \
    "uninstall:$UNINSTALL_ACTION_SOURCE"; do
    action_name=${locked_action%%:*}
    action_source=${locked_action#*:}
    [ -s "$action_source" ] ||
        fail "$action_name action is unavailable for bootstrap-lock verification"
    [ "$(grep -c '^[[:space:]]*acquire_bootstrap_lock$' "$action_source")" -eq 1 ] ||
        fail "$action_name must acquire the bootstrap lock exactly once"
done
[ "$(grep -c '^[[:space:]]*acquire_bootstrap_lock$' "$INSTALLER")" -eq 3 ] ||
    fail 'only install, upgrade, and uninstall may acquire the bootstrap lock'
bootstrap_lock_line=$(grep -n '^[[:space:]]*acquire_bootstrap_lock$' "$INSTALL_ACTION_SOURCE" | head -n 1 | cut -d: -f1)
fresh_target_line=$(grep -n '^[[:space:]]*assert_fresh_target$' "$INSTALL_ACTION_SOURCE" | head -n 1 | cut -d: -f1)
if [ -z "$bootstrap_lock_line" ] || [ -z "$fresh_target_line" ] ||
    [ "$bootstrap_lock_line" -ge "$fresh_target_line" ] ||
    [ "$bootstrap_lock_line" -ge "$manifest_download_line" ]; then
    fail 'install must acquire the bootstrap lock before fresh-target validation and release download'
fi
upgrade_lock_line=$(grep -n '^[[:space:]]*acquire_bootstrap_lock$' "$UPGRADE_ACTION_SOURCE" | head -n 1 | cut -d: -f1)
upgrade_validation_line=$(grep -n '^[[:space:]]*"[$]MANAGEMENT_VALIDATE_BINARY" host$' "$UPGRADE_ACTION_SOURCE" | head -n 1 | cut -d: -f1)
if [ -z "$upgrade_lock_line" ] || [ -z "$upgrade_validation_line" ] ||
    [ "$upgrade_lock_line" -ge "$upgrade_validation_line" ]; then
    fail 'upgrade must acquire the bootstrap lock before validating or replacing the installed management release'
fi
uninstall_lock_line=$(grep -n '^[[:space:]]*acquire_bootstrap_lock$' "$UNINSTALL_ACTION_SOURCE" | head -n 1 | cut -d: -f1)
uninstall_product_line=$(grep -n '^[[:space:]]*if \[\[ -e /srv/probe/api/probe-api' "$UNINSTALL_ACTION_SOURCE" | head -n 1 | cut -d: -f1)
if [ -z "$uninstall_lock_line" ] || [ -z "$uninstall_product_line" ] ||
    [ "$uninstall_lock_line" -ge "$uninstall_product_line" ]; then
    fail 'uninstall must acquire the bootstrap lock before inspecting or mutating the installed management product'
fi

# Only newly created exact-mode roots may pass through install(1). Existing
# roots, including the broad shared parents below, are validation-only.
# Literal installer source; the path variable must remain unexpanded.
# shellcheck disable=SC2016
assert_contains 'if [[ ! -e "$path" ]]; then' "$INSTALLER"
# Literal installer source; mode and path variables must remain unexpanded.
# shellcheck disable=SC2016
assert_contains 'install -d -o root -g root -m "$mode" "$path"' "$INSTALLER"
# Literal installer source; the path variable must remain unexpanded.
# shellcheck disable=SC2016
assert_contains 'stat -c '\''%u:%g:%a'\'' "$path"' "$INSTALLER"
assert_contains 'refusing to change an existing directory' "$INSTALLER"
assert_contains 'ensure_secure_directory /srv/probe 0755' "$INSTALLER"
assert_contains 'ensure_secure_directory /usr/local/lib 0755' "$INSTALLER"
assert_contains 'ensure_backup_parent_directory /var/backups/probe-panel' "$INSTALLER"
# Literal installer source; GID and path variables must remain unexpanded.
# shellcheck disable=SC2016
assert_contains 'install -d -o root -g "$probe_api_gid" -m 0710 "$path"' "$INSTALLER"
assert_contains 'refusing to change an existing backup directory' "$INSTALLER"

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
        'probe-api UID is outside the platform system-account range' \
        'probe-api must use /nonexistent as its home directory' \
        'probe-api must use the platform nologin shell as its shell' \
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
[ "$(grep -Fc 'assert_probe_api_service_account' "$INSTALLER")" -ge 2 ] ||
    fail 'fresh management bootstrap must enforce the strict probe-api account contract'
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
bootstrap_runtime_line=$(grep -n '^[[:space:]]*prepare_runtime_services "[$]PANEL_PROFILE"[[:space:]]*$' "$INSTALLER" | tail -n 1 | cut -d: -f1)
if [ -z "$bootstrap_account_line" ] ||
    [ -z "$bootstrap_runtime_line" ] ||
    [ "$bootstrap_account_line" -ge "$bootstrap_runtime_line" ]; then
    fail 'strict probe-api account validation must precede Nginx/PostgreSQL service mutations'
fi
assert_contains "printf 'PROBE_SETUP_FINALIZE_REQUEST_FILE=%s\\n' \"\$FINALIZER_REQUEST_FILE\"" "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_FINALIZE_RESULT_FILE=%s\\n' \"\$FINALIZER_RESULT_FILE\"" "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_BUNDLE_ROOT=%s\\n' \"\$INSTALLED_RELEASE\"" "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_RELEASE_ID=%s\\n' \"\$PANEL_VERSION\"" "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_PROFILE=%s\\n' \"\$PANEL_PROFILE\"" "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_PLATFORM_ID=%s\\n' \"\$PLATFORM_ID\"" "$INSTALLER"
# shellcheck disable=SC2016
assert_contains 'PROBE_SETUP_PROFILE="$PANEL_PROFILE"' "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_SOCKET_PATH=%s\\n' \"\$SETUP_SOCKET_PATH\"" "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_SERVER_IP=%s\\n' \"\$SETUP_SERVER_IP\"" "$INSTALLER"

# Management release activation must use the shared hardened lock helper. It
# must never chmod the platform's shared lock directory or use a truncating open.
assert_contains 'require_commands flock stat' "$DEPLOY_INSTALL_RELEASE"
assert_contains 'acquire_deployment_lock' "$DEPLOY_INSTALL_RELEASE"
assert_contains 'acquire_root_lock()' "$DEPLOY_COMMON"
assert_contains 'acquire_deployment_lock()' "$DEPLOY_COMMON"
# This is a literal source-code contract; $lock_root_mode must not expand here.
# shellcheck disable=SC2016
assert_contains 'if [[ "$lock_root_mode" != 1777 ]]; then' "$DEPLOY_COMMON"
assert_contains 'set -o noclobber' "$DEPLOY_COMMON"
assert_contains 'stat -Lc '\''%d:%i'\'' /proc/self/fd/9' "$DEPLOY_COMMON"
assert_contains 'flock --exclusive --nonblock 9' "$DEPLOY_COMMON"
if grep -Eq 'install -d .* /run/lock|exec 9>"[$]PROBE_DEPLOY_LOCK"|flock -n 9' "$DEPLOY_INSTALL_RELEASE"; then
    fail 'management release installer still mutates the shared lock root or uses the unsafe legacy lock open'
fi
assert_contains 'management release runtime must not mutate the deb-family default Nginx site' "$INSTALLER"
assert_contains 'management administrator artifact contains a historical multi-product setup control' "$INSTALLER"

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
    'ExecStopPost=/usr/local/lib/probe-panel/probe-setup finalize-cleanup' \
    'TimeoutStartSec=30min' \
    'CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE CAP_FOWNER CAP_NET_BIND_SERVICE CAP_SETGID CAP_SETUID' \
    'AmbientCapabilities=CAP_SETGID CAP_SETUID' \
    'NoNewPrivileges=true' \
    'ProtectSystem=strict' \
    'ReadOnlyPaths=/srv/probe/setup-ui' \
    'ReadWritePaths=/etc/probe-panel' \
    'ReadWritePaths=/var/lib/probe-panel/setup' \
    'ReadWritePaths=/etc/nginx/conf.d' \
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
    'SocketBindAllow=tcp:18455' \
    'SocketBindDeny=any'; do
    assert_contains "$contract" "$FINALIZER_UNIT"
done

if grep -Fxq 'ReadWritePaths=/etc/nginx' "$FINALIZER_UNIT"; then
    fail 'finalizer must not make the entire Nginx configuration directory writable'
fi
if grep -Fxq 'ReadWritePaths=/etc/nginx/sites-enabled' "$FINALIZER_UNIT"; then
    fail 'management finalizer must not write the Nginx sites-enabled directory'
fi

[ "$(grep -Fc 'SocketBindAllow=' "$FINALIZER_UNIT")" -eq 3 ] ||
    fail 'management finalizer must contain exactly three reviewed ingress bind rules'
if grep -Eq '^SocketBindAllow=tcp:(18453|18454)$' "$FINALIZER_UNIT"; then
    fail 'management finalizer must not bind historical visitor or Agent artifact ports'
fi

[ "$(grep -Fc 'ExecStopPost=' "$FINALIZER_UNIT")" -eq 1 ] ||
    fail 'finalizer must contain exactly one retry-aware ExecStopPost action'
if grep -Eq '^ExecStopPost=/usr/bin/(sleep|systemctl)' "$FINALIZER_UNIT"; then
    fail 'preflight failure must not unconditionally stop the retryable setup channel'
fi
assert_contains 'finalize-cleanup' "$SETUP_MAIN"
assert_contains 'if !terminalSetupState(record.Status) {' "$SETUP_MAIN"
assert_contains 'return state == setup.StateInstalled || state == setup.StateRecoveryRequired' "$SETUP_MAIN"
assert_contains '"stop", "probe-panel-setup.socket"' "$SETUP_MAIN"
assert_contains '"--no-block", "stop", "probe-panel-setup.service"' "$SETUP_MAIN"

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
/bin/bash -c '
    source "$1"
    initialize_runtime_platform_contract centos-linux-7-systemd
    [[ "$(selected_systemd_asset_name probe-api.service)" == probe-api-legacy.service ]]
    [[ "$(selected_systemd_asset_name probe-postgres-backup.service)" == probe-postgres-backup-legacy.service ]]
    [[ "$(selected_systemd_asset_name probe-postgres-backup.timer)" == probe-postgres-backup-legacy.timer ]]
    RUNTIME_PLATFORM_ID=
    RUNTIME_SYSTEMD_PROFILE=
    RUNTIME_POSTGRES_SERVICE=
    RUNTIME_CERTBOT_TIMER=
    initialize_runtime_platform_contract debian-13-systemd
    [[ "$(selected_systemd_asset_name probe-api.service)" == probe-api.service ]]
' probe-runtime-unit-profile "$DEPLOY_COMMON" ||
    fail 'deployment runtime did not select legacy/modern systemd assets exactly'
/bin/bash -c '
    source "$1"
    initialize_runtime_platform_contract centos-stream-9-systemd
    [[ "$(runtime_postgres_command pg_dump)" == /usr/pgsql-14/bin/pg_dump ]]
    [[ "$(runtime_postgres_command pg_restore)" == /usr/pgsql-14/bin/pg_restore ]]
    [[ "$(runtime_postgres_command psql)" == /usr/pgsql-14/bin/psql ]]
    RUNTIME_PLATFORM_ID=
    RUNTIME_SYSTEMD_PROFILE=
    RUNTIME_POSTGRES_SERVICE=
    RUNTIME_CERTBOT_TIMER=
    initialize_runtime_platform_contract debian-10-systemd
    [[ "$(runtime_postgres_command pg_dump)" == /usr/bin/pg_dump ]]
    [[ "$(runtime_postgres_command psql)" == /usr/bin/psql ]]
' probe-runtime-postgres-command "$DEPLOY_COMMON" ||
    fail 'deployment runtime did not select Debian/RPM PostgreSQL command paths exactly'
/bin/bash -c '
    source "$1"
    for accessor in runtime_platform_id runtime_systemd_profile runtime_postgres_service runtime_certbot_timer runtime_account_family; do
        if ( "$accessor" >/dev/null 2>&1 ); then
            exit 41
        fi
    done
    if ( runtime_postgres_command pg_dump >/dev/null 2>&1 ); then
        exit 42
    fi
    RUNTIME_PLATFORM_ID=debian-12-systemd
    if ( runtime_platform_id >/dev/null 2>&1 ); then
        exit 43
    fi
    RUNTIME_PLATFORM_ID=
    initialize_runtime_platform_contract debian-12-systemd
    RUNTIME_SYSTEMD_PROFILE=unknown
    if ( runtime_systemd_profile >/dev/null 2>&1 ); then
        exit 44
    fi
    RUNTIME_SYSTEMD_PROFILE=modern
    RUNTIME_POSTGRES_SERVICE=unknown.service
    if ( runtime_postgres_service >/dev/null 2>&1 ); then
        exit 45
    fi
    RUNTIME_POSTGRES_SERVICE=postgresql.service
    RUNTIME_CERTBOT_TIMER=unknown.timer
    if ( runtime_certbot_timer >/dev/null 2>&1 ); then
        exit 46
    fi
    RUNTIME_CERTBOT_TIMER=certbot.timer
    RUNTIME_PLATFORM_ID=unknown-systemd
    if ( runtime_account_family >/dev/null 2>&1 ); then
        exit 47
    fi
' probe-runtime-fail-closed "$DEPLOY_COMMON" ||
    fail 'deployment runtime accessors did not fail closed on uninitialized or inconsistent platform state'
if grep -Fq '${RUNTIME_POSTGRES_SERVICE:-postgresql.service}' "$DEPLOY_COMMON" ||
   grep -Fq '${RUNTIME_CERTBOT_TIMER:-certbot.timer}' "$DEPLOY_COMMON" ||
   grep -Fq '${RUNTIME_SYSTEMD_PROFILE:-modern}' "$DEPLOY_COMMON" ||
   grep -Fq 'local account_family=deb' "$DEPLOY_COMMON"; then
    fail 'deployment runtime still contains a silent Debian/modern fallback'
fi

CERTBOT_TIMER_LOG=$TEST_ROOT/certbot-timer.log
PROBE_CERTBOT_TIMER_LOG=$CERTBOT_TIMER_LOG /bin/bash -c '
    source "$1"
    PROBE_INGRESS_MODE=domain
    initialize_runtime_platform_contract centos-stream-9-systemd
    systemctl() {
        printf "%s\n" "$*" >> "$PROBE_CERTBOT_TIMER_LOG"
        case "$1" in
            is-enabled) printf "%s\n" enabled ;;
            is-active) printf "%s\n" active ;;
            *) return 96 ;;
        esac
    }
    validate_certbot_timer_state management
' probe-runtime-certbot-timer "$DEPLOY_COMMON" ||
    fail 'deployment runtime rejected the RPM certbot-renew.timer contract'
grep -Fxq 'is-enabled certbot-renew.timer' "$CERTBOT_TIMER_LOG" ||
    fail 'deployment runtime did not query the selected certbot timer enablement'
grep -Fxq 'is-active certbot-renew.timer' "$CERTBOT_TIMER_LOG" ||
    fail 'deployment runtime did not query the selected certbot timer activity'
# These are literal source-code contracts and must not expand in this process.
# shellcheck disable=SC2016
for contract in \
    'readonly PROBE_PRIVATE_CA_FILE="/etc/probe-panel/tls/private-ca/ca.pem"' \
    'PROBE_INGRESS_MODE must be exactly domain or ip' \
    'PROBE_AGENT_INSTALL_CA_FILE must be set explicitly' \
    'selected_nginx_template()' \
    'management_platform_nginx_dialect()' \
    'management_platform_systemd_profile()' \
    'management_platform_postgres_service()' \
    'management_platform_certbot_timer()' \
    'management_platform_postgres_bin_dir()' \
    'assert_runtime_platform_contract()' \
    'initialize_runtime_platform_contract()' \
    'runtime_platform_id()' \
    'runtime_systemd_profile()' \
    'runtime_postgres_service()' \
    'runtime_certbot_timer()' \
    'runtime_account_family()' \
    'runtime_postgres_command()' \
    'selected_systemd_asset_name()' \
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
    'release_bundle_profile()' \
    'validate_release_profile()' \
    'management release must not contain Agent artifacts' \
    'management release must not contain visitor frontend artifacts' \
    'stage_release "$artifact_root" "$unique_release_id" "$release_profile"' \
    'activate_release "$release_dir" "$release_profile"' \
    'nginx-management.conf' \
    'nginx-management-ip.conf' \
    'nginx-management-classic.conf' \
    'nginx-management-ip-classic.conf' \
    'nginx-management-legacy.conf' \
    'nginx-management-ip-legacy.conf' \
    'config validate-ingress-tls admin-domain' \
    'config validate-ingress-tls admin-ip' \
    'timer_unit="$(runtime_certbot_timer)"' \
    'systemctl is-active --quiet "$(runtime_postgres_service)"' \
    'systemctl start "$(runtime_postgres_service)"' \
    '"$(runtime_postgres_command pg_dump)" --no-password' \
    '"$(runtime_postgres_command pg_restore)" --list' \
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
assert_contains '--profile PROFILE' "$DEPLOY_INSTALL_RELEASE"
assert_contains 'v1.2 accepts management only' "$DEPLOY_INSTALL_RELEASE"
assert_contains 'this v1.2 release installer accepts management only' "$DEPLOY_INSTALL_RELEASE"
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
