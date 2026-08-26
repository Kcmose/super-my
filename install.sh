#!/usr/bin/env bash

# Canonical common source for the Probe Panel server bootstrap. The public
# root install.sh is generated from this file and the reviewed family adapters.
# This script installs verified prebuilt application assets plus the
# runtime packages required by the local setup flow. It never receives database
# or administrator credentials.

set -Eeuo pipefail
umask 077

PROGRAM="${0##*/}"
MANAGEMENT_VERSION="v1.2.0"
PANEL_PROFILE="management"
REQUESTED_PROFILE="${PROBE_PANEL_RELEASE_PROFILE:-management}"
PANEL_VERSION="${PROBE_PANEL_RELEASE_VERSION:-$MANAGEMENT_VERSION}"
RELEASE_BASE_URL="${PROBE_PANEL_RELEASE_BASE_URL:-https://github.com/Kcmose/super-my/releases/download/${PANEL_VERSION}}"
SUPER_MY_REF="refs/tags/${PANEL_VERSION}"
MANAGEMENT_RUNTIME_ABI="probe-linux-systemd-v2"
SUPPORTED_PLATFORM_IDS="debian-9-systemd,debian-10-systemd,debian-11-systemd,debian-12-systemd,debian-13-systemd,ubuntu-18.04-systemd,ubuntu-20.04-systemd,ubuntu-22.04-systemd,ubuntu-24.04-systemd,ubuntu-26.04-systemd,centos-linux-7-systemd,centos-linux-8-systemd,centos-stream-8-systemd,centos-stream-9-systemd,centos-stream-10-systemd"
BOOTSTRAP_ENTRYPOINT_SENTINEL='probe-panel-bootstrap-complete-v1'

PLATFORM_ID=""
PLATFORM_ADAPTER=""
PLATFORM_CONTRACT=""
PLATFORM_PACKAGE_FAMILY=""
PLATFORM_PACKAGE_MANAGER=""
PLATFORM_CA_BUNDLE=""
PLATFORM_SYSTEMD_MIN_VERSION=""
PLATFORM_SYSTEMD_PROFILE=""
PLATFORM_NGINX_INSTALL_PACKAGE=""
PLATFORM_NGINX_BINARY_PACKAGE=""
PLATFORM_NGINX_DIALECT=""
PLATFORM_POSTGRES_SERVER_PACKAGE=""
PLATFORM_POSTGRES_CLIENT_PACKAGE=""
PLATFORM_POSTGRES_SERVICE=""
PLATFORM_POSTGRES_UNIT=""
PLATFORM_PSQL=""
PLATFORM_PG_ISREADY=""
PLATFORM_PG_SOCKET_DIR=""
PLATFORM_CERTBOT_TIMER=""
PLATFORM_NOLOGIN_SHELL=""
PLATFORM_EOL="0"
PLATFORM_APT_CODENAME=""
PLATFORM_APT_BASE_MODE=""
PLATFORM_PGDG_APT_BASE_URL=""
PLATFORM_RPM_EL_MAJOR=""
ACCEPT_EOL=0

SETUP_STATE_ROOT="/var/lib/probe-panel/setup"
SETUP_STATE_FILE="${SETUP_STATE_ROOT}/state.json"
UNSUPPORTED_SETUP_CODE_FILE="${SETUP_STATE_ROOT}/setup-code.json"
SETUP_CONFIG_ROOT="/etc/probe-panel"
SETUP_ENV_FILE="${SETUP_CONFIG_ROOT}/setup.env"
SETUP_UI_ROOT="/srv/probe/setup-ui"
RELEASES_ROOT="/srv/probe/releases"
PROGRAM_ROOT="/usr/local/lib/probe-panel"
SETUP_BINARY="${PROGRAM_ROOT}/probe-setup"
MANAGEMENT_VALIDATE_BINARY="${PROGRAM_ROOT}/validate-management.sh"
MANAGEMENT_UNINSTALL_BINARY="${PROGRAM_ROOT}/uninstall-management.sh"
SETUP_UNIT="/etc/systemd/system/probe-panel-setup.service"
SETUP_SERVICE="probe-panel-setup.service"
SETUP_SOCKET_UNIT="/etc/systemd/system/probe-panel-setup.socket"
SETUP_SOCKET_SERVICE="probe-panel-setup.socket"
SETUP_SOCKET_PATH="/run/probe-panel-setup/setup.sock"
FINALIZER_UNIT="/etc/systemd/system/probe-panel-finalizer.service"
FINALIZER_PATH_UNIT="/etc/systemd/system/probe-panel-finalizer.path"
FINALIZER_SERVICE="probe-panel-finalizer.service"
FINALIZER_PATH_SERVICE="probe-panel-finalizer.path"
FINALIZER_RUNTIME_ROOT="/run/probe-panel-setup"
FINALIZER_REQUEST_FILE="${FINALIZER_RUNTIME_ROOT}/finalize.json"
FINALIZER_RESULT_FILE="${FINALIZER_RUNTIME_ROOT}/result.json"
MANAGED_MARKER=".probe-panel-bootstrap-managed"
BOOTSTRAP_LOCK_FILE="/run/lock/probe-panel-bootstrap.lock"
BOOTSTRAP_LOCK_FD=9

TEMP_ROOT=""
INSTALLED_RELEASE=""
CREATED_STATE=0
CREATED_ENV=0
INSTALLED_UNIT=0
INSTALL_COMPLETED=0
HOST_MUTATION_STARTED=0
NGINX_MASKED_BY_INSTALLER=0
NGINX_PREEXISTED=0
NGINX_ABSENT_AT_START=0
POSTGRESQL_STATE_CAPTURED=0
POSTGRESQL_WAS_ACTIVE=0
POSTGRESQL_PREEXISTED=0
SETUP_SERVER_IP=""
PACKAGE_SOURCE_CREATED_PATHS=()
PACKAGE_SOURCE_CREATED_SHA256=()
PACKAGE_SOURCE_CONSUMED=0

PGDG_APT_KEY_URL='https://www.postgresql.org/media/keys/ACCC4CF8.asc'
PGDG_APT_KEY_SHA256='0144068502a1eddd2a0280ede10ef607d1ec592ce819940991203941564e8e76'
PGDG_APT_KEY_FINGERPRINT='B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8'
PGDG_APT_KEY_PATH='/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc'
PROBE_APT_SOURCE_PATH='/etc/apt/sources.list.d/probe-panel-runtime.list'
# These paths and transaction variables are consumed by the separately linted
# CentOS adapter and by the concatenated standalone installer. Keeping all
# state declarations in common.sh preserves the definition-only adapter rule.
# shellcheck disable=SC2034
PGDG_RPM_KEY_PATH='/etc/pki/rpm-gpg/PROBE-PANEL-PGDG-RPM-GPG-KEY'
# shellcheck disable=SC2034
CENTOS_MANAGED_REPO_DIR='/etc/yum.repos.d/probe-panel-runtime.repos'
# shellcheck disable=SC2034
CENTOS_MANAGED_REPO_PATH="$CENTOS_MANAGED_REPO_DIR/probe-panel-runtime.repo"
# shellcheck disable=SC2034
CENTOS_BASE_KEY_PATH='/etc/pki/rpm-gpg/PROBE-PANEL-CENTOS-RPM-GPG-KEY'
# shellcheck disable=SC2034
CENTOS_EPEL_KEY_PATH='/etc/pki/rpm-gpg/PROBE-PANEL-EPEL-RPM-GPG-KEY'
# shellcheck disable=SC2034
CENTOS_REPO_ALLOWLIST='probe-centos-baseos,probe-centos-appstream,probe-centos-builder,probe-epel,probe-pgdg14'
# shellcheck disable=SC2034
CENTOS_BASE_KEY_FINGERPRINT=''
# shellcheck disable=SC2034
CENTOS_EPEL_KEY_FINGERPRINT=''
# shellcheck disable=SC2034
CENTOS_PGDG_KEY_FINGERPRINT=''
# shellcheck disable=SC2034
CENTOS_MODULE_STATE_PATH='/etc/dnf/modules.d/postgresql.module'
# shellcheck disable=SC2034
CENTOS_MODULE_STATE_CAPTURED=0
# shellcheck disable=SC2034
CENTOS_MODULE_STATE_EXISTED=0
# shellcheck disable=SC2034
CENTOS_MODULE_STATE_MODE=''
# shellcheck disable=SC2034
CENTOS_MODULE_STATE_SNAPSHOT=''
# shellcheck disable=SC2034
CENTOS_MODULE_MUTATION_STARTED=0
# shellcheck disable=SC2034
CENTOS_MODULE_STATE_MUTATED_SHA=''

usage() {
    cat <<EOF
Usage: ${PROGRAM} [install [--accept-eol]|upgrade [--accept-eol]|validate|status|uninstall]

Commands:
  install      Install the management/API first-run setup service (default).
  upgrade      Download this immutable management Release, create a verified
               database backup, migrate and atomically switch an existing
               management installation on the same exact platform.
  validate     Run installed host and runtime checks without building source.
  --accept-eol Explicitly acknowledge a legacy, EOL or extended-maintenance
               operating-system tier.
               This does not restore vendor security maintenance; trusted,
               working package repositories remain the operator's duty.
  status       Show bootstrap files and setup-service status without secrets.
  uninstall    Deactivate the management product and remove its bootstrap
               programs while preserving configuration, setup state,
               PostgreSQL data, backups, and inactive application releases.
  purge        Not supported. Data removal must be an explicit, separately
               reviewed operation with a verified final backup.
  -h, --help   Show this help.

The installer accepts no database or administrator credentials. After install,
open /install through the printed root SSH tunnel and enter secrets only there.
Management mode preserves a valid existing Nginx service/configuration and may
reuse PostgreSQL only when its listener is loopback-only.
Management IP mode can share Nginx on port 18455. Management ACME domain mode
still requires exclusive 80/443 finalization and refuses an active Nginx service.

This v1.2 installer is management-only. PROBE_PANEL_RELEASE_PROFILE cannot
select full; historical full v1.1.0 installations must use the immutable v1.1.0
tag and its matching script. The management v1.2.0 profile remains unreleased
until matching immutable, checksum-verified release assets are published.
EOF
}

log() {
    printf '[probe-panel] %s\n' "$*" >&2
}

warn() {
    printf '[probe-panel] WARNING: %s\n' "$*" >&2
}

die() {
    printf '[probe-panel] ERROR: %s\n' "$*" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "required command is missing: $1"
}

require_root() {
    [[ "$(id -u)" -eq 0 ]] || die 'run this command as root or through sudo'
}

parse_os_release_token() {
    local key="$1" value="$2" length
    [[ -n "$value" ]] || die "$key in os-release is empty"
    case "$value" in
        \"*)
            length=${#value}
            [[ "$length" -ge 2 && "${value: -1}" == '"' ]] ||
                die "$key in os-release has unmatched double quotes"
            value="${value:1:length-2}"
            ;;
        \'*)
            length=${#value}
            [[ "$length" -ge 2 && "${value: -1}" == "'" ]] ||
                die "$key in os-release has unmatched single quotes"
            value="${value:1:length-2}"
            ;;
        *\"*|*\'*)
            die "$key in os-release has non-canonical quoting"
            ;;
    esac
    [[ "$value" =~ ^[A-Za-z0-9._-]+$ ]] ||
        die "$key in os-release contains disallowed characters"
    printf '%s\n' "$value"
}

parse_os_release_name() {
    local value="$1" length
    [[ -n "$value" ]] || die 'NAME in os-release is empty'
    case "$value" in
        \"*)
            length=${#value}
            [[ "$length" -ge 2 && "${value: -1}" == '"' ]] ||
                die 'NAME in os-release has unmatched double quotes'
            value="${value:1:length-2}"
            ;;
        \'*)
            length=${#value}
            [[ "$length" -ge 2 && "${value: -1}" == "'" ]] ||
                die 'NAME in os-release has unmatched single quotes'
            value="${value:1:length-2}"
            ;;
        *\"*|*\'*) die 'NAME in os-release has non-canonical quoting' ;;
    esac
    [[ "$value" =~ ^[A-Za-z0-9._/[:space:]-]+$ ]] ||
        die 'NAME in os-release contains disallowed characters'
    printf '%s\n' "$value"
}

configure_deb_platform() {
    PLATFORM_ID="$1"
    PLATFORM_SYSTEMD_MIN_VERSION="$2"
    PLATFORM_SYSTEMD_PROFILE="$3"
    PLATFORM_NGINX_INSTALL_PACKAGE="$4"
    PLATFORM_NGINX_BINARY_PACKAGE="$5"
    PLATFORM_NGINX_DIALECT="$6"
    PLATFORM_POSTGRES_SERVER_PACKAGE="$7"
    PLATFORM_POSTGRES_CLIENT_PACKAGE="$8"
    PLATFORM_EOL="${9:-0}"
    PLATFORM_PACKAGE_FAMILY='deb'
    PLATFORM_PACKAGE_MANAGER='apt-get'
    PLATFORM_CA_BUNDLE='/etc/ssl/certs/ca-certificates.crt'
    PLATFORM_POSTGRES_SERVICE='postgresql.service'
    PLATFORM_POSTGRES_UNIT='postgresql.service'
    PLATFORM_PSQL='/usr/bin/psql'
    PLATFORM_PG_ISREADY='/usr/bin/pg_isready'
    PLATFORM_PG_SOCKET_DIR='/var/run/postgresql'
    PLATFORM_CERTBOT_TIMER='certbot.timer'
    PLATFORM_NOLOGIN_SHELL='/usr/sbin/nologin'
}

configure_rpm_platform() {
    PLATFORM_ID="$1"
    PLATFORM_PACKAGE_MANAGER="$2"
    PLATFORM_SYSTEMD_MIN_VERSION="$3"
    PLATFORM_SYSTEMD_PROFILE="$4"
    PLATFORM_NGINX_DIALECT="$5"
    PLATFORM_NGINX_BINARY_PACKAGE="$6"
    PLATFORM_EOL="${7:-0}"
    PLATFORM_PACKAGE_FAMILY='rpm'
    PLATFORM_CA_BUNDLE='/etc/pki/tls/certs/ca-bundle.crt'
    PLATFORM_NGINX_INSTALL_PACKAGE='nginx'
    PLATFORM_POSTGRES_SERVER_PACKAGE='postgresql14-server'
    PLATFORM_POSTGRES_CLIENT_PACKAGE='postgresql14'
    PLATFORM_POSTGRES_SERVICE='postgresql-14.service'
    PLATFORM_POSTGRES_UNIT='postgresql-14.service'
    PLATFORM_PSQL='/usr/pgsql-14/bin/psql'
    PLATFORM_PG_ISREADY='/usr/pgsql-14/bin/pg_isready'
    PLATFORM_PG_SOCKET_DIR='/var/run/postgresql'
    PLATFORM_CERTBOT_TIMER='certbot-renew.timer'
    PLATFORM_NOLOGIN_SHELL='/sbin/nologin'
    PLATFORM_RPM_EL_MAJOR="${PLATFORM_ID#centos-*-}"
    PLATFORM_RPM_EL_MAJOR="${PLATFORM_RPM_EL_MAJOR%%-*}"
}

# The optional path is only for data-only contract fixtures; production calls
# intentionally use the default /etc/os-release path.
# shellcheck disable=SC2120
select_supported_platform() {
    # The optional path exists only so the pure parser/selector can be exercised
    # against contract fixtures. Production always uses the default path. A
    # standards-compliant /etc/os-release symlink to ../usr/lib/os-release is
    # accepted because -f follows it to its readable regular-file target.
    local os_release="${1:-/etc/os-release}"
    [[ -r "$os_release" && -f "$os_release" ]] ||
        die "$os_release is missing or does not resolve to a readable regular file"

    PLATFORM_ID=''
    PLATFORM_ADAPTER=''
    PLATFORM_CONTRACT=''
    PLATFORM_PACKAGE_FAMILY=''
    PLATFORM_PACKAGE_MANAGER=''
    PLATFORM_CA_BUNDLE=''
    PLATFORM_SYSTEMD_MIN_VERSION=''
    PLATFORM_SYSTEMD_PROFILE=''
    PLATFORM_NGINX_INSTALL_PACKAGE=''
    PLATFORM_NGINX_BINARY_PACKAGE=''
    PLATFORM_NGINX_DIALECT=''
    PLATFORM_POSTGRES_SERVER_PACKAGE=''
    PLATFORM_POSTGRES_CLIENT_PACKAGE=''
    PLATFORM_POSTGRES_SERVICE=''
    PLATFORM_POSTGRES_UNIT=''
    PLATFORM_PSQL=''
    PLATFORM_PG_ISREADY=''
    PLATFORM_PG_SOCKET_DIR=''
    PLATFORM_CERTBOT_TIMER=''
    PLATFORM_NOLOGIN_SHELL=''
    PLATFORM_EOL='0'
    PLATFORM_APT_CODENAME=''
    PLATFORM_APT_BASE_MODE=''
    PLATFORM_PGDG_APT_BASE_URL=''
    PLATFORM_RPM_EL_MAJOR=''
    local os_id='' version_id='' os_name='' line raw_value selected_adapter=''
    local id_count=0 version_count=0 name_count=0
    while IFS= read -r line || [[ -n "$line" ]]; do
        case "$line" in
            ''|'#'*) continue ;;
            ID=*)
                id_count=$((id_count + 1))
                raw_value="${line#ID=}"
                os_id="$(parse_os_release_token ID "$raw_value")"
                ;;
            VERSION_ID=*)
                version_count=$((version_count + 1))
                raw_value="${line#VERSION_ID=}"
                version_id="$(parse_os_release_token VERSION_ID "$raw_value")"
                ;;
            NAME=*)
                name_count=$((name_count + 1))
                raw_value="${line#NAME=}"
                os_name="$(parse_os_release_name "$raw_value")"
                ;;
        esac
    done < "$os_release"
    [[ "$id_count" -eq 1 && "$version_count" -eq 1 ]] ||
        die 'os-release must declare exactly one ID and one VERSION_ID'

    (( name_count <= 1 )) || die 'os-release must declare NAME at most once'

    case "$os_id" in
        debian|ubuntu|centos)
            PLATFORM_ADAPTER="$os_id"
            selected_adapter="$os_id"
            ;;
        *)
            die "platform ${os_id:-unknown} ${version_id:-unknown} ${os_name:-unknown} is not in the accepted candidate matrix; accepted candidate platform IDs: $SUPPORTED_PLATFORM_IDS"
            ;;
    esac
    platform_adapter_call configure "$version_id" "$os_name"
    [[ "$PLATFORM_ADAPTER" == "$selected_adapter" ]] ||
        die 'the selected platform adapter changed its own dispatch identity'
    validate_platform_adapter_api
    PLATFORM_CONTRACT="$MANAGEMENT_RUNTIME_ABI"
}

platform_adapter_call() {
    local operation="$1" function_name
    shift
    [[ "$PLATFORM_ADAPTER" =~ ^(debian|ubuntu|centos)$ ]] ||
        die 'the selected platform adapter is unavailable'
    function_name="${PLATFORM_ADAPTER}_platform_${operation}"
    declare -F "$function_name" >/dev/null ||
        die "the selected platform adapter does not implement $operation"
    "$function_name" "$@"
}

validate_platform_adapter_api() {
    local operation
    for operation in \
        configure preflight_commands native_unit_paths assert_packaged_file \
        assert_postgresql_clients preflight_security runtime_packages \
        prepare_package_sources install_packages initialize_postgresql create_service_account \
        validate_nologin_shell disable_default_nginx_site; do
        declare -F "${PLATFORM_ADAPTER}_platform_${operation}" >/dev/null ||
            die "the selected platform adapter is incomplete: $operation"
    done
    [[ -n "$PLATFORM_ID" && -n "$PLATFORM_PACKAGE_FAMILY" &&
       -n "$PLATFORM_PACKAGE_MANAGER" && -n "$PLATFORM_NOLOGIN_SHELL" ]] ||
        die 'the selected platform adapter returned an incomplete contract'
}

validate_platform_lifecycle() {
    [[ "$PLATFORM_EOL" == 0 || "$PLATFORM_EOL" == 1 ]] ||
        die 'the selected platform lifecycle contract is invalid'
    if [[ "$PLATFORM_EOL" == 1 ]]; then
        [[ "$ACCEPT_EOL" == 1 ]] ||
            die "platform $PLATFORM_ID requires explicit legacy/EOL lifecycle acknowledgement; rerun with --accept-eol only after arranging a trusted maintained, extended-maintenance or frozen package source"
        warn "continuing on lifecycle-restricted candidate $PLATFORM_ID by explicit request; the installer cannot provide or restore operating-system security maintenance"
    fi
}

platform_contract_fingerprint() {
    printf '%s\n' \
        "$PLATFORM_ID|$PLATFORM_ADAPTER|$PLATFORM_CONTRACT|$PLATFORM_PACKAGE_FAMILY|$PLATFORM_PACKAGE_MANAGER|$PLATFORM_CA_BUNDLE|$PLATFORM_SYSTEMD_MIN_VERSION|$PLATFORM_SYSTEMD_PROFILE|$PLATFORM_NGINX_INSTALL_PACKAGE|$PLATFORM_NGINX_BINARY_PACKAGE|$PLATFORM_NGINX_DIALECT|$PLATFORM_POSTGRES_SERVER_PACKAGE|$PLATFORM_POSTGRES_CLIENT_PACKAGE|$PLATFORM_POSTGRES_SERVICE|$PLATFORM_POSTGRES_UNIT|$PLATFORM_PSQL|$PLATFORM_PG_ISREADY|$PLATFORM_PG_SOCKET_DIR|$PLATFORM_CERTBOT_TIMER|$PLATFORM_NOLOGIN_SHELL|$PLATFORM_EOL|$PLATFORM_APT_CODENAME|$PLATFORM_APT_BASE_MODE|$PLATFORM_PGDG_APT_BASE_URL|$PLATFORM_RPM_EL_MAJOR"
}

selected_setup_asset_name() {
    local modern_name="$1"
    case "$modern_name" in
        probe-panel-setup.service|probe-panel-setup.socket|probe-panel-finalizer-management.service) ;;
        *) die "unsupported setup systemd asset: $modern_name" ;;
    esac
    if [[ "$PLATFORM_SYSTEMD_PROFILE" == legacy ]]; then
        printf '%s-legacy.%s\n' "${modern_name%.*}" "${modern_name##*.}"
    else
        [[ "$PLATFORM_SYSTEMD_PROFILE" == modern ]] ||
            die 'the selected platform systemd profile is unavailable'
        printf '%s\n' "$modern_name"
    fi
}

detect_architecture() {
    case "$(uname -m)" in
        x86_64|amd64) printf '%s\n' amd64 ;;
        aarch64|arm64) printf '%s\n' arm64 ;;
        *) die "architecture $(uname -m) is not in the accepted candidate matrix; expected amd64 or arm64" ;;
    esac
}

validate_https_url() {
    local url="$1" remainder authority
    [[ "$url" == https://* ]] || die 'release base URL must use HTTPS'
    remainder="${url#https://}"
    [[ -n "$remainder" && "$remainder" != /* && "$remainder" != *'@'* &&
       "$remainder" != *'?'* && "$remainder" != *'#'* &&
       "$remainder" != *[[:space:]]* ]] || die 'release base URL is malformed'
    authority="${remainder%%/*}"
    [[ -n "$authority" ]] || die 'release base URL has no host'
}

validate_release_settings() {
    [[ "$REQUESTED_PROFILE" == management ]] ||
        die 'this v1.2 installer is management-only; use the immutable v1.1.0 tag and script for historical full installations'
    [[ "$PANEL_VERSION" == "$MANAGEMENT_VERSION" ]] ||
        die "the management installer is pinned to the unreleased $MANAGEMENT_VERSION contract"
    validate_https_url "$RELEASE_BASE_URL"
}

release_bundle_name() {
    local profile="$1" version="$2" architecture="$3"
    case "$profile" in
        management) printf 'probe-panel-management-%s-linux-%s\n' "$version" "$architecture" ;;
        *) die "the v1.2 installer accepts management bundles only" ;;
    esac
}

release_asset_name() {
    local bundle_name

    bundle_name="$(release_bundle_name "$1" "$2" "$3")" || return $?
    printf '%s.tar.gz\n' "$bundle_name"
}

unit_is_installed() {
    local load_state fragment
    load_state="$(systemd_property "$1" LoadState 2>/dev/null || :)"
    fragment="$(systemd_property "$1" FragmentPath 2>/dev/null || :)"
    [[ -n "$fragment" || ( -n "$load_state" && "$load_state" != not-found ) ]]
}

systemd_property() {
    local unit="$1" property="$2" output
    [[ "$property" =~ ^[A-Za-z][A-Za-z0-9]*$ ]] || return 2
    output="$(systemctl show --property="$property" "$unit" 2>/dev/null)" || return 1
    [[ "$output" == "$property="* && "$output" != *$'\n'* ]] || return 1
    printf '%s\n' "${output#*=}"
}

acquire_bootstrap_lock() {
    local lock_root_mode path_identity descriptor_identity
    [[ -d /run/lock && ! -L /run/lock ]] || die '/run/lock must be a real directory'
    [[ "$(stat -c '%u:%g' /run/lock)" == 0:0 ]] || die '/run/lock must be owned by root:root'
    lock_root_mode="$(stat -c '%a' /run/lock)"
    [[ "$lock_root_mode" =~ ^[0-7]{3,4}$ ]] || die '/run/lock has an invalid mode'
    if [[ "$lock_root_mode" != 1777 ]]; then
        (( (8#$lock_root_mode & 0022) == 0 )) ||
            die '/run/lock must be root:root mode 1777 with sticky bit, or must not be group/world-writable'
    fi

    if [[ -L "$BOOTSTRAP_LOCK_FILE" || ( -e "$BOOTSTRAP_LOCK_FILE" && ! -f "$BOOTSTRAP_LOCK_FILE" ) ]]; then
        die "$BOOTSTRAP_LOCK_FILE must be a real regular file"
    fi
    if [[ ! -e "$BOOTSTRAP_LOCK_FILE" ]]; then
        # noclobber uses an exclusive create. If another process or an
        # unprivileged user wins the race in a sticky /run/lock, the
        # validation below rejects that object without following or replacing it.
        (umask 077; set -o noclobber; : > "$BOOTSTRAP_LOCK_FILE") 2>/dev/null || :
    fi
    [[ -f "$BOOTSTRAP_LOCK_FILE" && ! -L "$BOOTSTRAP_LOCK_FILE" ]] ||
        die "$BOOTSTRAP_LOCK_FILE could not be created as a real regular file"
    [[ "$(stat -c '%u:%g:%a' "$BOOTSTRAP_LOCK_FILE")" == 0:0:600 ]] ||
        die "$BOOTSTRAP_LOCK_FILE must be root:root mode 0600"

    exec 9>>"$BOOTSTRAP_LOCK_FILE"
    [[ -f "$BOOTSTRAP_LOCK_FILE" && ! -L "$BOOTSTRAP_LOCK_FILE" ]] ||
        die "$BOOTSTRAP_LOCK_FILE changed while it was being opened"
    path_identity="$(stat -c '%d:%i' "$BOOTSTRAP_LOCK_FILE")"
    descriptor_identity="$(stat -Lc '%d:%i' "/proc/self/fd/$BOOTSTRAP_LOCK_FD")"
    [[ "$path_identity" == "$descriptor_identity" ]] ||
        die "$BOOTSTRAP_LOCK_FILE changed while it was being opened"
    flock --exclusive --nonblock "$BOOTSTRAP_LOCK_FD" ||
        die 'another Probe Panel install or uninstall operation is already running'
}

capture_postgresql_start_state() {
    POSTGRESQL_WAS_ACTIVE=0
    [[ -n "$PLATFORM_POSTGRES_SERVICE" ]] || die 'the PostgreSQL service contract is unavailable'
    if systemctl is-active --quiet "$PLATFORM_POSTGRES_SERVICE"; then
        POSTGRESQL_WAS_ACTIVE=1
    fi
    POSTGRESQL_STATE_CAPTURED=1
}

assert_fresh_target() {
    local path
    if [[ -e "$UNSUPPORTED_SETUP_CODE_FILE" || -L "$UNSUPPORTED_SETUP_CODE_FILE" ]]; then
        die "a legacy bootstrap record is present; this management-only installer will not migrate or overwrite it. Use the matching immutable historical tag/script or a separately reviewed recovery"
    fi
    for path in \
        "$SETUP_BINARY" "$SETUP_UNIT" "$SETUP_SOCKET_UNIT" "$FINALIZER_UNIT" "$FINALIZER_PATH_UNIT" \
        "$SETUP_ENV_FILE" "$SETUP_UI_ROOT" \
        "$SETUP_STATE_FILE" "$UNSUPPORTED_SETUP_CODE_FILE" \
        /etc/systemd/system/probe-api.service \
        /etc/nginx/conf.d/probe-panel.conf \
        /srv/probe/api/probe-api /srv/probe/admin; do
        if [[ -e "$path" || -L "$path" ]]; then
            die "existing or partial Probe Panel installation found at $path; refusing to overwrite it"
        fi
    done
    if unit_is_installed "$SETUP_SERVICE" || unit_is_installed "$SETUP_SOCKET_SERVICE" || unit_is_installed "$FINALIZER_SERVICE" ||
       unit_is_installed "$FINALIZER_PATH_SERVICE" || unit_is_installed probe-api.service; then
        die 'an existing Probe Panel systemd service was found; refusing to replace it'
    fi
}

require_preflight_commands() {
    local command_name
    local -a missing_commands=() platform_commands=()
    for command_name in \
        awk chmod chown cp cut find flock getent grep id install ip \
        journalctl mkdir mktemp mv ps readlink rm runuser sed sha256sum sleep sort ss stat \
        systemctl systemd-analyze timeout uname unlink wc; do
        if ! command -v "$command_name" >/dev/null 2>&1; then
            missing_commands+=("$command_name")
        fi
    done
    mapfile -t platform_commands < <(platform_adapter_call preflight_commands)
    (( ${#platform_commands[@]} > 0 )) ||
        die 'the selected platform adapter returned no preflight commands'
    for command_name in "${platform_commands[@]}"; do
        [[ "$command_name" =~ ^[A-Za-z0-9._+-]+$ ]] ||
            die 'the selected platform adapter returned an invalid preflight command'
        command -v "$command_name" >/dev/null 2>&1 || missing_commands+=("$command_name")
    done
    if ! curl_supports_release_tls && ! command -v wget >/dev/null 2>&1; then
        missing_commands+=(curl-with-tls1.2-or-wget)
    fi
    if (( ${#missing_commands[@]} > 0 )); then
        die "release-verification prerequisites are missing: ${missing_commands[*]}. Install the candidate platform prerequisites (including ca-certificates, curl or wget, python3, coreutils, findutils, grep, sed, awk, iproute/iproute2, util-linux, and procps/procps-ng) before retrying; this installer will not change the host to fetch or validate a release"
    fi
    require_command python3
    [[ -n "$PLATFORM_CA_BUNDLE" && -s "$PLATFORM_CA_BUNDLE" && -f "$PLATFORM_CA_BUNDLE" ]] ||
        die "the platform CA certificate bundle is missing at ${PLATFORM_CA_BUNDLE:-unknown}; install ca-certificates before retrying"
}

ensure_package_source_directory() {
    local directory="$1" mode
    [[ "$directory" == /* ]] || die 'package-source directories must be absolute'
    if [[ ! -e "$directory" && ! -L "$directory" ]]; then
        install -d -o root -g root -m 0755 -- "$directory"
    fi
    [[ -d "$directory" && ! -L "$directory" ]] ||
        die "$directory must be a real package-source directory"
    [[ "$(stat -c '%u:%g' "$directory")" == 0:0 ]] ||
        die "$directory must be owned by root:root"
    mode="$(stat -c '%a' "$directory")"
    [[ "$mode" =~ ^[0-7]{3,4}$ ]] || die "$directory has an invalid mode"
    (( (8#$mode & 8#7022) == 0 )) ||
        die "$directory must not have special bits or be writable by group or other users"
}

install_managed_package_source() {
    local source_file="$1" target_file="$2" expected_mode="$3"
    local source_sha target_sha target_mode
    [[ -f "$source_file" && ! -L "$source_file" && "$target_file" == /* ]] ||
        die 'the managed package-source input is unsafe'
    source_sha="$(sha256sum "$source_file" | awk '{print $1}')"
    [[ "$source_sha" =~ ^[0-9a-f]{64}$ ]] || die 'could not hash a managed package-source input'

    if [[ -e "$target_file" || -L "$target_file" ]]; then
        [[ -f "$target_file" && ! -L "$target_file" ]] ||
            die "$target_file must be a real regular file"
        [[ "$(stat -c '%u:%g' "$target_file")" == 0:0 ]] ||
            die "$target_file must be owned by root:root"
        target_mode="$(stat -c '%a' "$target_file")"
        [[ "$target_mode" == "$expected_mode" ]] ||
            die "$target_file must have mode $expected_mode"
        target_sha="$(sha256sum "$target_file" | awk '{print $1}')"
        [[ "$target_sha" == "$source_sha" ]] ||
            die "$target_file already exists with content not owned by this immutable installer; refusing to overwrite it"
        return 0
    fi

    install -o root -g root -m "$expected_mode" -- "$source_file" "$target_file"
    [[ -f "$target_file" && ! -L "$target_file" &&
       "$(stat -c '%u:%g:%a' "$target_file")" == "0:0:$expected_mode" ]] ||
        die "could not install the managed package source $target_file safely"
    target_sha="$(sha256sum "$target_file" | awk '{print $1}')"
    [[ "$target_sha" == "$source_sha" ]] || die "$target_file changed while it was installed"
    PACKAGE_SOURCE_CREATED_PATHS+=("$target_file")
    PACKAGE_SOURCE_CREATED_SHA256+=("$source_sha")
}

rollback_created_package_sources() {
    local index path expected_sha current_sha
    for ((index=${#PACKAGE_SOURCE_CREATED_PATHS[@]} - 1; index >= 0; index--)); do
        path="${PACKAGE_SOURCE_CREATED_PATHS[index]}"
        expected_sha="${PACKAGE_SOURCE_CREATED_SHA256[index]}"
        if [[ -f "$path" && ! -L "$path" && "$(stat -c '%u:%g' "$path" 2>/dev/null)" == 0:0 ]]; then
            current_sha="$(sha256sum "$path" 2>/dev/null | awk '{print $1}')"
            if [[ "$current_sha" == "$expected_sha" ]]; then
                rm -f -- "$path"
                continue
            fi
        elif [[ ! -e "$path" && ! -L "$path" ]]; then
            continue
        fi
        warn "refusing to remove package-source path that changed after creation: $path"
    done
}

assert_openpgp_primary_fingerprint() {
    local key_file="$1" expected_fingerprint="$2"
    [[ -f "$key_file" && ! -L "$key_file" && "$expected_fingerprint" =~ ^[0-9A-F]{40}$ ]] ||
        die 'the OpenPGP fingerprint contract is invalid'
    python3 - "$key_file" "$expected_fingerprint" <<'PY'
import base64
import hashlib
import sys

path, expected = sys.argv[1:]
lines = open(path, "r", encoding="ascii", errors="strict").read().splitlines()
payload = []
inside = False
ended = False
for line in lines:
    if line == "-----BEGIN PGP PUBLIC KEY BLOCK-----":
        if inside or ended:
            raise SystemExit(1)
        inside = True
        continue
    if line == "-----END PGP PUBLIC KEY BLOCK-----":
        if not inside:
            raise SystemExit(1)
        ended = True
        inside = False
        continue
    if not inside or not line or ":" in line or line.startswith("="):
        continue
    payload.append(line)
if not ended or inside:
    raise SystemExit(1)
raw = base64.b64decode("".join(payload), validate=True)
offset = 0
fingerprints = []
while offset < len(raw):
    header = raw[offset]
    offset += 1
    if not header & 0x80:
        raise SystemExit(1)
    if header & 0x40:
        tag = header & 0x3F
        first = raw[offset]
        offset += 1
        if first < 192:
            length = first
        elif first < 224:
            length = ((first - 192) << 8) + raw[offset] + 192
            offset += 1
        elif first == 255:
            length = int.from_bytes(raw[offset:offset + 4], "big")
            offset += 4
        else:
            raise SystemExit(1)
    else:
        tag = (header >> 2) & 0x0F
        length_type = header & 0x03
        widths = (1, 2, 4)
        if length_type == 3:
            raise SystemExit(1)
        width = widths[length_type]
        length = int.from_bytes(raw[offset:offset + width], "big")
        offset += width
    body = raw[offset:offset + length]
    if len(body) != length:
        raise SystemExit(1)
    offset += length
    if tag == 6:
        if not body or body[0] != 4 or len(body) > 65535:
            raise SystemExit(1)
        fingerprints.append(hashlib.sha1(b"\x99" + len(body).to_bytes(2, "big") + body).hexdigest().upper())
if fingerprints != [expected]:
    raise SystemExit(1)
PY
}

download_fixed_openpgp_key() {
    local url="$1" expected_sha="$2" expected_fingerprint="$3" output="$4" actual_sha
    download_file "$output" "$url" 60
    [[ -s "$output" && "$(stat -c '%s' "$output")" -le 65536 ]] ||
        die 'a package signing key is empty or exceeds 64 KiB'
    actual_sha="$(sha256sum "$output" | awk '{print $1}')"
    [[ "$actual_sha" == "$expected_sha" ]] ||
        die "package signing key SHA-256 mismatch (expected $expected_sha, got ${actual_sha:-missing})"
    assert_openpgp_primary_fingerprint "$output" "$expected_fingerprint" ||
        die "package signing key fingerprint mismatch (expected $expected_fingerprint)"
}

curl_supports_release_tls() {
    command -v curl >/dev/null 2>&1 &&
        curl -q --tlsv1.2 --version >/dev/null 2>&1
}

assert_secure_preexisting_path() {
    local path="$1" expected_type="$2" mode
    case "$expected_type" in
        file)
            [[ -f "$path" && ! -L "$path" ]] || die "$path must be a real regular file"
            ;;
        directory)
            [[ -d "$path" && ! -L "$path" ]] || die "$path must be a real directory"
            ;;
        *)
            die "internal preflight type is invalid for $path"
            ;;
    esac
    [[ "$(stat -c '%u:%g' "$path")" == 0:0 ]] || die "$path must be owned by root:root"
    mode="$(stat -c '%a' "$path")"
    [[ "$mode" =~ ^[0-7]{3,4}$ ]] || die "$path has an invalid mode"
    (( (8#$mode & 8#7022) == 0 )) ||
        die "$path must not have special bits or be writable by group or other users"
}

assert_native_unit_wants_link() {
    local wants_path="$1" expected_fragment="$2" resolved_target
    [[ -e "$wants_path" || -L "$wants_path" ]] || return 0
    [[ -L "$wants_path" ]] ||
        die "$wants_path must be a symbolic link to the reviewed native systemd unit"
    [[ "$(stat -c '%u:%g' "$wants_path")" == 0:0 ]] ||
        die "$wants_path must be a root-owned symbolic link"
    resolved_target="$(readlink -f -- "$wants_path" 2>/dev/null || :)"
    [[ "$resolved_target" == "$expected_fragment" ]] ||
        die "$wants_path does not target the reviewed native systemd unit $expected_fragment"
}

assert_native_systemd_unit() {
    local service="$1" fragment drop_in_paths expected_fragment matched_fragment=''
    shift
    (($# > 0)) || die "no native systemd unit path contract was supplied for $service"
    unit_is_installed "$service" || die "$service is missing or is not recognized by systemd"
    fragment="$(systemd_property "$service" FragmentPath 2>/dev/null || :)"
    if [[ -n "$fragment" && -f "$fragment" && ! -L "$fragment" ]]; then
        fragment="$(readlink -f -- "$fragment")"
        for expected_fragment in "$@"; do
            if [[ "$fragment" == "$expected_fragment" ]]; then
                matched_fragment="$expected_fragment"
                break
            fi
        done
    fi
    [[ -n "$matched_fragment" ]] ||
        die "candidate platform prerequisites require a reviewed native unit for $service (found ${fragment:-missing})"
    drop_in_paths="$(systemd_property "$service" DropInPaths 2>/dev/null || :)"
    [[ -z "$drop_in_paths" ]] ||
        die "$service has systemd drop-ins and cannot be treated as an unmodified native unit"
    [[ ! -e "/etc/systemd/system/$service" && ! -L "/etc/systemd/system/$service" &&
       ! -e "/etc/systemd/system/$service.d" && ! -L "/etc/systemd/system/$service.d" ]] ||
        die "$service has a persistent systemd alias, override, or drop-in under /etc"
    assert_native_unit_wants_link \
        "/etc/systemd/system/multi-user.target.wants/$service" "$matched_fragment"
    assert_native_unit_wants_link \
        "/run/systemd/system/multi-user.target.wants/$service" "$matched_fragment"
}

assert_platform_native_unit() {
    local service="$1" unit_name="$2"
    local -a native_unit_paths=()
    [[ "$unit_name" != */* && "$unit_name" == *.service ]] ||
        die "invalid native systemd unit name contract: $unit_name"
    mapfile -t native_unit_paths < <(platform_adapter_call native_unit_paths "$unit_name")
    (( ${#native_unit_paths[@]} > 0 )) ||
        die 'the selected platform adapter returned no native unit paths'
    assert_native_systemd_unit "$service" "${native_unit_paths[@]}"
}

assert_deb_family_packaged_file() {
    local file_path="$1" package_name="$2" package_owner package_status
    assert_secure_preexisting_path "$file_path" file
    package_owner="$(dpkg-query --search "$file_path" 2>/dev/null || :)"
    [[ "$package_owner" == "$package_name: $file_path" ]] ||
        die "$file_path is not owned by the expected deb-family package $package_name"
    # dpkg-query expands this format token; the shell must preserve it literally.
    # shellcheck disable=SC2016
    package_status="$(dpkg-query --show --showformat='${Status}' "$package_name" 2>/dev/null || :)"
    [[ "$package_status" == 'install ok installed' ]] ||
        die "the deb-family package $package_name is not fully installed"
}

assert_rpm_packaged_file() {
    local file_path="$1" package_name="$2" package_owner package_status package_signatures
    local signature key_id expected_fingerprint expected_key_id trusted_signature=0
    local -a expected_key_ids=()
    shift 2
    (($# > 0)) || die "the RPM package $package_name has no signing-key binding contract"
    for expected_fingerprint in "$@"; do
        [[ "$expected_fingerprint" =~ ^[0-9A-F]{40}$ ]] ||
            die "the RPM package $package_name has an invalid signing-key fingerprint contract"
        expected_key_id="${expected_fingerprint: -8}"
        expected_key_ids+=("${expected_key_id,,}")
    done
    assert_secure_preexisting_path "$file_path" file
    package_owner="$(rpm -qf --qf '%{NAME}\n' "$file_path" 2>/dev/null || :)"
    [[ "$package_owner" == "$package_name" ]] ||
        die "$file_path is not owned by the expected RPM package $package_name"
    package_status="$(rpm -q --qf '%{NAME}\n' "$package_name" 2>/dev/null || :)"
    [[ "$package_status" == "$package_name" ]] ||
        die "the RPM package $package_name is not installed"
    package_signatures="$(rpm -q --qf '%{RSAHEADER:pgpsig}\n%{DSAHEADER:pgpsig}\n%{SIGPGP:pgpsig}\n%{SIGGPG:pgpsig}\n' \
        "$package_name" 2>/dev/null || :)"
    while IFS= read -r signature; do
        [[ -n "$signature" && "$signature" != *'(none)'* ]] || continue
        if [[ "$signature" =~ [Kk]ey[[:space:]]+[Ii][Dd][[:space:]]+([0-9A-Fa-f]{8,16}) ]]; then
            key_id="${BASH_REMATCH[1],,}"
            key_id="${key_id: -8}"
            for expected_key_id in "${expected_key_ids[@]}"; do
                if [[ "$expected_key_id" == "$key_id" ]]; then
                    if rpm -qa --qf '%{NAME}|%{VERSION}\n' 2>/dev/null |
                       awk -F'|' -v wanted="$expected_key_id" '
                           $1 == "gpg-pubkey" && tolower($2) == wanted { found=1 }
                           END { exit !found }
                       '; then
                        trusted_signature=1
                        break 2
                    fi
                fi
            done
        fi
    done <<< "$package_signatures"
    [[ "$trusted_signature" -eq 1 ]] ||
        die "the installed RPM package $package_name has no signature linked to an imported trusted key"
}

assert_platform_packaged_file() {
    platform_adapter_call assert_packaged_file "$@"
}

assert_deb_family_packaged_wrapper() {
    local entry_path="$1" expected_target="$2" package_name="$3"
    local resolved_target
    [[ "$entry_path" == /* && "$expected_target" == /* ]] ||
        die 'deb-family wrapper validation requires absolute paths'
    [[ -L "$entry_path" ]] || die "$entry_path must be the deb-family symbolic-link entrypoint"
    [[ "$(stat -c '%u:%g' "$entry_path")" == 0:0 ]] ||
        die "$entry_path must be a root-owned symbolic link"
    resolved_target="$(readlink -f -- "$entry_path")"
    [[ "$resolved_target" == "$expected_target" ]] ||
        die "$entry_path does not resolve to the candidate deb-family wrapper: ${resolved_target:-missing}"
    assert_deb_family_packaged_file "$expected_target" "$package_name"
}

assert_native_deb_family_postgresql_clients() {
    local psql_entry pg_isready_entry
    psql_entry="$(command -v psql 2>/dev/null || :)"
    pg_isready_entry="$(command -v pg_isready 2>/dev/null || :)"
    [[ "$psql_entry" == /usr/bin/psql && "$pg_isready_entry" == /usr/bin/pg_isready ]] ||
        die 'PostgreSQL client commands must enter through /usr/bin/psql and /usr/bin/pg_isready'
    assert_deb_family_packaged_wrapper \
        /usr/bin/psql /usr/share/postgresql-common/pg_wrapper postgresql-client-common
    assert_deb_family_packaged_wrapper \
        /usr/bin/pg_isready /usr/share/postgresql-common/pg_wrapper postgresql-client-common
    assert_deb_family_packaged_file /usr/lib/postgresql/14/bin/psql postgresql-client-14
    assert_deb_family_packaged_file /usr/lib/postgresql/14/bin/pg_isready postgresql-client-14
}

deb_family_platform_preflight_commands() {
    printf '%s\n' addgroup adduser apt-cache apt-get dpkg dpkg-query
}

deb_family_platform_native_unit_paths() {
    local unit_name="$1"
    printf '/usr/lib/systemd/system/%s\n/lib/systemd/system/%s\n' "$unit_name" "$unit_name"
}

deb_family_platform_assert_packaged_file() {
    assert_deb_family_packaged_file "$@"
}

deb_family_platform_assert_postgresql_clients() {
    assert_native_deb_family_postgresql_clients
}

deb_family_platform_preflight_security() {
    :
}

deb_family_platform_runtime_packages() {
    printf '%s\n' ca-certificates curl python3 certbot iproute2 util-linux
}

debian9_ensure_apt_https_transport() {
    [[ "$PLATFORM_ID" == debian-9-systemd ]] || return 0
    [[ -x /usr/lib/apt/methods/https && ! -L /usr/lib/apt/methods/https ]] && return 0
    die 'Debian 9 requires a preinstalled, non-symlink /usr/lib/apt/methods/https (normally apt-transport-https); automatic bootstrap remains disabled until an unexpired, pinned Debian archive keyring path is formally verified'
}

deb_family_platform_prepare_package_sources() {
    local deb_arch key_download source_candidate pgdg_validity_option=''
    local distribution_keyring distribution_keyring_package
    [[ -n "$TEMP_ROOT" && "$TEMP_ROOT" == /var/tmp/probe-panel-bootstrap.* &&
       -d "$TEMP_ROOT" && ! -L "$TEMP_ROOT" ]] ||
        die 'a private bootstrap workspace is required before package-source preparation'
    [[ -n "$PLATFORM_APT_CODENAME" &&
       "$PLATFORM_APT_BASE_MODE" =~ ^(debian-archive|debian-live|ubuntu-live)$ &&
       "$PLATFORM_PGDG_APT_BASE_URL" =~ ^https:// ]] ||
        die 'the deb-family package-source contract is incomplete'
    if [[ "$PLATFORM_EOL" == 1 && "$ACCEPT_EOL" != 1 ]]; then
        die 'an EOL package source cannot be prepared without --accept-eol'
    fi

    deb_arch="$(dpkg --print-architecture 2>/dev/null)" ||
        die 'could not determine the deb-family package architecture'
    [[ "$deb_arch" == amd64 || "$deb_arch" == arm64 ]] ||
        die "the PGDG 14 package source does not support deb architecture $deb_arch in this installer"
    if [[ "$PLATFORM_APT_CODENAME" == stretch && "$deb_arch" == arm64 ]]; then
        die 'the official archived stretch-pgdg arm64 index does not contain PostgreSQL 14; Debian 9 arm64 remains an unavailable candidate'
    fi
    debian9_ensure_apt_https_transport

    case "$PLATFORM_ADAPTER" in
        debian)
            distribution_keyring='/usr/share/keyrings/debian-archive-keyring.gpg'
            distribution_keyring_package='debian-archive-keyring'
            ;;
        ubuntu)
            distribution_keyring='/usr/share/keyrings/ubuntu-archive-keyring.gpg'
            distribution_keyring_package='ubuntu-keyring'
            ;;
        *) die 'the deb-family distribution keyring contract is unavailable' ;;
    esac
    assert_deb_family_packaged_file "$distribution_keyring" "$distribution_keyring_package"
    [[ -s "$distribution_keyring" ]] ||
        die "the packaged distribution archive keyring is empty: $distribution_keyring"

    ensure_package_source_directory /usr/share/postgresql-common
    ensure_package_source_directory /usr/share/postgresql-common/pgdg
    ensure_package_source_directory /etc/apt/sources.list.d
    key_download="$TEMP_ROOT/apt.postgresql.org.asc"
    download_fixed_openpgp_key \
        "$PGDG_APT_KEY_URL" "$PGDG_APT_KEY_SHA256" "$PGDG_APT_KEY_FINGERPRINT" "$key_download"
    install_managed_package_source "$key_download" "$PGDG_APT_KEY_PATH" 644

    source_candidate="$TEMP_ROOT/probe-panel-runtime.list"
    : > "$source_candidate"
    case "$PLATFORM_APT_BASE_MODE" in
        debian-archive)
            printf 'deb [arch=%s signed-by=%s check-valid-until=no] https://archive.debian.org/debian %s main\n' \
                "$deb_arch" "$distribution_keyring" "$PLATFORM_APT_CODENAME" >> "$source_candidate"
            printf 'deb [arch=%s signed-by=%s check-valid-until=no] https://archive.debian.org/debian-security %s/updates main\n' \
                "$deb_arch" "$distribution_keyring" "$PLATFORM_APT_CODENAME" >> "$source_candidate"
            pgdg_validity_option=' check-valid-until=no'
            ;;
        debian-live)
            printf 'deb [arch=%s signed-by=%s] https://deb.debian.org/debian %s main\n' \
                "$deb_arch" "$distribution_keyring" "$PLATFORM_APT_CODENAME" >> "$source_candidate"
            printf 'deb [arch=%s signed-by=%s] https://deb.debian.org/debian %s-updates main\n' \
                "$deb_arch" "$distribution_keyring" "$PLATFORM_APT_CODENAME" >> "$source_candidate"
            printf 'deb [arch=%s signed-by=%s] https://security.debian.org/debian-security %s-security main\n' \
                "$deb_arch" "$distribution_keyring" "$PLATFORM_APT_CODENAME" >> "$source_candidate"
            ;;
        ubuntu-live)
            local ubuntu_archive ubuntu_security
            if [[ "$deb_arch" == arm64 ]]; then
                ubuntu_archive='https://ports.ubuntu.com/ubuntu-ports'
                ubuntu_security="$ubuntu_archive"
            else
                ubuntu_archive='https://archive.ubuntu.com/ubuntu'
                ubuntu_security='https://security.ubuntu.com/ubuntu'
            fi
            printf 'deb [arch=%s signed-by=%s] %s %s main universe\n' \
                "$deb_arch" "$distribution_keyring" "$ubuntu_archive" "$PLATFORM_APT_CODENAME" >> "$source_candidate"
            printf 'deb [arch=%s signed-by=%s] %s %s-updates main universe\n' \
                "$deb_arch" "$distribution_keyring" "$ubuntu_archive" "$PLATFORM_APT_CODENAME" >> "$source_candidate"
            printf 'deb [arch=%s signed-by=%s] %s %s-security main universe\n' \
                "$deb_arch" "$distribution_keyring" "$ubuntu_security" "$PLATFORM_APT_CODENAME" >> "$source_candidate"
            [[ "$PLATFORM_PGDG_APT_BASE_URL" != https://apt-archive.postgresql.org/* ]] ||
                pgdg_validity_option=' check-valid-until=no'
            ;;
    esac
    printf 'deb [arch=%s signed-by=%s%s] %s %s-pgdg main\n' \
        "$deb_arch" "$PGDG_APT_KEY_PATH" "$pgdg_validity_option" \
        "$PLATFORM_PGDG_APT_BASE_URL" "$PLATFORM_APT_CODENAME" >> "$source_candidate"
    chmod 0644 "$source_candidate"
    install_managed_package_source "$source_candidate" "$PROBE_APT_SOURCE_PATH" 644
}

deb_family_apt_repository_options() {
    printf '%s\n' \
        "Dir::Etc::sourcelist=$PROBE_APT_SOURCE_PATH" \
        'Dir::Etc::sourceparts=-' \
        'Acquire::AllowInsecureRepositories=false' \
        'Acquire::AllowDowngradeToInsecureRepositories=false' \
        'APT::Get::AllowUnauthenticated=false'
}

deb_family_platform_install_packages() {
    local option candidate_version
    local -a apt_options=()
    export DEBIAN_FRONTEND=noninteractive
    [[ -f "$PROBE_APT_SOURCE_PATH" && ! -L "$PROBE_APT_SOURCE_PATH" ]] ||
        die 'the isolated Probe Panel APT source is unavailable'
    while IFS= read -r option; do
        apt_options+=(-o "$option")
    done < <(deb_family_apt_repository_options)
    apt-get "${apt_options[@]}" update
    candidate_version="$(apt-cache "${apt_options[@]}" policy "$PLATFORM_POSTGRES_SERVER_PACKAGE" |
        awk '$1 == "Candidate:" { print $2; exit }')"
    [[ "$candidate_version" == 14.* ]] ||
        die "the isolated PGDG source did not offer PostgreSQL 14 (candidate ${candidate_version:-missing})"
    PACKAGE_SOURCE_CONSUMED=1
    apt-get "${apt_options[@]}" install -y --no-install-recommends "$@"
}

deb_family_platform_initialize_postgresql() {
    local preexisting="$1"
    [[ "$preexisting" == 0 || "$preexisting" == 1 ]] ||
        die 'the PostgreSQL preexistence state is invalid'
}

deb_family_platform_create_service_account() {
    require_command addgroup
    require_command adduser
    addgroup --system probe-api
    adduser --system --ingroup probe-api --no-create-home --home /nonexistent \
        --shell /usr/sbin/nologin probe-api
}

deb_family_platform_validate_nologin_shell() {
    [[ "$1" == /usr/sbin/nologin ]]
}

deb_family_platform_disable_default_nginx_site() {
    local default_site=/etc/nginx/sites-enabled/default
    if [[ -e "$default_site" || -L "$default_site" ]]; then
        [[ -L "$default_site" && "$(readlink -f -- "$default_site")" == /etc/nginx/sites-available/default ]] ||
            die "$default_site is not the deb-family stock default-site symlink; refusing to alter it"
        unlink -- "$default_site"
        log 'disabled the stock deb-family Nginx default site created by this installation'
    fi
}

assert_platform_postgresql_clients() {
    platform_adapter_call assert_postgresql_clients
}

assert_supported_systemd_version() {
    local version_output version_line implementation major
    version_output="$(systemctl --version 2>/dev/null)" ||
        die 'could not determine the running systemd version'
    version_line="${version_output%%$'\n'*}"
    read -r implementation major _ <<< "$version_line"
    [[ "$implementation" == systemd && "$major" =~ ^[0-9]+$ && ${#major} -le 6 ]] ||
        die "systemctl returned an invalid systemd version: ${version_line:-empty}"
    [[ "$PLATFORM_SYSTEMD_MIN_VERSION" =~ ^[0-9]+$ ]] ||
        die 'the selected platform systemd contract is unavailable'
    (( 10#$major >= 10#$PLATFORM_SYSTEMD_MIN_VERSION )) ||
        die "platform $PLATFORM_ID requires systemd $PLATFORM_SYSTEMD_MIN_VERSION or newer (found $major)"
}

preflight_systemd_host() {
    local pid_one lock_mode
    [[ "$PLATFORM_CONTRACT" == "$MANAGEMENT_RUNTIME_ABI" && "$PLATFORM_ID" == *-systemd ]] ||
        die 'an accepted candidate Linux/systemd platform must be selected before host preflight'
    [[ -d /run/systemd/system && ! -L /run/systemd/system ]] ||
        die 'systemd must be running before Probe Panel can be installed'
    pid_one="$(ps -p 1 -o comm= 2>/dev/null || :)"
    pid_one="${pid_one//[[:space:]]/}"
    [[ "$pid_one" == systemd ]] ||
        die "PID 1 must be systemd (found ${pid_one:-unknown})"
    assert_supported_systemd_version
    [[ -d /var/tmp && ! -L /var/tmp ]] || die '/var/tmp must be a real directory'
    [[ "$(stat -c '%u:%g' /var/tmp)" == 0:0 ]] || die '/var/tmp must be owned by root:root'
    lock_mode="$(stat -c '%a' /var/tmp)"
    [[ "$lock_mode" == 1777 ]] || die '/var/tmp must be root:root mode 1777'
}

preflight_platform_security() {
    platform_adapter_call preflight_security
}

validate_existing_nginx_configuration() {
    local nginx_binary="$1" nginx_config="$2" nginx_include_directory="$3"
    local config_dump config_size expected_include
    [[ -n "$TEMP_ROOT" && "$TEMP_ROOT" == /var/tmp/probe-panel-bootstrap.* &&
       -d "$TEMP_ROOT" && ! -L "$TEMP_ROOT" ]] ||
        die 'a private preflight workspace is required for Nginx validation'
    assert_secure_preexisting_path "$nginx_config" file
    assert_secure_preexisting_path "$nginx_include_directory" directory
    config_dump="$TEMP_ROOT/nginx-config.dump"
    expected_include="$nginx_include_directory/*.conf;"
    if ! (ulimit -f 2048; timeout --signal=KILL 15s \
        "$nginx_binary" -t -c "$nginx_config" >"$config_dump" 2>&1); then
        rm -f -- "$config_dump"
        die 'the existing native deb-family Nginx configuration is invalid; no host changes were made'
    fi
    if ! (ulimit -f 2048; timeout --signal=KILL 15s \
        "$nginx_binary" -T -c "$nginx_config" >"$config_dump" 2>&1); then
        rm -f -- "$config_dump"
        die 'the existing native deb-family Nginx configuration could not be safely inspected; no host changes were made'
    fi
    [[ -f "$config_dump" && ! -L "$config_dump" ]] ||
        die 'the Nginx preflight output is not a real regular file'
    config_size="$(stat -c '%s' "$config_dump")"
    if [[ ! "$config_size" =~ ^[0-9]+$ || "$config_size" -gt 1048576 ]]; then
        rm -f -- "$config_dump"
        die 'the existing Nginx configuration dump exceeds the 1 MiB preflight limit'
    fi
    if ! awk -v expected="$expected_include" '
        $1 == "include" && $2 == expected && NF == 2 { found=1 }
        END { exit !found }
    ' "$config_dump"; then
        rm -f -- "$config_dump"
        die 'the active deb-family Nginx configuration must include /etc/nginx/conf.d/*.conf'
    fi
    rm -f -- "$config_dump"
}

preflight_existing_runtimes() {
    local path nginx_signal=0 postgres_signal=0 nginx_binary

    NGINX_PREEXISTED=0
    POSTGRESQL_PREEXISTED=0

    for path in /usr/local/openresty /www/server/nginx /www/server/openresty /opt/1panel; do
        if [[ -e "$path" || -L "$path" ]]; then
            die "an OpenResty or 1Panel runtime was found at $path; migrate it away or use a clean candidate platform host before installing Probe Panel"
        fi
    done
    if command -v openresty >/dev/null 2>&1 || command -v 1pctl >/dev/null 2>&1 ||
       unit_is_installed openresty.service || unit_is_installed 1panel.service; then
            die 'candidate platform prerequisites exclude OpenResty and 1Panel-managed web stacks; use the native deb-family Nginx service'
    fi

    if command -v nginx >/dev/null 2>&1 || unit_is_installed nginx.service ||
       [[ -e /etc/nginx || -L /etc/nginx ]] ||
       [[ -e /etc/systemd/system/nginx.service || -L /etc/systemd/system/nginx.service ]] ||
       [[ -e /etc/systemd/system/nginx.service.d || -L /etc/systemd/system/nginx.service.d ]]; then
        nginx_signal=1
    fi
    if [[ "$nginx_signal" -eq 1 ]]; then
        command -v nginx >/dev/null 2>&1 ||
            die 'a partial Nginx installation was found without an nginx binary'
        nginx_binary="$(readlink -f -- "$(command -v nginx)")"
        [[ "$nginx_binary" == /usr/sbin/nginx ]] ||
            die "candidate platform prerequisites require the deb-family /usr/sbin/nginx layout (found $nginx_binary)"
        [[ -n "$PLATFORM_NGINX_BINARY_PACKAGE" ]] || die 'the platform Nginx package contract is unavailable'
        assert_platform_packaged_file /usr/sbin/nginx "$PLATFORM_NGINX_BINARY_PACKAGE"
        assert_platform_native_unit nginx.service nginx.service
        validate_existing_nginx_configuration \
            /usr/sbin/nginx /etc/nginx/nginx.conf /etc/nginx/conf.d
        NGINX_PREEXISTED=1
    fi
    if [[ -e "$PLATFORM_PSQL" || -L "$PLATFORM_PSQL" || -e "$PLATFORM_PG_ISREADY" || -L "$PLATFORM_PG_ISREADY" ]] ||
       unit_is_installed "$PLATFORM_POSTGRES_SERVICE" || [[ -e /etc/postgresql || -L /etc/postgresql ]] ||
       [[ -e /var/lib/postgresql || -L /var/lib/postgresql ]] ||
       [[ -e /var/lib/pgsql || -L /var/lib/pgsql ]] ||
       [[ -e "/etc/systemd/system/$PLATFORM_POSTGRES_SERVICE" || -L "/etc/systemd/system/$PLATFORM_POSTGRES_SERVICE" ]] ||
       [[ -e "/etc/systemd/system/$PLATFORM_POSTGRES_SERVICE.d" || -L "/etc/systemd/system/$PLATFORM_POSTGRES_SERVICE.d" ]] ||
       getent passwd postgres >/dev/null 2>&1; then
        postgres_signal=1
    elif ss -H -lnt | awk '$4 ~ /:5432$/ { found=1 } END { exit !found }'; then
        postgres_signal=1
    fi
    if [[ "$postgres_signal" -eq 1 ]]; then
        if [[ ! -x "$PLATFORM_PSQL" || ! -x "$PLATFORM_PG_ISREADY" ]]; then
            die 'a partial or unrecognized PostgreSQL installation was found; review it before installing Probe Panel'
        fi
        assert_platform_postgresql_clients
        assert_platform_native_unit "$PLATFORM_POSTGRES_SERVICE" "$PLATFORM_POSTGRES_UNIT"
        getent passwd postgres >/dev/null 2>&1 || die 'the platform postgres service account is unavailable'
        [[ "$POSTGRESQL_STATE_CAPTURED" -eq 1 ]] || die 'PostgreSQL start state was not captured'
        if [[ "$POSTGRESQL_WAS_ACTIVE" -ne 1 ]] ||
           ! systemctl is-active --quiet "$PLATFORM_POSTGRES_SERVICE"; then
            die "an existing inactive PostgreSQL service will not be started blindly; verify that listen_addresses and TCP 5432 are loopback-only, start $PLATFORM_POSTGRES_SERVICE yourself, then retry"
        fi
        assert_local_postgresql management
        POSTGRESQL_PREEXISTED=1
    fi
}

install_runtime_dependencies() {
    log 'installing first-run runtime dependencies after release verification'
    local profile="$1"
    [[ "$profile" == management ]] || die 'runtime dependency installation is management-only'
    local -a runtime_packages=()
    [[ "$POSTGRESQL_STATE_CAPTURED" -eq 1 ]] || die 'PostgreSQL start state was not captured'
    platform_adapter_call prepare_package_sources
    mapfile -t runtime_packages < <(platform_adapter_call runtime_packages)
    (( ${#runtime_packages[@]} > 0 )) ||
        die 'the selected platform adapter returned no runtime packages'
    if [[ "$POSTGRESQL_PREEXISTED" -eq 0 ]]; then
        [[ -n "$PLATFORM_POSTGRES_SERVER_PACKAGE" && -n "$PLATFORM_POSTGRES_CLIENT_PACKAGE" ]] ||
            die 'the selected platform PostgreSQL package contract is unavailable'
        runtime_packages+=("$PLATFORM_POSTGRES_SERVER_PACKAGE" "$PLATFORM_POSTGRES_CLIENT_PACKAGE")
    fi
    if [[ "$NGINX_PREEXISTED" -eq 0 ]]; then
        # Prevent package scripts from starting and enabling an unreviewed stock
        # default site. This temporary mask is removed immediately after install.
        NGINX_ABSENT_AT_START=1
        NGINX_MASKED_BY_INSTALLER=1
        systemctl mask nginx.service >/dev/null
        runtime_packages+=("$PLATFORM_NGINX_INSTALL_PACKAGE")
    fi
    platform_adapter_call install_packages "${runtime_packages[@]}"
    for dependency in curl python3 sha256sum nginx certbot ss ip runuser setpriv readlink; do
        require_command "$dependency"
    done
    assert_platform_postgresql_clients

    platform_adapter_call initialize_postgresql "$POSTGRESQL_PREEXISTED"

    local nginx_binary nginx_enablement
    if [[ "$NGINX_ABSENT_AT_START" -eq 1 ]]; then
        # A systemd mask makes FragmentPath resolve to /dev/null. Remove only
        # the mask created above, then prove the newly installed stock service
        # is stopped and disabled before inspecting its real native unit.
        systemctl unmask nginx.service >/dev/null
        NGINX_MASKED_BY_INSTALLER=0
        systemctl daemon-reload
        systemctl stop nginx.service
        systemctl disable nginx.service >/dev/null
        systemctl reset-failed nginx.service >/dev/null 2>&1 || :
        if systemctl is-active --quiet nginx.service; then
            die 'newly installed Nginx must remain stopped during management setup'
        fi
        nginx_enablement="$(systemctl is-enabled nginx.service 2>/dev/null || :)"
        [[ "$nginx_enablement" == disabled ]] ||
            die "newly installed Nginx must remain disabled during management setup (found ${nginx_enablement:-unknown})"
    fi
    nginx_binary="$(readlink -f -- "$(command -v nginx)")"
    [[ "$nginx_binary" == /usr/sbin/nginx ]] ||
        die "candidate platform prerequisites require the platform /usr/sbin/nginx runtime (found $nginx_binary)"
    [[ -n "$PLATFORM_NGINX_BINARY_PACKAGE" ]] || die 'the platform Nginx package contract is unavailable'
    assert_platform_packaged_file /usr/sbin/nginx "$PLATFORM_NGINX_BINARY_PACKAGE"
    assert_platform_native_unit nginx.service nginx.service
    assert_platform_native_unit "$PLATFORM_POSTGRES_SERVICE" "$PLATFORM_POSTGRES_UNIT"
    getent passwd postgres >/dev/null 2>&1 || die 'the platform postgres service account is unavailable'
}

assert_local_postgresql() {
    local profile="${1:-$PANEL_PROFILE}" listen_addresses address server_version
    [[ "$profile" == management ]] || die 'PostgreSQL validation is management-only'
    local -a postgres_addresses=()
    "$PLATFORM_PG_ISREADY" --host 127.0.0.1 --port 5432 --username postgres --dbname postgres >/dev/null ||
        die 'PostgreSQL must accept local TCP connections on 127.0.0.1:5432 before setup starts'
    listen_addresses="$(runuser -u postgres -- /usr/bin/env -i PATH=/usr/bin:/bin \
        "$PLATFORM_PSQL" --no-psqlrc --host="$PLATFORM_PG_SOCKET_DIR" --port=5432 \
        --username=postgres --dbname=postgres -Atqc 'SHOW listen_addresses')" ||
        die 'could not query the local PostgreSQL listener configuration'
    server_version="$(runuser -u postgres -- /usr/bin/env -i PATH=/usr/bin:/bin \
        "$PLATFORM_PSQL" --no-psqlrc --host="$PLATFORM_PG_SOCKET_DIR" --port=5432 \
        --username=postgres --dbname=postgres -Atqc 'SHOW server_version_num')" ||
        die 'could not query the local PostgreSQL server version'
    server_version="${server_version//[[:space:]]/}"
    [[ "$server_version" =~ ^[0-9]+$ && ${#server_version} -le 9 ]] ||
        die 'PostgreSQL returned an invalid server_version_num'
    (( 10#$server_version >= 140000 && 10#$server_version < 150000 )) ||
        die "Probe Panel requires PostgreSQL 14.x exactly (found server_version_num=$server_version)"
    IFS=',' read -r -a postgres_addresses <<< "$listen_addresses"
    for address in "${postgres_addresses[@]}"; do
        address="${address//[[:space:]]/}"
        case "$address" in
            ''|localhost|127.0.0.1|::1) ;;
            *) die "PostgreSQL listen_addresses is not local-only: $listen_addresses" ;;
        esac
    done

    if ss -H -lnt | awk '
        $4 ~ /:5432$/ && $4 != "127.0.0.1:5432" && $4 != "[::1]:5432" { unsafe=1 }
        END { exit !unsafe }
    '; then
        die 'PostgreSQL TCP 5432 is exposed beyond loopback; management installation refused without changing the service'
    fi
}

prepare_runtime_services() {
    local profile="$1" nginx_enablement
    [[ "$profile" == management ]] || die 'runtime service preparation is management-only'
    if [[ "$NGINX_PREEXISTED" -eq 1 ]]; then
        log 'preserving the existing Nginx service, enablement, and site configuration'
    else
        log 'preparing newly installed Nginx without exposing its stock default site'
        [[ "$NGINX_ABSENT_AT_START" -eq 1 && "$NGINX_MASKED_BY_INSTALLER" -eq 0 ]] ||
            die 'newly installed Nginx did not complete its safe unmask transition'
        systemctl stop nginx.service
        systemctl disable nginx.service >/dev/null
        systemctl reset-failed nginx.service >/dev/null 2>&1 || :
        if systemctl is-active --quiet nginx.service; then
            die 'newly installed Nginx became active before management finalization'
        fi
        nginx_enablement="$(systemctl is-enabled nginx.service 2>/dev/null || :)"
        [[ "$nginx_enablement" == disabled ]] ||
            die "newly installed Nginx became enabled before management finalization (found ${nginx_enablement:-unknown})"
        platform_adapter_call disable_default_nginx_site
    fi
    nginx -t >/dev/null || die 'the existing Nginx configuration is invalid; refusing to add Probe Panel'
    if [[ "$POSTGRESQL_PREEXISTED" -eq 1 ]]; then
        log 'validating the already-active PostgreSQL service for the setup wizard'
        systemctl is-active --quiet "$PLATFORM_POSTGRES_SERVICE" ||
            die "the existing PostgreSQL service became inactive; verify its loopback-only listener, start $PLATFORM_POSTGRES_SERVICE yourself, then retry"
    else
        log 'starting the newly installed PostgreSQL service for the setup wizard'
        systemctl start "$PLATFORM_POSTGRES_SERVICE"
    fi
    systemctl is-active --quiet "$PLATFORM_POSTGRES_SERVICE" || die 'PostgreSQL did not start'
    assert_local_postgresql management
}

login_defs_number() {
    local key="$1" fallback="${2-}" value
    [[ -z "$fallback" || "$fallback" =~ ^[0-9]+$ ]] ||
        die "the internal default for $key must be numeric"
    [[ -f /etc/login.defs && ! -L /etc/login.defs ]] ||
        die '/etc/login.defs must be a regular file, not a symbolic link'
    [[ "$(stat -c '%u:%g' /etc/login.defs)" == 0:0 ]] ||
        die '/etc/login.defs must be owned by root:root'
    local mode
    mode="$(stat -c '%a' /etc/login.defs)"
    [[ "$mode" =~ ^[0-7]{3,4}$ ]] || die '/etc/login.defs has an invalid mode'
    (( (8#$mode & 0022) == 0 )) ||
        die '/etc/login.defs must not be writable by group or other users'

    value="$(awk -v wanted="$key" -v fallback="$fallback" '
        /^[ \t]*#/ { next }
        $1 == wanted {
            count++
            if (NF != 2 || $2 !~ /^[0-9]+$/) {
                invalid = 1
                next
            }
            value = $2
        }
        END {
            if (invalid || count > 1) exit 1
            if (count == 1) {
                print value
                exit
            }
            if (fallback != "") {
                print fallback
                exit
            }
            exit 1
        }
    ' /etc/login.defs)" || die "/etc/login.defs must define at most one valid numeric $key"
    printf '%s\n' "$value"
}

assert_probe_api_service_account() {
    require_command awk
    require_command getent
    require_command id
    require_command stat

    getent passwd root >/dev/null 2>&1 || die 'the system account database is unavailable'
    getent group root >/dev/null 2>&1 || die 'the system group database is unavailable'

    local passwd_name_count group_name_count
    passwd_name_count="$(getent passwd | awk -F: '$1 == "probe-api" { count++ } END { print count + 0 }')" ||
        die 'could not enumerate the system account database'
    group_name_count="$(getent group | awk -F: '$1 == "probe-api" { count++ } END { print count + 0 }')" ||
        die 'could not enumerate the system group database'
    [[ "$passwd_name_count" == 1 && "$group_name_count" == 1 ]] ||
        die 'probe-api must have exactly one passwd record and one same-name group record'

    local -a passwd_records group_records uid_records gid_records group_ids group_members
    mapfile -t passwd_records < <(getent passwd probe-api || :)
    mapfile -t group_records < <(getent group probe-api || :)
    (( ${#passwd_records[@]} == 1 )) ||
        die 'probe-api must resolve to exactly one service account'
    (( ${#group_records[@]} == 1 )) ||
        die 'probe-api must resolve to exactly one same-name primary group'

    local account_name account_uid account_gid account_home account_shell
    local group_name group_gid group_members_field
    [[ "${passwd_records[0]}" =~ ^([^:]*:){6}[^:]*$ ]] ||
        die 'the probe-api passwd record is malformed'
    IFS=: read -r account_name _ account_uid account_gid _ account_home account_shell <<< "${passwd_records[0]}"
    [[ "$account_name" == probe-api && "$account_uid" =~ ^[0-9]+$ && "$account_gid" =~ ^[0-9]+$ ]] ||
        die 'the probe-api passwd record is malformed'
    [[ "$account_home" == /nonexistent ]] ||
        die 'probe-api must use /nonexistent as its home directory'
    platform_adapter_call validate_nologin_shell "$account_shell" ||
        die 'probe-api must use the platform nologin shell as its shell'

    local sys_uid_max_raw uid_min_raw sys_uid_max uid_min numeric_uid numeric_gid
    uid_min_raw="$(login_defs_number UID_MIN)"
    (( ${#uid_min_raw} <= 10 && ${#account_uid} <= 10 && ${#account_gid} <= 10 )) ||
        die 'probe-api or login.defs contains an out-of-range numeric identifier'
    uid_min=$((10#$uid_min_raw))
    numeric_uid=$((10#$account_uid))
    numeric_gid=$((10#$account_gid))
    (( uid_min >= 2 && uid_min <= 4294967294 && numeric_uid <= 4294967294 && numeric_gid <= 4294967294 )) ||
        die 'probe-api or login.defs contains an out-of-range numeric identifier'
    # login.defs(5) defines the omitted SYS_UID_MAX default as UID_MIN-1.
    # Reviewed distribution stock files rely on that default, so preserve it while still
    # rejecting duplicate, malformed, or explicitly unsafe overrides above.
    sys_uid_max_raw="$(login_defs_number SYS_UID_MAX "$((uid_min - 1))")"
    (( ${#sys_uid_max_raw} <= 10 )) ||
        die 'probe-api or login.defs contains an out-of-range numeric identifier'
    sys_uid_max=$((10#$sys_uid_max_raw))
    (( sys_uid_max <= 4294967294 )) ||
        die 'probe-api or login.defs contains an out-of-range numeric identifier'
    (( sys_uid_max >= 1 && uid_min > sys_uid_max )) ||
        die '/etc/login.defs SYS_UID_MAX must be below UID_MIN'
    (( numeric_uid >= 1 && numeric_uid <= sys_uid_max && numeric_uid < uid_min )) ||
        die 'probe-api UID is outside the platform system-account range'

    [[ "${group_records[0]}" =~ ^([^:]*:){3}[^:]*$ ]] ||
        die 'the probe-api group record is malformed'
    IFS=: read -r group_name _ group_gid group_members_field <<< "${group_records[0]}"
    [[ "$group_name" == probe-api && "$group_gid" =~ ^[0-9]+$ ]] ||
        die 'the probe-api group record is malformed'
    (( 10#$group_gid == numeric_gid )) ||
        die 'probe-api must use its same-name group as the primary group'

    mapfile -t uid_records < <(getent passwd "$numeric_uid" || :)
    if (( ${#uid_records[@]} != 1 )) || [[ "${uid_records[0]}" != "${passwd_records[0]}" ]]; then
        die 'probe-api must have a unique UID'
    fi
    mapfile -t gid_records < <(getent group "$numeric_gid" || :)
    if (( ${#gid_records[@]} != 1 )) || [[ "${gid_records[0]}" != "${group_records[0]}" ]]; then
        die 'the probe-api primary GID must belong to exactly one group'
    fi
    [[ "$(id -g probe-api)" == "$numeric_gid" && "$(id -gn probe-api)" == probe-api ]] ||
        die 'probe-api must use its unique same-name primary group'

    local duplicate_uid_owner duplicate_gid_group group_id_output other_primary_user member seen_self=0
    duplicate_uid_owner="$(getent passwd | awk -F: -v wanted_uid="$numeric_uid" '
        $3 == wanted_uid && $1 != "probe-api" && other == "" { other = $1 }
        END { print other }
    ')" || die 'could not enumerate accounts while proving the probe-api UID is unique'
    [[ -z "$duplicate_uid_owner" ]] ||
        die "the probe-api UID is also used by another account: $duplicate_uid_owner"
    duplicate_gid_group="$(getent group | awk -F: -v wanted_gid="$numeric_gid" '
        $3 == wanted_gid && $1 != "probe-api" && other == "" { other = $1 }
        END { print other }
    ')" || die 'could not enumerate groups while proving the probe-api GID is unique'
    [[ -z "$duplicate_gid_group" ]] ||
        die "the probe-api primary GID is also used by another group: $duplicate_gid_group"

    group_id_output="$(id -G probe-api)" || die 'could not enumerate probe-api group membership'
    read -r -a group_ids <<< "$group_id_output"
    if (( ${#group_ids[@]} != 1 )) || [[ "${group_ids[0]}" != "$numeric_gid" ]]; then
        die 'probe-api must not have supplementary groups'
    fi

    if [[ -n "$group_members_field" ]]; then
        IFS=, read -r -a group_members <<< "$group_members_field"
        for member in "${group_members[@]}"; do
            [[ "$member" == probe-api && "$seen_self" -eq 0 ]] ||
                die 'the probe-api group must not contain other or duplicate explicit members'
            seen_self=1
        done
    fi
    other_primary_user="$(getent passwd | awk -F: -v wanted_gid="$numeric_gid" '
        $4 == wanted_gid && $1 != "probe-api" && other == "" { other = $1 }
        END { print other }
    ')" || die 'could not enumerate accounts that use the probe-api primary group'
    [[ -z "$other_primary_user" ]] ||
        die "the probe-api primary group is also used by another account: $other_primary_user"
}

prepare_probe_api_account() {
    require_command getent

    local passwd_record group_record
    passwd_record="$(getent passwd probe-api || :)"
    group_record="$(getent group probe-api || :)"
    if [[ -z "$passwd_record" && -z "$group_record" ]]; then
        platform_adapter_call create_service_account
    elif [[ -z "$passwd_record" || -z "$group_record" ]]; then
        die 'a partial probe-api service account or group already exists; refusing to repair it'
    fi
    assert_probe_api_service_account
}

detect_server_ip() {
    local candidate=''
    candidate="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '
        {
            for (i = 1; i <= NF; i++) {
                if ($i == "src" && i < NF) { print $(i + 1); exit }
            }
        }
    ')"
    if [[ -z "$candidate" ]]; then
        candidate="$(ip -6 route get 2001:4860:4860::8888 2>/dev/null | awk '
            {
                for (i = 1; i <= NF; i++) {
                    if ($i == "src" && i < NF) { print $(i + 1); exit }
                }
            }
        ')"
    fi
    [[ -n "$candidate" ]] ||
        die 'could not detect the server IP from the default route'

    SETUP_SERVER_IP="$(python3 - "$candidate" <<'PY'
import ipaddress
import sys

try:
    address = ipaddress.ip_address(sys.argv[1])
except ValueError as error:
    raise SystemExit("invalid detected server IP: {}".format(error))
if (
    address.is_loopback
    or address.is_unspecified
    or address.is_multicast
    or address.is_link_local
    or getattr(address, "ipv4_mapped", None) is not None
):
    raise SystemExit("detected server IP is not usable for panel ingress")
print(address.compressed)
PY
)" || die 'the detected default-route IP is not a usable IPv4 or IPv6 address'
    [[ -n "$SETUP_SERVER_IP" ]] || die 'server IP detection returned an empty address'
}

download_file() {
    local destination="$1" url="$2" maximum_seconds="$3"
    if curl_supports_release_tls; then
        curl -q --fail --silent --show-error --location \
            --proto '=https' --proto-redir '=https' --tlsv1.2 \
            --connect-timeout 15 --max-time "$maximum_seconds" \
            --output "$destination" "$url"
        return
    fi
    command -v wget >/dev/null 2>&1 || die 'HTTPS downloader is unavailable'
    wget --https-only --timeout=15 --tries=3 --max-redirect=5 \
        --output-document="$destination" "$url"
}

verify_release_archive() {
    local manifest="$1" archive="$2" asset_name="$3"
    local expected_hash actual_hash count
    count="$(awk -v target="$asset_name" '$2 == target { count++ } END { print count + 0 }' "$manifest")"
    [[ "$count" == 1 ]] || die "release SHA256SUMS must contain exactly one entry for $asset_name"
    expected_hash="$(awk -v target="$asset_name" '$2 == target { print $1 }' "$manifest")"
    [[ "$expected_hash" =~ ^[0-9a-f]{64}$ ]] || die 'release checksum is malformed'
    actual_hash="$(sha256sum "$archive")"
    actual_hash="${actual_hash%% *}"
    [[ "$actual_hash" == "$expected_hash" ]] || die 'release archive SHA256 verification failed'
}

safe_extract_archive() {
    local archive="$1" destination="$2"
    python3 - "$archive" "$destination" <<'PY'
import errno
import os
import stat
import sys
import tarfile

MAX_ARCHIVE_BYTES = 536870912
MAX_MEMBERS = 20000
MAX_FILE_BYTES = 536870912
MAX_EXPANDED_BYTES = 2147483648
COPY_CHUNK_BYTES = 65536


class ArchiveRejected(Exception):
    pass


def reject(message):
    raise ArchiveRejected(message)


def canonical_relative_path(name, is_directory, label):
    if not isinstance(name, str) or not name:
        reject("{0} is empty or is not text".format(label))
    if "\x00" in name or "\\" in name:
        reject("{0} contains a forbidden character: {1!r}".format(label, name))
    if any(ord(character) < 32 or ord(character) == 127 for character in name):
        reject("{0} contains a control character: {1!r}".format(label, name))
    if name.startswith("/"):
        reject("{0} must be relative: {1!r}".format(label, name))

    normalized = name
    if normalized.endswith("/"):
        if not is_directory or normalized.endswith("//"):
            reject("{0} is non-canonical: {1!r}".format(label, name))
        normalized = normalized[:-1]
    if not normalized:
        reject("{0} is empty after normalization".format(label))

    parts = normalized.split("/")
    if any(part in ("", ".", "..") for part in parts):
        reject("{0} is non-canonical or escapes its root: {1!r}".format(label, name))
    try:
        encoded_path = os.fsencode(normalized)
        encoded_parts = [os.fsencode(part) for part in parts]
    except (UnicodeEncodeError, TypeError):
        reject("{0} cannot be represented by the host filesystem".format(label))
    if len(encoded_path) > 4095 or any(len(part) > 255 for part in encoded_parts):
        reject("{0} exceeds the supported filesystem path length".format(label))
    return normalized, parts


def safe_file_mode(mode):
    # Preserve read/execute intent, but never archive-controlled special bits or
    # group/world write access. The owner must retain read/write access while
    # the verified bundle is staged and installed.
    return (mode & 0o555) | 0o600


def safe_directory_mode(mode):
    # Extraction directories remain owner-private until every member exists.
    # Their final mode may preserve only group/world read and search bits.
    return (mode & 0o055) | 0o700


SECURE_DESCRIPTOR_FLAGS = hasattr(os, "O_NOFOLLOW") and hasattr(os, "O_DIRECTORY")
OPEN_DIRECTORY_FLAGS = os.O_RDONLY
OPEN_FILE_FLAGS = os.O_WRONLY | os.O_CREAT | os.O_EXCL
if SECURE_DESCRIPTOR_FLAGS:
    OPEN_DIRECTORY_FLAGS |= os.O_DIRECTORY | os.O_NOFOLLOW
    OPEN_FILE_FLAGS |= os.O_NOFOLLOW
if hasattr(os, "O_CLOEXEC"):
    OPEN_DIRECTORY_FLAGS |= os.O_CLOEXEC
    OPEN_FILE_FLAGS |= os.O_CLOEXEC


def assert_safe_directory_fd(directory_fd, label):
    metadata = os.fstat(directory_fd)
    if not stat.S_ISDIR(metadata.st_mode):
        reject("{0} is not a directory".format(label))
    if metadata.st_uid != os.geteuid() or metadata.st_mode & 0o022:
        reject("{0} must be owned by the extractor and not group/world-writable".format(label))


def open_child_directory(parent_fd, component, create):
    try:
        child_fd = os.open(component, OPEN_DIRECTORY_FLAGS, dir_fd=parent_fd)
    except OSError as error:
        if not create or error.errno != errno.ENOENT:
            reject("unsafe archive parent component {0!r}: {1}".format(component, error))
        try:
            os.mkdir(component, 0o700, dir_fd=parent_fd)
        except OSError as mkdir_error:
            if mkdir_error.errno != errno.EEXIST:
                reject("could not create archive directory {0!r}: {1}".format(component, mkdir_error))
        try:
            child_fd = os.open(component, OPEN_DIRECTORY_FLAGS, dir_fd=parent_fd)
        except OSError as open_error:
            reject("unsafe archive directory {0!r}: {1}".format(component, open_error))
    try:
        assert_safe_directory_fd(child_fd, "archive directory {0!r}".format(component))
    except Exception:
        os.close(child_fd)
        raise
    return child_fd


def open_directory_path(root_fd, parts, create):
    current_fd = os.dup(root_fd)
    try:
        for component in parts:
            next_fd = open_child_directory(current_fd, component, create)
            os.close(current_fd)
            current_fd = next_fd
        return current_fd
    except Exception:
        os.close(current_fd)
        raise


def write_all(file_fd, data):
    offset = 0
    while offset < len(data):
        try:
            written = os.write(file_fd, data[offset:])
        except OSError as error:
            if error.errno == errno.EINTR:
                continue
            raise
        if written <= 0:
            reject("archive file write made no progress")
        offset += written


def validate_pax_paths(member, canonical_name, is_directory):
    pax_path = member.pax_headers.get("path")
    if pax_path is not None:
        pax_name, unused_parts = canonical_relative_path(
            pax_path, is_directory, "PAX path override"
        )
        if pax_name != canonical_name:
            reject("PAX path override does not match the effective archive path")
    pax_linkpath = member.pax_headers.get("linkpath")
    if pax_linkpath is not None:
        canonical_relative_path(pax_linkpath, False, "PAX linkpath override")
        if not (member.issym() or member.islnk()):
            reject("PAX linkpath override is forbidden on a non-link member")
    if any(key.startswith("GNU.sparse.") for key in member.pax_headers):
        reject("GNU sparse PAX members are forbidden in release archives")


def extract_regular_file(bundle, member, root_fd, parts):
    parent_fd = open_directory_path(root_fd, parts[:-1], True)
    file_fd = None
    source = None
    try:
        try:
            file_fd = os.open(parts[-1], OPEN_FILE_FLAGS, 0o600, dir_fd=parent_fd)
        except OSError as error:
            reject("archive file target already exists or is unsafe: {0!r}: {1}".format(
                member.name, error
            ))
        source = bundle.extractfile(member)
        if source is None:
            reject("regular archive member has no readable payload: {0!r}".format(member.name))
        remaining = member.size
        while remaining:
            chunk = source.read(min(COPY_CHUNK_BYTES, remaining))
            if not chunk:
                reject("archive member ended before its declared size: {0!r}".format(member.name))
            if len(chunk) > remaining:
                reject("archive member exceeded its declared size: {0!r}".format(member.name))
            write_all(file_fd, chunk)
            remaining -= len(chunk)
        if source.read(1):
            reject("archive member exceeded its declared size: {0!r}".format(member.name))
        os.fchmod(file_fd, safe_file_mode(member.mode))
    finally:
        if source is not None:
            source.close()
        if file_fd is not None:
            os.close(file_fd)
        os.close(parent_fd)


def extract_archive(archive_path, destination):
    if not SECURE_DESCRIPTOR_FLAGS:
        reject("the host Python runtime lacks secure descriptor-relative extraction flags")
    archive_flags = os.O_RDONLY | os.O_NOFOLLOW
    if hasattr(os, "O_CLOEXEC"):
        archive_flags |= os.O_CLOEXEC
    archive_fd = os.open(archive_path, archive_flags)
    archive_metadata = os.fstat(archive_fd)
    if not stat.S_ISREG(archive_metadata.st_mode):
        os.close(archive_fd)
        reject("release archive must be a regular file")
    if archive_metadata.st_size <= 0 or archive_metadata.st_size > MAX_ARCHIVE_BYTES:
        os.close(archive_fd)
        reject("release archive is empty or exceeds 512 MiB")

    root_fd = os.open(destination, OPEN_DIRECTORY_FLAGS)
    try:
        assert_safe_directory_fd(root_fd, "archive extraction root")
        seen = set()
        directory_modes = []
        member_count = 0
        expanded_bytes = 0
        with os.fdopen(archive_fd, "rb") as archive_stream:
            archive_fd = None
            with tarfile.open(fileobj=archive_stream, mode="r|gz") as bundle:
                for member in bundle:
                    member_count += 1
                    if member_count > MAX_MEMBERS:
                        reject("release archive has more than 20000 entries")

                    is_directory = member.isdir()
                    canonical_name, parts = canonical_relative_path(
                        member.name, is_directory, "archive path"
                    )
                    validate_pax_paths(member, canonical_name, is_directory)
                    if canonical_name in seen:
                        reject("duplicate archive path: {0!r}".format(member.name))
                    seen.add(canonical_name)

                    if member.issym() or member.islnk():
                        reject("links are forbidden in release archives: {0!r}".format(member.name))
                    if getattr(member, "sparse", None) is not None:
                        reject("sparse files are forbidden in release archives: {0!r}".format(member.name))
                    if not (member.isfile() or is_directory):
                        reject("devices, FIFOs, and other special files are forbidden: {0!r}".format(
                            member.name
                        ))
                    if not isinstance(member.size, int) or member.size < 0:
                        reject("archive member has an invalid size: {0!r}".format(member.name))
                    if is_directory:
                        if member.size != 0:
                            reject("archive directory has a non-zero payload: {0!r}".format(member.name))
                        directory_fd = open_directory_path(root_fd, parts, True)
                        os.close(directory_fd)
                        directory_modes.append((tuple(parts), safe_directory_mode(member.mode)))
                        continue
                    if member.size > MAX_FILE_BYTES:
                        reject("archive member exceeds the 512 MiB single-file limit: {0!r}".format(
                            member.name
                        ))
                    if expanded_bytes > MAX_EXPANDED_BYTES - member.size:
                        reject("release archive exceeds the 2 GiB expanded-size limit")
                    expanded_bytes += member.size
                    extract_regular_file(bundle, member, root_fd, parts)

        if member_count == 0:
            reject("release archive is empty")
        for parts, mode in sorted(directory_modes, key=lambda item: len(item[0]), reverse=True):
            directory_fd = open_directory_path(root_fd, parts, False)
            try:
                os.fchmod(directory_fd, mode)
            finally:
                os.close(directory_fd)
    finally:
        os.close(root_fd)
        if archive_fd is not None:
            os.close(archive_fd)


try:
    extract_archive(sys.argv[1], sys.argv[2])
except ArchiveRejected as error:
    sys.stderr.write("release archive extraction rejected: {0}\n".format(error))
    raise SystemExit(1)
except (OSError, EOFError, tarfile.TarError) as error:
    sys.stderr.write("release archive extraction failed safely: {0}\n".format(error))
    raise SystemExit(1)
except Exception as error:
    sys.stderr.write("release archive extraction failed safely: {0}\n".format(error))
    raise SystemExit(1)
PY
}

validate_release_platform_metadata() {
    local manifest="$1"
    local runtime_abi_count manifest_runtime_abi platform_ids_count manifest_platform_ids
    [[ -f "$manifest" && ! -L "$manifest" ]] || die 'release metadata is missing'
    runtime_abi_count="$(awk '/^runtime_abi=/ { count++ } END { print count + 0 }' "$manifest")"
    manifest_runtime_abi="$(sed -n 's/^runtime_abi=//p' "$manifest")"
    [[ "$runtime_abi_count" == 1 && "$manifest_runtime_abi" == "$MANAGEMENT_RUNTIME_ABI" ]] ||
        die "release metadata must declare runtime_abi=$MANAGEMENT_RUNTIME_ABI exactly once"
    [[ "$PLATFORM_CONTRACT" == "$manifest_runtime_abi" ]] ||
        die 'release runtime ABI does not match the selected platform contract'
    platform_ids_count="$(awk '/^platform_ids=/ { count++ } END { print count + 0 }' "$manifest")"
    manifest_platform_ids="$(sed -n 's/^platform_ids=//p' "$manifest")"
    [[ "$platform_ids_count" == 1 && "$manifest_platform_ids" == "$SUPPORTED_PLATFORM_IDS" ]] ||
        die "release metadata must declare platform_ids=$SUPPORTED_PLATFORM_IDS exactly once"
    case ",$manifest_platform_ids," in
        *",$PLATFORM_ID,"*) ;;
        *) die "release metadata does not include the selected platform ID: $PLATFORM_ID" ;;
    esac
}

management_deploy_helper_is_clean() {
    local helper_file="$1" api_hardening_line backup_hardening_line
    local legacy_hardening_count sanitized_helper scan_status=0
    api_hardening_line="        grep -Fxq 'ProtectSystem=full' \"\$unit_file\" || die \"legacy probe-api unit must protect the system\""
    backup_hardening_line="        grep -Fxq 'ProtectSystem=full' \"\$service_file\" ||"

    [[ -f "$helper_file" && ! -L "$helper_file" ]] || return 1
    grep -Fxq "$api_hardening_line" "$helper_file" || return 1
    grep -Fxq "$backup_hardening_line" "$helper_file" || return 1
    legacy_hardening_count="$(grep -Foc 'ProtectSystem=full' "$helper_file")" || return 1
    [[ "$legacy_hardening_count" == 2 ]] || return 1
    sanitized_helper="$(
        sed 's/ProtectSystem=full/ProtectSystem=reviewed-legacy-hardening/g' "$helper_file"
    )" || return 1
    grep -Eiq 'MANAGEMENT_BUNDLE_EXCLUDE|build_release_artifacts|deploy_release\(\)|npm[[:space:]]+run[[:space:]]+build|[.]/cmd/probe-agent|artifacts/(agent|web)|PROBE_(AGENT|WEB)_DIR|old_(agent|web)|probe-web|/srv/probe/(agent|web)|(^|[^[:alnum:]_])full([^[:alnum:]_]|$)' \
        <<< "$sanitized_helper" || scan_status=$?
    case "$scan_status" in
        0) return 1 ;;
        1) return 0 ;;
        *) return 1 ;;
    esac
}

validate_release_bundle() {
    local root="$1" architecture="$2" profile="${3:-$PANEL_PROFILE}"
    local manifest="$root/RELEASE-MANIFEST"
    local required manifest_profile_count manifest_profile
    [[ -f "$manifest" && ! -L "$manifest" ]] || die 'release metadata is missing'
    grep -Fxq 'format=probe-panel-release-v1' "$manifest" || die 'release metadata format is invalid'
    grep -Fxq "version=$PANEL_VERSION" "$manifest" || die 'release metadata version is invalid'
    grep -Fxq "architecture=linux-$architecture" "$manifest" || die 'release architecture does not match this server'
    grep -Fxq "super_my_ref=$SUPER_MY_REF" "$manifest" || die 'server source ref is not the verified release ref'
    validate_release_platform_metadata "$manifest"
    manifest_profile_count="$(awk '/^profile=/ { count++ } END { print count + 0 }' "$manifest")"
    manifest_profile="$(sed -n 's/^profile=//p' "$manifest")"
    [[ "$profile" == management ]] || die 'the v1.2 installer validates management bundles only'
    [[ "$manifest_profile_count" == 1 && "$manifest_profile" == management ]] ||
        die 'management release metadata must declare profile=management exactly once'
    [[ "$(grep -Ec '^(my_ref|my_agent_ref)=' "$manifest")" -eq 0 ]] ||
        die 'management release metadata must not reference visitor or Agent sources'

    for required in \
        BUNDLE-SHA256SUMS \
        artifacts/api/probe-api \
        setup/probe-setup \
        artifacts/admin/index.html \
        source/probe-api/config/probe-api.env.example \
        source/probe-api/config/probe-postgres-backup.env.example \
        source/probe-api/deploy/scripts/deploy-common.sh \
        source/probe-api/deploy/scripts/install-release.sh \
        source/probe-api/deploy/scripts/restore-management.sh \
        source/probe-api/deploy/scripts/validate-management.sh \
        source/probe-api/deploy/scripts/uninstall-management.sh \
        source/probe-api/deploy/scripts/backup-postgres.sh \
        source/probe-api/deploy/scripts/restore-postgres.sh \
        source/probe-api/deploy/setup/probe-panel-setup.service \
        source/probe-api/deploy/setup/probe-panel-setup.socket \
        source/probe-api/deploy/setup/probe-panel-setup-legacy.service \
        source/probe-api/deploy/setup/probe-panel-setup-legacy.socket \
        source/probe-api/deploy/setup/probe-panel-finalizer.path \
        source/probe-api/deploy/systemd/probe-api.service \
        source/probe-api/deploy/systemd/probe-api-legacy.service \
        source/probe-api/deploy/systemd/probe-postgres-backup.service \
        source/probe-api/deploy/systemd/probe-postgres-backup.timer \
        source/probe-api/deploy/systemd/probe-postgres-backup-legacy.service \
        source/probe-api/deploy/systemd/probe-postgres-backup-legacy.timer; do
        [[ -f "$root/$required" && ! -L "$root/$required" ]] ||
            die "release bundle is incomplete: $required"
    done
    [[ "$(grep -Fxc "readonly PG_DUMP_BINARY='@PROBE_PG_DUMP@'" \
        "$root/source/probe-api/deploy/scripts/backup-postgres.sh" ||
        :)" -eq 1 ]] ||
        die 'release PostgreSQL backup script has an invalid pg_dump render contract'
    [[ "$(grep -Fxc "readonly PSQL_BINARY='@PROBE_PSQL@'" \
        "$root/source/probe-api/deploy/scripts/restore-postgres.sh" ||
        :)" -eq 1 ]] ||
        die 'release PostgreSQL restore script has an invalid psql render contract'
    for required in backup-postgres.sh restore-postgres.sh; do
        [[ "$(grep -Fxc "readonly PG_RESTORE_BINARY='@PROBE_PG_RESTORE@'" \
            "$root/source/probe-api/deploy/scripts/$required" ||
            :)" -eq 1 ]] ||
            die "release PostgreSQL script has an invalid pg_restore render contract: $required"
    done
    local backup_tokens restore_tokens
    backup_tokens="$(grep -Eo '@PROBE_[A-Z0-9_]+@' \
        "$root/source/probe-api/deploy/scripts/backup-postgres.sh" | LC_ALL=C sort -u)"
    restore_tokens="$(grep -Eo '@PROBE_[A-Z0-9_]+@' \
        "$root/source/probe-api/deploy/scripts/restore-postgres.sh" | LC_ALL=C sort -u)"
    [[ "$backup_tokens" == $'@PROBE_PG_DUMP@\n@PROBE_PG_RESTORE@' ]] ||
        die 'release PostgreSQL backup script has an unexpected render-token set'
    [[ "$restore_tokens" == $'@PROBE_PG_RESTORE@\n@PROBE_PSQL@' ]] ||
        die 'release PostgreSQL restore script has an unexpected render-token set'
    for required in \
        source/probe-api/deploy/nginx/nginx-management.conf \
        source/probe-api/deploy/nginx/nginx-management-ip.conf \
        source/probe-api/deploy/nginx/nginx-management-legacy.conf \
        source/probe-api/deploy/nginx/nginx-management-ip-legacy.conf \
        source/probe-api/deploy/nginx/nginx-management-classic.conf \
        source/probe-api/deploy/nginx/nginx-management-ip-classic.conf \
        source/probe-api/deploy/setup/probe-panel-finalizer-management.service \
        source/probe-api/deploy/setup/probe-panel-finalizer-management-legacy.service; do
        [[ -f "$root/$required" && ! -L "$root/$required" ]] ||
            die "management release bundle is incomplete: $required"
    done
    [[ ! -e "$root/artifacts/web" && ! -L "$root/artifacts/web" ]] ||
        die 'management release bundle must not contain visitor frontend artifacts'
    [[ ! -e "$root/artifacts/agent" && ! -L "$root/artifacts/agent" ]] ||
        die 'management release bundle must not contain Agent artifacts'
    for required in \
        source/probe-api/deploy/nginx/nginx.conf \
        source/probe-api/deploy/nginx/nginx-ip.conf \
        source/probe-api/deploy/setup/probe-panel-finalizer.service \
        source/probe-api/deploy/scripts/build-release-bundles.sh \
        source/probe-api/deploy/scripts/install.sh \
        source/probe-api/deploy/scripts/upgrade.sh \
        source/probe-api/deploy/scripts/validate-production.sh; do
        [[ ! -e "$root/$required" && ! -L "$root/$required" ]] ||
            die "management release bundle contains a forbidden source/build asset: $required"
    done
    local expected_deploy_files actual_deploy_files
    expected_deploy_files="$(cat <<'EOF'
source/probe-api/deploy/nginx/nginx-management-classic.conf
source/probe-api/deploy/nginx/nginx-management-ip-classic.conf
source/probe-api/deploy/nginx/nginx-management-ip-legacy.conf
source/probe-api/deploy/nginx/nginx-management-ip.conf
source/probe-api/deploy/nginx/nginx-management-legacy.conf
source/probe-api/deploy/nginx/nginx-management.conf
source/probe-api/deploy/scripts/backup-postgres.sh
source/probe-api/deploy/scripts/deploy-common.sh
source/probe-api/deploy/scripts/install-release.sh
source/probe-api/deploy/scripts/restore-management.sh
source/probe-api/deploy/scripts/restore-postgres.sh
source/probe-api/deploy/scripts/uninstall-management.sh
source/probe-api/deploy/scripts/validate-management.sh
source/probe-api/deploy/setup/probe-panel-finalizer-management-legacy.service
source/probe-api/deploy/setup/probe-panel-finalizer-management.service
source/probe-api/deploy/setup/probe-panel-finalizer.path
source/probe-api/deploy/setup/probe-panel-setup-legacy.service
source/probe-api/deploy/setup/probe-panel-setup-legacy.socket
source/probe-api/deploy/setup/probe-panel-setup.env.example
source/probe-api/deploy/setup/probe-panel-setup.service
source/probe-api/deploy/setup/probe-panel-setup.socket
source/probe-api/deploy/systemd/probe-api-legacy.service
source/probe-api/deploy/systemd/probe-api.service
source/probe-api/deploy/systemd/probe-postgres-backup-legacy.service
source/probe-api/deploy/systemd/probe-postgres-backup-legacy.timer
source/probe-api/deploy/systemd/probe-postgres-backup.service
source/probe-api/deploy/systemd/probe-postgres-backup.timer
EOF
)"
    actual_deploy_files="$(
        cd "$root"
        find source/probe-api/deploy -type f -print | LC_ALL=C sort
    )"
    [[ "$actual_deploy_files" == "$expected_deploy_files" ]] ||
        die 'management release bundle deploy assets differ from the reviewed runtime allowlist'

    if ! management_deploy_helper_is_clean \
        "$root/source/probe-api/deploy/scripts/deploy-common.sh"; then
        die 'management deploy-common contains forbidden full, Agent, or visitor build logic'
    fi
    if grep -Eq -- '--disable-default-site|disable_default_nginx_site' \
        "$root/source/probe-api/deploy/scripts/install-release.sh" \
        "$root/source/probe-api/deploy/scripts/deploy-common.sh"; then
        die 'management release runtime must not mutate the deb-family default Nginx site'
    fi
    [[ -d "$root/artifacts/migrations" && ! -L "$root/artifacts/migrations" ]] ||
        die 'release migrations path is not a real directory'

    local expected_paths manifest_paths
    expected_paths="$(
        cd "$root"
        find artifacts setup source -type f -print | LC_ALL=C sort
    )"
    manifest_paths="$(awk '{ print $2 }' "$root/BUNDLE-SHA256SUMS" | LC_ALL=C sort)"
    [[ -n "$expected_paths" && "$manifest_paths" == "$expected_paths" ]] ||
        die 'BUNDLE-SHA256SUMS must cover every source, artifacts, and setup file exactly once'
    (
        cd "$root"
        sha256sum --check --strict BUNDLE-SHA256SUMS
    ) >/dev/null || die 'release bundle internal SHA256 verification failed'

    local forbidden_setup_control
    for forbidden_setup_control in panel-domain agent-domain '游客面板域名' 'Agent API 域名' '三个域名'; do
        if grep -R -I -F -q -- "$forbidden_setup_control" "$root/artifacts/admin"; then
            die "management administrator artifact contains a historical multi-product setup control: $forbidden_setup_control"
        fi
    done

    local setup_service_asset setup_socket_asset finalizer_asset
    setup_service_asset="$(selected_setup_asset_name probe-panel-setup.service)"
    setup_socket_asset="$(selected_setup_asset_name probe-panel-setup.socket)"
    finalizer_asset="$(selected_setup_asset_name probe-panel-finalizer-management.service)"
    local unit="$root/source/probe-api/deploy/setup/$setup_service_asset"
    local socket_unit="$root/source/probe-api/deploy/setup/$setup_socket_asset"
    grep -Fxq 'User=root' "$unit" || die 'setup service must use the root-owned setup state'
    grep -Fxq 'EnvironmentFile=/etc/probe-panel/setup.env' "$unit" || die 'setup service environment path changed'
    grep -Fxq 'ExecStart=/usr/local/lib/probe-panel/probe-setup serve' "$unit" || die 'setup service executable path changed'
    grep -Fxq 'CapabilityBoundingSet=' "$unit" || die 'HTTP setup service must have an empty capability set'
    grep -Fxq 'ReadWritePaths=/run/probe-panel-setup' "$unit" || die 'HTTP setup runtime request path changed'
    grep -Fxq 'RestrictAddressFamilies=AF_UNIX' "$unit" || die 'setup service must accept Unix sockets only'
    grep -Fxq 'PrivateNetwork=true' "$unit" || die 'setup service must have a private network namespace'
    [[ "$(grep -Fc 'SocketBindAllow=' "$unit")" -eq 0 ]] || die 'setup service must not bind any IP socket'
    if [[ "$PLATFORM_SYSTEMD_PROFILE" == modern ]]; then
        grep -Fxq 'ProtectSystem=strict' "$unit" || die 'modern setup service must strictly protect the system'
        grep -Fxq 'SocketBindDeny=any' "$unit" || die 'setup service must deny every other bind operation'
    else
        grep -Fxq 'ProtectSystem=full' "$unit" || die 'legacy setup service must protect the system'
        ! grep -Eq '^(SocketBindAllow|SocketBindDeny|ProtectProc|ProtectClock|ProtectKernelLogs|ProtectHostname|AmbientCapabilities)=' "$unit" ||
            die 'legacy setup service contains a directive newer than systemd 219'
    fi
    grep -Fxq 'ListenStream=/run/probe-panel-setup/setup.sock' "$socket_unit" || die 'setup Unix socket path changed'
    grep -Fxq 'SocketMode=0600' "$socket_unit" || die 'setup Unix socket must be root-private'
    grep -Fxq 'DirectoryMode=0700' "$socket_unit" || die 'setup Unix socket directory must be root-private'
    grep -Fxq 'RemoveOnStop=yes' "$socket_unit" || die 'setup Unix socket must be removed when stopped'

    local finalizer_unit="$root/source/probe-api/deploy/setup/$finalizer_asset"
    local finalizer_path="$root/source/probe-api/deploy/setup/probe-panel-finalizer.path"
    grep -Fxq 'ExecStart=/usr/local/lib/probe-panel/probe-setup finalize' "$finalizer_unit" ||
        die 'setup finalizer executable path changed'
    [[ "$(grep -Fc 'ExecStopPost=' "$finalizer_unit")" -eq 1 ]] ||
        die 'setup finalizer must have exactly one retry-aware post-stop action'
    grep -Fxq 'ExecStopPost=/usr/local/lib/probe-panel/probe-setup finalize-cleanup' "$finalizer_unit" ||
        die 'setup finalizer must delegate terminal cleanup to the state-aware setup helper'
    ! grep -Eq '^ExecStopPost=/usr/bin/(sleep|systemctl)' "$finalizer_unit" ||
        die 'setup finalizer must not unconditionally stop a retryable setup channel'
    grep -Fxq 'TimeoutStartSec=30min' "$finalizer_unit" ||
        die 'setup finalizer timeout must match the 30-minute broker deadline'
    grep -Fxq 'CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE CAP_FOWNER CAP_NET_BIND_SERVICE CAP_SETGID CAP_SETUID' "$finalizer_unit" ||
        die 'setup finalizer capability boundary changed'
    if [[ "$PLATFORM_SYSTEMD_PROFILE" == modern ]]; then
        grep -Fxq 'AmbientCapabilities=CAP_SETGID CAP_SETUID' "$finalizer_unit" ||
            die 'setup finalizer identity-switch capability contract changed'
        grep -Fxq 'ProtectSystem=strict' "$finalizer_unit" ||
            die 'modern setup finalizer must strictly protect the system'
    else
        grep -Fxq 'ProtectSystem=full' "$finalizer_unit" ||
            die 'legacy setup finalizer must protect the system'
        ! grep -Eq '^(SocketBindAllow|SocketBindDeny|ProtectProc|ProtectClock|ProtectKernelLogs|ProtectHostname|AmbientCapabilities)=' "$finalizer_unit" ||
            die 'legacy setup finalizer contains a directive newer than systemd 219'
    fi
    grep -Fxq 'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK' "$finalizer_unit" ||
        die 'setup finalizer listener-inspection address-family contract changed'
    local writable_path
    for writable_path in \
        /etc/probe-panel /etc/nginx/conf.d /etc/systemd/system /etc/letsencrypt \
        /var/lib/letsencrypt /var/log/letsencrypt /var/log/nginx /var/lib/nginx /var/lib/probe-panel/setup /srv/probe \
        /var/backups/probe-panel /run/lock /run/probe-panel-setup; do
        grep -Fxq "ReadWritePaths=$writable_path" "$finalizer_unit" ||
            die "setup finalizer is missing its reviewed writable path: $writable_path"
    done
    ! grep -Fxq 'ReadWritePaths=/etc/nginx/sites-enabled' "$finalizer_unit" ||
        die 'management setup finalizer must not write the Nginx sites-enabled directory'
    ! grep -Fxq 'ReadWritePaths=/etc/nginx' "$finalizer_unit" ||
        die 'setup finalizer must not make the entire Nginx configuration directory writable'
    if [[ "$PLATFORM_SYSTEMD_PROFILE" == modern ]]; then
        local -a finalizer_bind_rules=(tcp:80 tcp:443 tcp:18455)
        [[ "$(grep -Fc 'SocketBindAllow=' "$finalizer_unit")" -eq "${#finalizer_bind_rules[@]}" ]] ||
            die "setup finalizer has an unexpected ingress bind-rule count for $profile"
        local finalizer_bind_rule
        for finalizer_bind_rule in "${finalizer_bind_rules[@]}"; do
            grep -Fxq "SocketBindAllow=$finalizer_bind_rule" "$finalizer_unit" ||
                die "setup finalizer is missing its reviewed ingress bind rule: $finalizer_bind_rule"
        done
        grep -Fxq 'SocketBindDeny=any' "$finalizer_unit" || die 'setup finalizer bind policy changed'
    fi
    ! grep -Fxq 'SocketBindAllow=tcp:18080' "$finalizer_unit" ||
        die 'non-HTTP setup finalizer must not bind the setup listener'
    grep -Fxq 'PathExists=/run/probe-panel-setup/finalize.json' "$finalizer_path" ||
        die 'setup finalizer request path changed'
}

ensure_secure_directory() {
    local path="$1" mode="$2" expected_mode
    [[ "$mode" =~ ^0[0-7]{3}$ ]] || die "invalid secure directory mode for $path: $mode"
    expected_mode="${mode#0}"
    if [[ -L "$path" || ( -e "$path" && ! -d "$path" ) ]]; then
        die "$path must be a real directory"
    fi
    if [[ ! -e "$path" ]]; then
        install -d -o root -g root -m "$mode" "$path"
    fi
    [[ "$(stat -c '%u:%g:%a' "$path")" == "0:0:$expected_mode" ]] ||
        die "$path must already be root:root mode $mode; refusing to change an existing directory"
}

ensure_shared_root_directory() {
    local path="$1" create_mode="$2" mode
    if [[ -L "$path" || ( -e "$path" && ! -d "$path" ) ]]; then
        die "$path must be a real directory"
    fi
    if [[ ! -e "$path" ]]; then
        install -d -o root -g root -m "$create_mode" "$path"
    fi
    [[ "$(stat -c '%u:%g' "$path")" == 0:0 ]] ||
        die "$path must remain owned by root:root"
    mode="$(stat -c '%a' "$path")"
    [[ "$mode" =~ ^[0-7]{3,4}$ ]] || die "$path has an invalid mode"
    [[ "${mode: -2:1}" != [2367] && "${mode: -1}" != [2367] ]] ||
        die "$path must not be group- or world-writable"
}

ensure_backup_parent_directory() {
    local path="$1" probe_api_gid
    if [[ -L "$path" || ( -e "$path" && ! -d "$path" ) ]]; then
        die "$path must be a real directory"
    fi
    probe_api_gid="$(id -g probe-api)" || die 'could not resolve the probe-api primary GID'
    [[ "$probe_api_gid" =~ ^[0-9]+$ ]] || die 'the probe-api primary GID is invalid'
    if [[ ! -e "$path" ]]; then
        install -d -o root -g "$probe_api_gid" -m 0710 "$path"
    fi
    [[ "$(stat -c '%u:%g:%a' "$path")" == "0:$probe_api_gid:710" ]] ||
        die "$path must already be root:probe-api mode 0710; refusing to change an existing backup directory"
}

write_setup_environment() {
    local temporary
    [[ "$PLATFORM_CONTRACT" == "$MANAGEMENT_RUNTIME_ABI" ]] ||
        die 'the setup platform contract is unavailable'
    case ",$SUPPORTED_PLATFORM_IDS," in
        *",$PLATFORM_ID,"*) ;;
        *) die 'the setup platform ID is not in the accepted candidate platform matrix' ;;
    esac
    temporary="$(mktemp "${SETUP_CONFIG_ROOT}/.setup.env.XXXXXX")"
    if ! {
        printf 'PROBE_SETUP_STATE_FILE=%s\n' "$SETUP_STATE_FILE"
        printf 'PROBE_SETUP_SOCKET_PATH=%s\n' "$SETUP_SOCKET_PATH"
        printf 'PROBE_SETUP_SERVER_IP=%s\n' "$SETUP_SERVER_IP"
        printf 'PROBE_SETUP_ADMIN_ROOT=%s\n' "$SETUP_UI_ROOT"
        printf 'PROBE_SETUP_FINALIZE_REQUEST_FILE=%s\n' "$FINALIZER_REQUEST_FILE"
        printf 'PROBE_SETUP_FINALIZE_RESULT_FILE=%s\n' "$FINALIZER_RESULT_FILE"
        printf 'PROBE_SETUP_BUNDLE_ROOT=%s\n' "$INSTALLED_RELEASE"
        printf 'PROBE_SETUP_RELEASE_ID=%s\n' "$PANEL_VERSION"
        printf 'PROBE_SETUP_PROFILE=%s\n' "$PANEL_PROFILE"
        printf 'PROBE_SETUP_PLATFORM_ID=%s\n' "$PLATFORM_ID"
    } > "$temporary"; then
        rm -f -- "$temporary"
        return 1
    fi
    if ! chown root:root "$temporary" || ! chmod 0600 "$temporary" ||
       ! mv -f -- "$temporary" "$SETUP_ENV_FILE"; then
        rm -f -- "$temporary"
        return 1
    fi
    CREATED_ENV=1
}

remove_managed_tree() {
    local path="$1"
    local expected_amd64 expected_arm64
    [[ ! -e "$path" && ! -L "$path" ]] && return 0
    [[ -d "$path" && ! -L "$path" ]] || {
        warn "refusing to remove unexpected path: $path"
        return 1
    }
    [[ -f "$path/$MANAGED_MARKER" && ! -L "$path/$MANAGED_MARKER" ]] || {
        warn "refusing to remove unmarked directory: $path"
        return 1
    }
    expected_amd64="/srv/probe/releases/$(release_bundle_name "$PANEL_PROFILE" "$PANEL_VERSION" amd64)"
    expected_arm64="/srv/probe/releases/$(release_bundle_name "$PANEL_PROFILE" "$PANEL_VERSION" arm64)"
    case "$path" in
        "$SETUP_UI_ROOT"|"$expected_amd64"|"$expected_arm64"|"$PROGRAM_ROOT")
            rm -rf -- "$path"
            ;;
        *)
            warn "refusing to remove path outside the bootstrap allowlist: $path"
            return 1
            ;;
    esac
}

cleanup_install() {
    local status=$?
    trap - EXIT HUP INT TERM
    set +e

    if [[ "$HOST_MUTATION_STARTED" -eq 1 && "$NGINX_MASKED_BY_INSTALLER" -eq 1 ]]; then
        if systemctl unmask nginx.service >/dev/null 2>&1; then
            NGINX_MASKED_BY_INSTALLER=0
        else
            warn 'could not remove the temporary Nginx service mask during failed-install cleanup'
        fi
    fi

    if [[ "$HOST_MUTATION_STARTED" -eq 1 && "$INSTALL_COMPLETED" -ne 1 ]]; then
        if [[ "$NGINX_ABSENT_AT_START" -eq 1 ]] && unit_is_installed nginx.service; then
            systemctl stop nginx.service >/dev/null 2>&1 || :
            systemctl disable nginx.service >/dev/null 2>&1 || :
            systemctl reset-failed nginx.service >/dev/null 2>&1 || :
        fi
        if [[ "$INSTALLED_UNIT" -eq 1 ]]; then
            systemctl stop "$FINALIZER_PATH_SERVICE" "$FINALIZER_SERVICE" >/dev/null 2>&1 || :
            systemctl disable "$FINALIZER_PATH_SERVICE" >/dev/null 2>&1 || :
            systemctl stop "$SETUP_SERVICE" >/dev/null 2>&1 || :
            systemctl disable "$SETUP_SERVICE" >/dev/null 2>&1 || :
            systemctl stop "$SETUP_SOCKET_SERVICE" >/dev/null 2>&1 || :
            systemctl disable "$SETUP_SOCKET_SERVICE" >/dev/null 2>&1 || :
            rm -f -- "$SETUP_UNIT" "$SETUP_SOCKET_UNIT" "$FINALIZER_UNIT" "$FINALIZER_PATH_UNIT"
            systemctl daemon-reload >/dev/null 2>&1 || :
        fi
        remove_managed_tree "$SETUP_UI_ROOT" >/dev/null 2>&1 || :
        if [[ -n "$INSTALLED_RELEASE" ]]; then
            remove_managed_tree "$INSTALLED_RELEASE" >/dev/null 2>&1 || :
        fi
        remove_managed_tree "$PROGRAM_ROOT" >/dev/null 2>&1 || :
        if [[ "$CREATED_STATE" -eq 1 ]]; then
            rm -f -- "$SETUP_STATE_FILE"
        fi
        if [[ "$CREATED_ENV" -eq 1 ]]; then
            rm -f -- "$SETUP_ENV_FILE"
        fi
        if [[ "$POSTGRESQL_STATE_CAPTURED" -eq 1 && "$POSTGRESQL_WAS_ACTIVE" -eq 0 ]]; then
            systemctl stop "$PLATFORM_POSTGRES_SERVICE" >/dev/null 2>&1 || :
        fi
        if [[ "$PACKAGE_SOURCE_CONSUMED" -eq 0 ]]; then
            rollback_created_package_sources
        elif (( ${#PACKAGE_SOURCE_CREATED_PATHS[@]} > 0 )); then
            warn 'preserving the managed package sources because a package transaction may have consumed them'
        fi
        if [[ "$PLATFORM_ADAPTER" == centos ]] &&
           declare -F centos_platform_cleanup_package_state >/dev/null; then
            centos_platform_cleanup_package_state ||
                warn 'could not restore the pre-install CentOS package-module state safely'
        fi
    fi

    if [[ -n "$TEMP_ROOT" && "$TEMP_ROOT" == /var/tmp/probe-panel-bootstrap.* &&
          -d "$TEMP_ROOT" && ! -L "$TEMP_ROOT" ]]; then
        rm -rf -- "$TEMP_ROOT"
    fi
    exit "$status"
}

wait_for_setup_service() {
    local remaining=30
    while (( remaining > 0 )); do
        if systemctl is-active --quiet "$SETUP_SOCKET_SERVICE" &&
           systemctl is-active --quiet "$SETUP_SERVICE" &&
           [[ -S "$SETUP_SOCKET_PATH" ]] &&
           curl -q --fail --silent --show-error --max-time 2 \
               --unix-socket "$SETUP_SOCKET_PATH" \
               http://localhost:18080/install >/dev/null; then
            return 0
        fi
        if systemctl is-failed --quiet "$SETUP_SERVICE"; then
            return 1
        fi
        sleep 1
        ((remaining--))
    done
    return 1
}

assert_setup_listener() {
    [[ -S "$SETUP_SOCKET_PATH" && ! -L "$SETUP_SOCKET_PATH" ]] ||
        die 'setup Unix socket is missing or unsafe'
    [[ "$(stat -c '%U:%G' "$SETUP_SOCKET_PATH")" == root:root ]] ||
        die 'setup Unix socket must be owned by root:root'
    [[ "$(stat -c '%a' "$SETUP_SOCKET_PATH")" == 600 ]] ||
        die 'setup Unix socket must have mode 0600'
    [[ "$(stat -c '%U:%G' "$FINALIZER_RUNTIME_ROOT")" == root:root &&
       "$(stat -c '%a' "$FINALIZER_RUNTIME_ROOT")" == 700 ]] ||
        die 'setup Unix socket directory must be root:root mode 0700'
    if ss -H -lnt | awk '$4 ~ /:18080$/ { found=1 } END { exit !found }'; then
        die 'the server must not expose a TCP listener on setup port 18080'
    fi
}

install_action() {
    require_root
    # shellcheck disable=SC2119
    select_supported_platform
    validate_platform_lifecycle
    validate_release_settings
    require_preflight_commands
    acquire_bootstrap_lock
    assert_fresh_target
    trap cleanup_install EXIT
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM
    local architecture asset_name release_url manifest_url archive manifest extraction_root bundle_root
    local init_output unit_source socket_unit_source finalizer_unit_source system_directory
    local initial_platform_fingerprint
    local initial_nginx_preexisting initial_postgresql_preexisting
    initial_platform_fingerprint="$(platform_contract_fingerprint)"
    preflight_systemd_host
    preflight_platform_security
    TEMP_ROOT="$(mktemp -d /var/tmp/probe-panel-bootstrap.XXXXXX)"
    capture_postgresql_start_state
    preflight_existing_runtimes
    detect_server_ip

    initial_nginx_preexisting="$NGINX_PREEXISTED"
    initial_postgresql_preexisting="$POSTGRESQL_PREEXISTED"
    architecture="$(detect_architecture)"
    asset_name="$(release_asset_name "$PANEL_PROFILE" "$PANEL_VERSION" "$architecture")"
    release_url="${RELEASE_BASE_URL}/${asset_name}"
    manifest_url="${RELEASE_BASE_URL}/SHA256SUMS"

    archive="$TEMP_ROOT/$asset_name"
    manifest="$TEMP_ROOT/SHA256SUMS"
    extraction_root="$TEMP_ROOT/extracted"
    mkdir -m 0700 "$extraction_root"

    log "downloading immutable $PANEL_VERSION release for linux-$architecture"
    download_file "$manifest" "$manifest_url" 60
    [[ -s "$manifest" && "$(stat -c '%s' "$manifest")" -le 1048576 ]] ||
        die 'release SHA256SUMS is empty or exceeds 1 MiB'
    download_file "$archive" "$release_url" 600
    [[ -s "$archive" && "$(stat -c '%s' "$archive")" -le 536870912 ]] ||
        die 'release archive is empty or exceeds 512 MiB'
    verify_release_archive "$manifest" "$archive" "$asset_name"
    safe_extract_archive "$archive" "$extraction_root"

    bundle_root="$extraction_root/$(release_bundle_name "$PANEL_PROFILE" "$PANEL_VERSION" "$architecture")"
    [[ -d "$bundle_root" && ! -L "$bundle_root" ]] || die 'release archive has an unexpected root directory'
    [[ "$(find "$extraction_root" -mindepth 1 -maxdepth 1 -type d | wc -l)" -eq 1 ]] ||
        die 'release archive must contain exactly one root directory'
    validate_release_bundle "$bundle_root" "$architecture" "$PANEL_PROFILE"

    # Recheck every read-only host contract while holding the bootstrap lock.
    # No package, account, service, or permanent path is touched unless the
    # immutable release and both host snapshots have passed validation.
    # shellcheck disable=SC2119
    select_supported_platform
    validate_platform_lifecycle
    [[ "$(platform_contract_fingerprint)" == "$initial_platform_fingerprint" ]] ||
        die 'the selected platform changed during release verification; retry after the host is stable'
    assert_fresh_target
    preflight_systemd_host
    preflight_platform_security
    preflight_existing_runtimes
    [[ "$NGINX_PREEXISTED" -eq "$initial_nginx_preexisting" &&
       "$POSTGRESQL_PREEXISTED" -eq "$initial_postgresql_preexisting" ]] ||
        die 'the Nginx or PostgreSQL runtime changed during release verification; retry after the host is stable'

    HOST_MUTATION_STARTED=1
    install_runtime_dependencies "$PANEL_PROFILE"
    prepare_probe_api_account
    prepare_runtime_services "$PANEL_PROFILE"

    ensure_secure_directory /srv/probe 0755
    ensure_secure_directory "$RELEASES_ROOT" 0755
    # Nginx must be able to traverse this root-owned directory to serve the
    # public IP-mode CA certificate. Sensitive children remain individually
    # restricted (for example setup.env is root:root 0600).
    ensure_secure_directory "$SETUP_CONFIG_ROOT" 0755
    [[ "$(stat -c '%u:%g:%a' "$SETUP_CONFIG_ROOT")" == 0:0:755 ]] ||
        die "$SETUP_CONFIG_ROOT must be root:root mode 0755"
    ensure_secure_directory /var/lib/probe-panel 0700
    ensure_secure_directory "$SETUP_STATE_ROOT" 0700
    # Certbot owns these shared roots. Existing secure permissions are accepted
    # verbatim so a management installation cannot disrupt another site.
    ensure_shared_root_directory /etc/letsencrypt 0700
    ensure_shared_root_directory /var/lib/letsencrypt 0700
    ensure_shared_root_directory /var/log/letsencrypt 0700
    ensure_backup_parent_directory /var/backups/probe-panel
    ensure_secure_directory /usr/local/lib 0755
    for system_directory in /etc/nginx/conf.d /etc/systemd/system /run/lock; do
        [[ -d "$system_directory" && ! -L "$system_directory" ]] ||
            die "required system directory is missing or unsafe: $system_directory"
    done

    INSTALLED_RELEASE="$RELEASES_ROOT/$(release_bundle_name "$PANEL_PROFILE" "$PANEL_VERSION" "$architecture")"
    [[ ! -e "$INSTALLED_RELEASE" && ! -L "$INSTALLED_RELEASE" ]] ||
        die "release directory already exists: $INSTALLED_RELEASE"
    : > "$bundle_root/$MANAGED_MARKER"
    chmod 0600 "$bundle_root/$MANAGED_MARKER"
    mv -T -- "$bundle_root" "$INSTALLED_RELEASE"
    chown -R root:root "$INSTALLED_RELEASE"

    install -d -o root -g root -m 0755 "$PROGRAM_ROOT"
    : > "$PROGRAM_ROOT/$MANAGED_MARKER"
    chmod 0600 "$PROGRAM_ROOT/$MANAGED_MARKER"
    install -o root -g root -m 0755 "$INSTALLED_RELEASE/setup/probe-setup" "$SETUP_BINARY"

    cp -a -- "$INSTALLED_RELEASE/artifacts/admin" "$SETUP_UI_ROOT"
    : > "$SETUP_UI_ROOT/$MANAGED_MARKER"
    chmod 0600 "$SETUP_UI_ROOT/$MANAGED_MARKER"
    chown -R root:root "$SETUP_UI_ROOT"
    find "$SETUP_UI_ROOT" -type d -exec chmod 0755 {} +
    find "$SETUP_UI_ROOT" -type f ! -name "$MANAGED_MARKER" -exec chmod 0644 {} +

    unit_source="$INSTALLED_RELEASE/source/probe-api/deploy/setup/$(selected_setup_asset_name probe-panel-setup.service)"
    socket_unit_source="$INSTALLED_RELEASE/source/probe-api/deploy/setup/$(selected_setup_asset_name probe-panel-setup.socket)"
    finalizer_unit_source="$INSTALLED_RELEASE/source/probe-api/deploy/setup/$(selected_setup_asset_name probe-panel-finalizer-management.service)"
    install -o root -g root -m 0644 "$unit_source" "$SETUP_UNIT"
    install -o root -g root -m 0644 "$socket_unit_source" "$SETUP_SOCKET_UNIT"
    sed "s/postgresql[.]service/$PLATFORM_POSTGRES_SERVICE/g" "$finalizer_unit_source" > "$FINALIZER_UNIT"
    chown root:root "$FINALIZER_UNIT"
    chmod 0644 "$FINALIZER_UNIT"
    install -o root -g root -m 0644 \
        "$INSTALLED_RELEASE/source/probe-api/deploy/setup/probe-panel-finalizer.path" \
        "$FINALIZER_PATH_UNIT"
    INSTALLED_UNIT=1
    write_setup_environment

    init_output="$(
        PROBE_SETUP_STATE_FILE="$SETUP_STATE_FILE" \
        PROBE_SETUP_ADMIN_ROOT="$SETUP_UI_ROOT" \
        PROBE_SETUP_PROFILE="$PANEL_PROFILE" \
        "$SETUP_BINARY" init
    )"
    [[ -z "$init_output" ]] || die 'setup helper unexpectedly printed sensitive initialization output'
    CREATED_STATE=1

    systemctl daemon-reload
    systemd-analyze verify "$SETUP_SOCKET_UNIT" "$SETUP_UNIT" "$FINALIZER_UNIT" "$FINALIZER_PATH_UNIT"
    systemctl enable "$SETUP_SOCKET_SERVICE"
    systemctl start "$SETUP_SOCKET_SERVICE"
    systemctl enable "$SETUP_SERVICE"
    systemctl start "$SETUP_SERVICE"
    systemctl enable "$FINALIZER_PATH_SERVICE"
    systemctl start "$FINALIZER_PATH_SERVICE"
    if ! wait_for_setup_service; then
        journalctl -u "$SETUP_SERVICE" -n 20 --no-pager >&2 || :
        die 'root-only Unix Socket setup service did not become ready'
    fi
    systemctl is-active --quiet "$FINALIZER_PATH_SERVICE" || die 'setup finalizer path watcher is not active'
    assert_setup_listener

    INSTALL_COMPLETED=1
    printf '\nProbe Panel 初始化服务已启动。\n'
    printf '不需要安装码。请在你的电脑执行 root SSH 隧道：\n'
    printf '  ssh -N -o ExitOnForwardFailure=yes -L 127.0.0.1:18080:/run/probe-panel-setup/setup.sock root@%s\n\n' "$SETUP_SERVER_IP"
    printf '然后在本机浏览器打开：\n'
    printf '  http://127.0.0.1:18080/install\n\n'
    printf 'setup 只使用服务器 root 私有 Unix Socket，不监听任何服务器 TCP 端口。\n'
    printf '管理端域名留空时使用 %s 的 18455 HTTPS 端口；填写管理端域名时使用独占 80/443 的 ACME 模式。\n' "$SETUP_SERVER_IP"
    printf '此安装不包含访客前端或 Agent；它们必须在管理端完成后分别安装。\n'
}

status_action() {
    local service_status='not-installed'
    local socket_status='not-installed'
    if command -v systemctl >/dev/null 2>&1 && unit_is_installed "$SETUP_SERVICE"; then
        service_status="$(systemctl is-active "$SETUP_SERVICE" 2>/dev/null || :)"
        [[ -n "$service_status" ]] || service_status=inactive
    fi
    if command -v systemctl >/dev/null 2>&1 && unit_is_installed "$SETUP_SOCKET_SERVICE"; then
        socket_status="$(systemctl is-active "$SETUP_SOCKET_SERVICE" 2>/dev/null || :)"
        [[ -n "$socket_status" ]] || socket_status=inactive
    fi
    printf 'Probe Panel bootstrap status\n'
    printf '  setup service: %s\n' "$service_status"
    printf '  setup socket:  %s\n' "$socket_status"
    if command -v systemctl >/dev/null 2>&1; then
        printf '  finalizer path: %s\n' "$(systemctl is-active "$FINALIZER_PATH_SERVICE" 2>/dev/null || :)"
    fi
    printf '  setup binary:  %s\n' "$([[ -x "$SETUP_BINARY" ]] && printf present || printf absent)"
    printf '  setup state:   %s\n' "$([[ -f "$SETUP_STATE_FILE" && ! -L "$SETUP_STATE_FILE" ]] && printf present || printf absent)"
    printf '  socket path:   %s\n' "$([[ -S "$SETUP_SOCKET_PATH" && ! -L "$SETUP_SOCKET_PATH" ]] && printf present || printf absent)"
    printf '  configuration: %s\n' "$([[ -f "$SETUP_ENV_FILE" && ! -L "$SETUP_ENV_FILE" ]] && printf present || printf absent)"
    printf '  installer profile: %s %s\n' "$PANEL_PROFILE" "$PANEL_VERSION"
    printf 'Probe Panel management setup does not display or require an installation code.\n'
    printf 'Database and administrator credentials are never displayed.\n'
}

validate_action() {
    require_root
    [[ -x "$MANAGEMENT_VALIDATE_BINARY" && ! -L "$MANAGEMENT_VALIDATE_BINARY" ]] ||
        die 'the installed management validator is unavailable; this is not a finalized v1.2+ management installation'
    "$MANAGEMENT_VALIDATE_BINARY" all
}

upgrade_action() {
    require_root
    # shellcheck disable=SC2119
    select_supported_platform
    validate_platform_lifecycle
    validate_release_settings
    require_preflight_commands
    acquire_bootstrap_lock
    [[ -x "$MANAGEMENT_VALIDATE_BINARY" && ! -L "$MANAGEMENT_VALIDATE_BINARY" ]] ||
        die 'the installed management validator is unavailable; only finalized v1.2+ management installations can use this upgrade path'
    "$MANAGEMENT_VALIDATE_BINARY" host

    trap cleanup_install EXIT
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM
    local architecture asset_name release_url manifest_url archive manifest extraction_root bundle_root
    local initial_platform_fingerprint install_release
    initial_platform_fingerprint="$(platform_contract_fingerprint)"
    preflight_systemd_host
    preflight_platform_security
    TEMP_ROOT="$(mktemp -d /var/tmp/probe-panel-bootstrap.XXXXXX)"
    architecture="$(detect_architecture)"
    asset_name="$(release_asset_name "$PANEL_PROFILE" "$PANEL_VERSION" "$architecture")"
    release_url="${RELEASE_BASE_URL}/${asset_name}"
    manifest_url="${RELEASE_BASE_URL}/SHA256SUMS"
    archive="$TEMP_ROOT/$asset_name"
    manifest="$TEMP_ROOT/SHA256SUMS"
    extraction_root="$TEMP_ROOT/extracted"
    mkdir -m 0700 "$extraction_root"

    log "downloading immutable $PANEL_VERSION management release for upgrade"
    download_file "$manifest" "$manifest_url" 60
    [[ -s "$manifest" && "$(stat -c '%s' "$manifest")" -le 1048576 ]] ||
        die 'release SHA256SUMS is empty or exceeds 1 MiB'
    download_file "$archive" "$release_url" 600
    [[ -s "$archive" && "$(stat -c '%s' "$archive")" -le 536870912 ]] ||
        die 'release archive is empty or exceeds 512 MiB'
    verify_release_archive "$manifest" "$archive" "$asset_name"
    safe_extract_archive "$archive" "$extraction_root"
    bundle_root="$extraction_root/$(release_bundle_name "$PANEL_PROFILE" "$PANEL_VERSION" "$architecture")"
    [[ -d "$bundle_root" && ! -L "$bundle_root" ]] ||
        die 'release archive has an unexpected root directory'
    [[ "$(find "$extraction_root" -mindepth 1 -maxdepth 1 -type d | wc -l)" -eq 1 ]] ||
        die 'release archive must contain exactly one root directory'
    validate_release_bundle "$bundle_root" "$architecture" "$PANEL_PROFILE"

    # Re-prove the exact target after the immutable bundle has been verified.
    # The packaged release installer owns the deployment/database locks and all
    # mutation/rollback ordering from this point onward.
    # shellcheck disable=SC2119
    select_supported_platform
    validate_platform_lifecycle
    [[ "$(platform_contract_fingerprint)" == "$initial_platform_fingerprint" ]] ||
        die 'the selected platform changed during upgrade verification'
    preflight_systemd_host
    preflight_platform_security
    "$MANAGEMENT_VALIDATE_BINARY" host
    install_release="$bundle_root/source/probe-api/deploy/scripts/install-release.sh"
    [[ -x "$install_release" && ! -L "$install_release" ]] ||
        die 'the verified management release installer is missing or unsafe'
    "$install_release" --bundle-root "$bundle_root" --release-id "$PANEL_VERSION" --profile management
    "$MANAGEMENT_VALIDATE_BINARY" all
    log "management upgrade to $PANEL_VERSION completed"
}

uninstall_action() {
    local cleanup_failed=0
    require_root
    require_command systemctl
    require_command flock
    require_command stat
    acquire_bootstrap_lock

    if [[ -e /srv/probe/api/probe-api || -L /srv/probe/api/probe-api ||
          -e /etc/systemd/system/probe-api.service || -L /etc/systemd/system/probe-api.service ]]; then
        [[ -x "$MANAGEMENT_UNINSTALL_BINARY" && ! -L "$MANAGEMENT_UNINSTALL_BINARY" ]] ||
            die 'a finalized management product exists but its reviewed uninstaller is missing; refusing a partial bootstrap-only uninstall'
        "$MANAGEMENT_UNINSTALL_BINARY"
    fi

    systemctl stop "$FINALIZER_PATH_SERVICE" "$FINALIZER_SERVICE" >/dev/null 2>&1 || :
    systemctl disable "$FINALIZER_PATH_SERVICE" >/dev/null 2>&1 || :
    systemctl stop "$SETUP_SERVICE" >/dev/null 2>&1 || :
    systemctl disable "$SETUP_SERVICE" >/dev/null 2>&1 || :
    systemctl stop "$SETUP_SOCKET_SERVICE" >/dev/null 2>&1 || :
    systemctl disable "$SETUP_SOCKET_SERVICE" >/dev/null 2>&1 || :
    rm -f -- "$SETUP_UNIT" "$SETUP_SOCKET_UNIT" "$FINALIZER_UNIT" "$FINALIZER_PATH_UNIT"
    systemctl daemon-reload

    remove_managed_tree "$SETUP_UI_ROOT" || cleanup_failed=1
    remove_managed_tree "/srv/probe/releases/$(release_bundle_name "$PANEL_PROFILE" "$PANEL_VERSION" amd64)" || cleanup_failed=1
    remove_managed_tree "/srv/probe/releases/$(release_bundle_name "$PANEL_PROFILE" "$PANEL_VERSION" arm64)" || cleanup_failed=1
    remove_managed_tree "$PROGRAM_ROOT" || cleanup_failed=1
    (( cleanup_failed == 0 )) ||
        die 'management activation was removed, but one or more marked bootstrap directories require manual review'

    printf '%s\n' \
        'Probe Panel management activation and bootstrap programs were uninstalled.' \
        'Preserved: /etc/probe-panel, /var/lib/probe-panel, /var/backups/probe-panel,' \
        'and all PostgreSQL databases. Purge is intentionally unsupported.'
}

# Keep every external command and installation side effect behind this complete
# parse barrier. A truncated curl | bash stream cannot enter main().
main() {
    local supplied_count=$# entrypoint_sentinel=''
    (( supplied_count > 0 )) || die 'bootstrap input is incomplete; refusing to enter the installer'
    entrypoint_sentinel="${!supplied_count}"
    [[ "$entrypoint_sentinel" == "$BOOTSTRAP_ENTRYPOINT_SENTINEL" ]] ||
        die 'bootstrap input did not contain the complete entrypoint sentinel'
    set -- "${@:1:supplied_count-1}"

    local action=install
    if (($# > 0)); then
        case "$1" in
            -h|--help|help)
                action=help
                shift
                ;;
            --*) ;;
            *)
                action="$1"
                shift
                ;;
        esac
    fi
    while (($# > 0)); do
        case "$1" in
            --accept-eol)
                [[ ( "$action" == install || "$action" == upgrade ) && "$ACCEPT_EOL" == 0 ]] ||
                    die '--accept-eol is accepted exactly once and only with install or upgrade'
                ACCEPT_EOL=1
                ;;
            *) die 'unsupported option; credentials are never command-line arguments' ;;
        esac
        shift
    done

    case "$action" in
        install) install_action ;;
        upgrade) upgrade_action ;;
        validate) validate_action ;;
        status) status_action ;;
        uninstall) uninstall_action ;;
        purge) die 'purge is not implemented; preserve data and perform a separately reviewed, backup-verified removal' ;;
        -h|--help|help) usage ;;
        *) die "unknown command: $action" ;;
    esac
}

# --- BEGIN GENERATED PLATFORM ADAPTER: debian ---
# Debian platform adapter for the management bootstrap.
# This file is concatenated into the public standalone install.sh; it must only
# define functions and must never perform work while being parsed.
# shellcheck shell=bash
# Adapter globals are consumed after this file is concatenated with common.sh.
# shellcheck disable=SC2034,SC2154

debian_platform_configure() {
    local version_id="$1"
    case "$version_id" in
        9)
            configure_deb_platform debian-9-systemd 232 legacy nginx-full nginx-full classic postgresql-14 postgresql-client-14 1
            PLATFORM_APT_CODENAME=stretch
            PLATFORM_APT_BASE_MODE=debian-archive
            PLATFORM_PGDG_APT_BASE_URL='https://apt-archive.postgresql.org/pub/repos/apt'
            ;;
        10)
            configure_deb_platform debian-10-systemd 241 legacy nginx-full nginx-full legacy postgresql-14 postgresql-client-14 1
            PLATFORM_APT_CODENAME=buster
            PLATFORM_APT_BASE_MODE=debian-archive
            PLATFORM_PGDG_APT_BASE_URL='https://apt-archive.postgresql.org/pub/repos/apt'
            ;;
        11)
            configure_deb_platform debian-11-systemd 247 legacy nginx-core nginx-core legacy postgresql-14 postgresql-client-14 1
            PLATFORM_APT_CODENAME=bullseye
            PLATFORM_APT_BASE_MODE=debian-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt.postgresql.org/pub/repos/apt'
            ;;
        12)
            configure_deb_platform debian-12-systemd 252 modern nginx nginx legacy postgresql-14 postgresql-client-14 1
            PLATFORM_APT_CODENAME=bookworm
            PLATFORM_APT_BASE_MODE=debian-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt.postgresql.org/pub/repos/apt'
            ;;
        13)
            configure_deb_platform debian-13-systemd 257 modern nginx nginx modern postgresql-14 postgresql-client-14
            PLATFORM_APT_CODENAME=trixie
            PLATFORM_APT_BASE_MODE=debian-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt.postgresql.org/pub/repos/apt'
            ;;
        *)
            die "platform debian ${version_id:-unknown} is not in the accepted candidate matrix; accepted candidate platform IDs: $SUPPORTED_PLATFORM_IDS"
            ;;
    esac
}

debian_platform_preflight_commands() { deb_family_platform_preflight_commands; }
debian_platform_native_unit_paths() { deb_family_platform_native_unit_paths "$@"; }
debian_platform_assert_packaged_file() { deb_family_platform_assert_packaged_file "$@"; }
debian_platform_assert_postgresql_clients() { deb_family_platform_assert_postgresql_clients; }
debian_platform_preflight_security() { deb_family_platform_preflight_security; }
debian_platform_runtime_packages() { deb_family_platform_runtime_packages; }
debian_platform_prepare_package_sources() { deb_family_platform_prepare_package_sources; }
debian_platform_install_packages() { deb_family_platform_install_packages "$@"; }
debian_platform_initialize_postgresql() { deb_family_platform_initialize_postgresql "$@"; }
debian_platform_create_service_account() { deb_family_platform_create_service_account; }
debian_platform_validate_nologin_shell() { deb_family_platform_validate_nologin_shell "$@"; }
debian_platform_disable_default_nginx_site() { deb_family_platform_disable_default_nginx_site; }

# --- END GENERATED PLATFORM ADAPTER: debian ---

# --- BEGIN GENERATED PLATFORM ADAPTER: ubuntu ---
# Ubuntu platform adapter for the management bootstrap.
# Ubuntu remains a separate adapter even though it intentionally reuses the
# reviewed deb-family package and account helpers from install/common.sh.
# shellcheck shell=bash
# Adapter globals are consumed after this file is concatenated with common.sh.
# shellcheck disable=SC2034,SC2154

ubuntu_platform_configure() {
    local version_id="$1"
    case "$version_id" in
        18.04)
            configure_deb_platform ubuntu-18.04-systemd 237 legacy nginx-core nginx-core legacy postgresql-14 postgresql-client-14 1
            PLATFORM_APT_CODENAME=bionic
            PLATFORM_APT_BASE_MODE=ubuntu-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt-archive.postgresql.org/pub/repos/apt'
            ;;
        20.04)
            configure_deb_platform ubuntu-20.04-systemd 245 legacy nginx-core nginx-core legacy postgresql-14 postgresql-client-14 1
            PLATFORM_APT_CODENAME=focal
            PLATFORM_APT_BASE_MODE=ubuntu-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt-archive.postgresql.org/pub/repos/apt'
            ;;
        22.04)
            configure_deb_platform ubuntu-22.04-systemd 249 modern nginx-core nginx-core legacy postgresql-14 postgresql-client-14
            PLATFORM_APT_CODENAME=jammy
            PLATFORM_APT_BASE_MODE=ubuntu-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt.postgresql.org/pub/repos/apt'
            ;;
        24.04)
            configure_deb_platform ubuntu-24.04-systemd 255 modern nginx nginx legacy postgresql-14 postgresql-client-14
            PLATFORM_APT_CODENAME=noble
            PLATFORM_APT_BASE_MODE=ubuntu-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt.postgresql.org/pub/repos/apt'
            ;;
        26.04)
            configure_deb_platform ubuntu-26.04-systemd 259 modern nginx nginx modern postgresql-14 postgresql-client-14
            PLATFORM_APT_CODENAME=resolute
            PLATFORM_APT_BASE_MODE=ubuntu-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt.postgresql.org/pub/repos/apt'
            ;;
        *)
            die "platform ubuntu ${version_id:-unknown} is not in the accepted candidate matrix; accepted candidate platform IDs: $SUPPORTED_PLATFORM_IDS"
            ;;
    esac
}

ubuntu_platform_preflight_commands() { deb_family_platform_preflight_commands; }
ubuntu_platform_native_unit_paths() { deb_family_platform_native_unit_paths "$@"; }
ubuntu_platform_assert_packaged_file() { deb_family_platform_assert_packaged_file "$@"; }
ubuntu_platform_assert_postgresql_clients() { deb_family_platform_assert_postgresql_clients; }
ubuntu_platform_preflight_security() { deb_family_platform_preflight_security; }
ubuntu_platform_runtime_packages() { deb_family_platform_runtime_packages; }
ubuntu_platform_prepare_package_sources() { deb_family_platform_prepare_package_sources; }
ubuntu_platform_install_packages() { deb_family_platform_install_packages "$@"; }
ubuntu_platform_initialize_postgresql() { deb_family_platform_initialize_postgresql "$@"; }
ubuntu_platform_create_service_account() { deb_family_platform_create_service_account; }
ubuntu_platform_validate_nologin_shell() { deb_family_platform_validate_nologin_shell "$@"; }
ubuntu_platform_disable_default_nginx_site() { deb_family_platform_disable_default_nginx_site; }

# --- END GENERATED PLATFORM ADAPTER: ubuntu ---

# --- BEGIN GENERATED PLATFORM ADAPTER: centos ---
# CentOS Linux / CentOS Stream platform adapter for the management bootstrap.
# The exact product NAME remains part of the contract so Linux and Stream are
# never inferred from ID_LIKE or from the presence of yum/dnf.
# shellcheck shell=bash
# Adapter globals are consumed after this file is concatenated with common.sh.
# shellcheck disable=SC2034,SC2154

centos_platform_configure_signing_contract() {
    local kernel_arch
    kernel_arch="$(uname -m 2>/dev/null)" ||
        die 'the CentOS kernel architecture could not be determined'
    case "$PLATFORM_ID:$kernel_arch" in
        centos-linux-7-systemd:x86_64)
            CENTOS_BASE_KEY_FINGERPRINT='6341AB2753D78A78A7C27BB124C6A8A7F4A80EB5'
            ;;
        centos-linux-7-systemd:aarch64)
            CENTOS_BASE_KEY_FINGERPRINT='EF8F3CA66EFDF32B36CDADF76C7CB6EF305D49D6'
            ;;
        centos-linux-8-systemd:x86_64|centos-linux-8-systemd:aarch64|centos-stream-8-systemd:x86_64|centos-stream-8-systemd:aarch64|centos-stream-9-systemd:x86_64|centos-stream-9-systemd:aarch64|centos-stream-10-systemd:x86_64|centos-stream-10-systemd:aarch64)
            CENTOS_BASE_KEY_FINGERPRINT='99DB70FAE1D7CE227FB6488205B555B38483C65D'
            ;;
        *) die "the CentOS signing contract does not support $PLATFORM_ID architecture $kernel_arch" ;;
    esac
    case "$PLATFORM_RPM_EL_MAJOR" in
        7) CENTOS_EPEL_KEY_FINGERPRINT='91E97D7C4A5E96F17F3E888F6A2FAEA2352C64E5' ;;
        8) CENTOS_EPEL_KEY_FINGERPRINT='94E279EB8D8F25B21810ADF121EA45AB2F86D6A1' ;;
        9) CENTOS_EPEL_KEY_FINGERPRINT='FF8AD1344597106ECE813B918A3872BF3228467C' ;;
        10) CENTOS_EPEL_KEY_FINGERPRINT='7D8D15CBFC4E62688591FB2633D98517E37ED158' ;;
        *) die 'the EPEL signing contract is unavailable' ;;
    esac
    case "$PLATFORM_RPM_EL_MAJOR:$kernel_arch" in
        7:x86_64) CENTOS_PGDG_KEY_FINGERPRINT='F245F0BF96AC182744CAFF2E64FACE1173E3B907' ;;
        7:aarch64) CENTOS_PGDG_KEY_FINGERPRINT='C78CD9E6DA3E1F5B5B16FC1A9FCD879F55B374B8' ;;
        8:x86_64|9:x86_64|10:x86_64)
            CENTOS_PGDG_KEY_FINGERPRINT='D4BF08AE67A0B4C7A1DBCCD240BCA2B408B40D20'
            ;;
        8:aarch64|9:aarch64|10:aarch64)
            CENTOS_PGDG_KEY_FINGERPRINT='B031F89FC983E98262906B6E177B343BB9738825'
            ;;
        *) die 'the PGDG signing contract is unavailable' ;;
    esac
}

centos_platform_configure() {
    local version_id="$1" os_name="$2"
    case "$version_id:$os_name" in
        '7:CentOS Linux')
            configure_rpm_platform centos-linux-7-systemd yum 219 legacy classic nginx 1
            ;;
        '8:CentOS Linux')
            configure_rpm_platform centos-linux-8-systemd dnf 239 legacy legacy nginx-core 1
            ;;
        '8:CentOS Stream')
            configure_rpm_platform centos-stream-8-systemd dnf 239 legacy legacy nginx-core 1
            ;;
        '9:CentOS Stream')
            configure_rpm_platform centos-stream-9-systemd dnf 252 modern legacy nginx-core
            ;;
        '10:CentOS Stream')
            configure_rpm_platform centos-stream-10-systemd dnf 257 modern modern nginx-core
            ;;
        *)
            die "platform centos ${version_id:-unknown} ${os_name:-unknown} is not in the accepted candidate matrix; accepted candidate platform IDs: $SUPPORTED_PLATFORM_IDS"
            ;;
    esac
    centos_platform_configure_signing_contract
}

centos_platform_preflight_commands() {
    printf '%s\n' groupadd useradd "$PLATFORM_PACKAGE_MANAGER" rpm rpmkeys
}

centos_platform_native_unit_paths() {
    printf '/usr/lib/systemd/system/%s\n' "$1"
}

centos_platform_assert_packaged_file() {
    local file_path="$1" package_name="$2" expected_fingerprint=''
    case "$package_name" in
        postgresql14|postgresql14-server)
            expected_fingerprint="$CENTOS_PGDG_KEY_FINGERPRINT"
            ;;
        nginx)
            [[ "$PLATFORM_RPM_EL_MAJOR" == 7 ]] ||
                die 'the CentOS nginx package ownership contract is invalid for this EL generation'
            expected_fingerprint="$CENTOS_EPEL_KEY_FINGERPRINT"
            ;;
        nginx-core)
            [[ "$PLATFORM_RPM_EL_MAJOR" != 7 ]] ||
                die 'the CentOS nginx-core package ownership contract is invalid for EL7'
            expected_fingerprint="$CENTOS_BASE_KEY_FINGERPRINT"
            ;;
        *)
            die "the CentOS RPM signing-key binding is unavailable for package $package_name"
            ;;
    esac
    [[ "$expected_fingerprint" =~ ^[0-9A-F]{40}$ ]] ||
        die "the CentOS RPM signing-key binding is incomplete for package $package_name"
    centos_platform_assert_imported_rpm_key "$expected_fingerprint"
    assert_rpm_packaged_file "$file_path" "$package_name" "$expected_fingerprint"
}

centos_platform_assert_postgresql_clients() {
    [[ -x "$PLATFORM_PSQL" && ! -L "$PLATFORM_PSQL" &&
       -x "$PLATFORM_PG_ISREADY" && ! -L "$PLATFORM_PG_ISREADY" ]] ||
        die 'PostgreSQL client commands must use the reviewed PGDG 14 paths under /usr/pgsql-14/bin'
    assert_platform_packaged_file "$PLATFORM_PSQL" "$PLATFORM_POSTGRES_CLIENT_PACKAGE"
    assert_platform_packaged_file "$PLATFORM_PG_ISREADY" "$PLATFORM_POSTGRES_CLIENT_PACKAGE"
}

centos_platform_preflight_security() {
    local selinux_mode='' enforce_value=''
    if command -v getenforce >/dev/null 2>&1; then
        selinux_mode="$(getenforce 2>/dev/null)" ||
            die 'could not determine the active SELinux mode'
    elif [[ -r /sys/fs/selinux/enforce ]]; then
        IFS= read -r enforce_value < /sys/fs/selinux/enforce ||
            die 'could not read the active SELinux enforcement state'
        case "$enforce_value" in
            1) selinux_mode=Enforcing ;;
            0) selinux_mode=Permissive ;;
            *) die 'the active SELinux enforcement state is invalid' ;;
        esac
    else
        selinux_mode=Disabled
    fi

    case "$selinux_mode" in
        Enforcing)
            die 'CentOS SELinux Enforcing support remains an unverified candidate; refusing before package, account, service, or permanent-path changes'
            ;;
        Permissive)
            warn 'SELinux is Permissive; this candidate may be used only for isolated compatibility testing and is not production support'
            ;;
        Disabled) ;;
        *) die "getenforce returned an unexpected mode: $selinux_mode" ;;
    esac
}

centos_platform_runtime_packages() {
    printf '%s\n' ca-certificates curl python3 certbot iproute util-linux procps-ng
}

centos_platform_write_repository() {
    local output="$1" repo_id="$2" repo_name="$3" base_url="$4"
    local key_path="$5" metadata_check="$6"
    [[ "$repo_id" =~ ^probe-[a-z0-9-]+$ && "$base_url" =~ ^https:// &&
       "$key_path" == /etc/pki/rpm-gpg/PROBE-PANEL-* &&
       ( "$metadata_check" == 0 || "$metadata_check" == 1 ) ]] ||
        die 'the CentOS managed-repository contract is invalid'
    {
        printf '[%s]\n' "$repo_id"
        printf 'name=%s\n' "$repo_name"
        printf 'baseurl=%s\n' "$base_url"
        printf 'enabled=0\n'
        printf 'gpgcheck=1\n'
        printf 'repo_gpgcheck=%s\n' "$metadata_check"
        printf 'gpgkey=file://%s\n' "$key_path"
        printf 'sslverify=1\n'
        printf 'skip_if_unavailable=0\n'
        [[ "$repo_id" != probe-pgdg14 ]] || printf 'module_hotfixes=1\n'
        printf '\n'
    } >> "$output"
}

centos_platform_assert_imported_rpm_key() {
    local fingerprint="$1" expected_key_id rpmdb_key
    [[ "$fingerprint" =~ ^[0-9A-F]{40}$ ]] ||
        die 'the imported RPM key fingerprint contract is invalid'
    [[ -n "$TEMP_ROOT" && "$TEMP_ROOT" == /var/tmp/probe-panel-bootstrap.* &&
       -d "$TEMP_ROOT" && ! -L "$TEMP_ROOT" ]] ||
        die 'a private bootstrap workspace is required for RPM key validation'
    expected_key_id="${fingerprint: -8}"
    expected_key_id="${expected_key_id,,}"
    rpmdb_key="$TEMP_ROOT/rpmdb-key-$expected_key_id.asc"
    rpm -q --qf '%{DESCRIPTION}\n' "gpg-pubkey-$expected_key_id" > "$rpmdb_key" 2>/dev/null ||
        die "rpmkeys did not import the expected RPM signing key $expected_key_id"
    assert_openpgp_primary_fingerprint "$rpmdb_key" "$fingerprint" ||
        die "the imported RPM signing key $expected_key_id does not match its pinned full fingerprint"
    rm -f -- "$rpmdb_key"
}

centos_platform_capture_postgresql_module_state() {
    local mode
    [[ "$CENTOS_MODULE_STATE_CAPTURED" -eq 0 ]] ||
        die 'the PostgreSQL module state was captured more than once'
    CENTOS_MODULE_STATE_SNAPSHOT="$TEMP_ROOT/postgresql.module.before"
    if [[ -e "$CENTOS_MODULE_STATE_PATH" || -L "$CENTOS_MODULE_STATE_PATH" ]]; then
        [[ -f "$CENTOS_MODULE_STATE_PATH" && ! -L "$CENTOS_MODULE_STATE_PATH" &&
           "$(stat -c '%u:%g' "$CENTOS_MODULE_STATE_PATH")" == 0:0 ]] ||
            die "$CENTOS_MODULE_STATE_PATH must be a root-owned regular file"
        mode="$(stat -c '%a' "$CENTOS_MODULE_STATE_PATH")"
        [[ "$mode" =~ ^[0-7]{3,4}$ ]] || die 'the PostgreSQL module-state mode is invalid'
        (( (8#$mode & 8#7022) == 0 )) ||
            die "$CENTOS_MODULE_STATE_PATH must not have special bits or be writable by group or other users"
        install -o root -g root -m 0600 -- "$CENTOS_MODULE_STATE_PATH" "$CENTOS_MODULE_STATE_SNAPSHOT"
        CENTOS_MODULE_STATE_EXISTED=1
        CENTOS_MODULE_STATE_MODE="$mode"
    else
        CENTOS_MODULE_STATE_EXISTED=0
        CENTOS_MODULE_STATE_MODE=''
    fi
    CENTOS_MODULE_STATE_CAPTURED=1
}

centos_platform_cleanup_package_state() {
    local current_sha
    [[ "$CENTOS_MODULE_STATE_CAPTURED" -eq 1 ]] || return 0
    [[ "$CENTOS_MODULE_MUTATION_STARTED" -eq 1 ]] || return 0
    if [[ ! -e "$CENTOS_MODULE_STATE_PATH" && ! -L "$CENTOS_MODULE_STATE_PATH" ]]; then
        [[ "$CENTOS_MODULE_STATE_EXISTED" -eq 0 ]] && return 0
        return 1
    fi
    [[ -f "$CENTOS_MODULE_STATE_PATH" && ! -L "$CENTOS_MODULE_STATE_PATH" &&
       "$(stat -c '%u:%g' "$CENTOS_MODULE_STATE_PATH" 2>/dev/null)" == 0:0 ]] || return 1
    current_sha="$(sha256sum "$CENTOS_MODULE_STATE_PATH" 2>/dev/null | awk '{print $1}')"
    if [[ -n "$CENTOS_MODULE_STATE_MUTATED_SHA" &&
          "$current_sha" != "$CENTOS_MODULE_STATE_MUTATED_SHA" ]]; then
        return 1
    fi
    if [[ "$CENTOS_MODULE_STATE_EXISTED" -eq 1 ]]; then
        [[ -f "$CENTOS_MODULE_STATE_SNAPSHOT" && ! -L "$CENTOS_MODULE_STATE_SNAPSHOT" &&
           "$CENTOS_MODULE_STATE_MODE" =~ ^[0-7]{3,4}$ ]] || return 1
        install -o root -g root -m "$CENTOS_MODULE_STATE_MODE" -- \
            "$CENTOS_MODULE_STATE_SNAPSHOT" "$CENTOS_MODULE_STATE_PATH" || return 1
    else
        rm -f -- "$CENTOS_MODULE_STATE_PATH" || return 1
    fi
    return 0
}

centos_platform_prepare_package_sources() {
    local rpm_arch platform_variant
    local unexpected_repo_entry
    local base_key_url base_key_sha epel_key_url epel_key_sha pgdg_key_name pgdg_key_sha
    local expected_base_fingerprint expected_epel_fingerprint expected_pgdg_fingerprint
    local base_key_download epel_key_download pgdg_key_download repo_candidate
    local baseos_url appstream_url builder_url epel_url pgdg_url
    [[ -n "$TEMP_ROOT" && "$TEMP_ROOT" == /var/tmp/probe-panel-bootstrap.* &&
       -d "$TEMP_ROOT" && ! -L "$TEMP_ROOT" ]] ||
        die 'a private bootstrap workspace is required before package-source preparation'
    if [[ "$PLATFORM_EOL" == 1 && "$ACCEPT_EOL" != 1 ]]; then
        die 'an EOL RPM package source cannot be prepared without --accept-eol'
    fi
    rpm_arch="$(rpm --eval '%{_arch}' 2>/dev/null)" || die 'could not determine the RPM architecture'
    case "$(uname -m 2>/dev/null):$rpm_arch" in
        x86_64:x86_64|aarch64:aarch64) ;;
        *) die "the managed CentOS RPM architecture is inconsistent (kernel $(uname -m 2>/dev/null || printf unknown), RPM $rpm_arch)" ;;
    esac
    platform_variant="$PLATFORM_ID:$rpm_arch"
    case "$platform_variant" in
        centos-linux-7-systemd:x86_64)
            base_key_url='https://www.centos.org/keys/RPM-GPG-KEY-CentOS-7'
            base_key_sha='8b48b04b336bd725b9e611c441c65456a4168083c4febc28e88828d8ec14827f'
            expected_base_fingerprint='6341AB2753D78A78A7C27BB124C6A8A7F4A80EB5'
            baseos_url='https://vault.centos.org/7.9.2009/os/x86_64/'
            appstream_url='https://vault.centos.org/7.9.2009/updates/x86_64/'
            builder_url='https://vault.centos.org/7.9.2009/extras/x86_64/'
            ;;
        centos-linux-7-systemd:aarch64)
            base_key_url='https://www.centos.org/keys/RPM-GPG-KEY-CentOS-7-aarch64'
            base_key_sha='a771c9556de54a8eb6e3b39d56f8e76a67413b05819159dd871b9e1ab37732b6'
            expected_base_fingerprint='EF8F3CA66EFDF32B36CDADF76C7CB6EF305D49D6'
            baseos_url='https://vault.centos.org/altarch/7.9.2009/os/aarch64/'
            appstream_url='https://vault.centos.org/altarch/7.9.2009/updates/aarch64/'
            builder_url='https://vault.centos.org/altarch/7.9.2009/extras/aarch64/'
            ;;
        centos-linux-8-systemd:*)
            baseos_url="https://vault.centos.org/8.5.2111/BaseOS/$rpm_arch/os/"
            appstream_url="https://vault.centos.org/8.5.2111/AppStream/$rpm_arch/os/"
            builder_url="https://vault.centos.org/8.5.2111/PowerTools/$rpm_arch/os/"
            ;;
        centos-stream-8-systemd:*)
            baseos_url="https://vault.centos.org/8-stream/BaseOS/$rpm_arch/os/"
            appstream_url="https://vault.centos.org/8-stream/AppStream/$rpm_arch/os/"
            builder_url="https://vault.centos.org/8-stream/PowerTools/$rpm_arch/os/"
            ;;
        centos-stream-9-systemd:*)
            baseos_url="https://mirror.stream.centos.org/9-stream/BaseOS/$rpm_arch/os/"
            appstream_url="https://mirror.stream.centos.org/9-stream/AppStream/$rpm_arch/os/"
            builder_url="https://mirror.stream.centos.org/9-stream/CRB/$rpm_arch/os/"
            ;;
        centos-stream-10-systemd:*)
            baseos_url="https://mirror.stream.centos.org/10-stream/BaseOS/$rpm_arch/os/"
            appstream_url="https://mirror.stream.centos.org/10-stream/AppStream/$rpm_arch/os/"
            builder_url="https://mirror.stream.centos.org/10-stream/CRB/$rpm_arch/os/"
            ;;
        *) die "the managed CentOS repository layout is unavailable for $platform_variant" ;;
    esac

    if [[ "$PLATFORM_RPM_EL_MAJOR" != 7 ]]; then
        base_key_url='https://www.centos.org/keys/RPM-GPG-KEY-CentOS-Official'
        base_key_sha='146059788b214d7ba0dd70c1cf21111e594c6cfde201da8a9a88fe7101be8a78'
        expected_base_fingerprint='99DB70FAE1D7CE227FB6488205B555B38483C65D'
    fi

    case "$PLATFORM_RPM_EL_MAJOR" in
        7)
            epel_key_sha='028b9accc59bab1d21f2f3f544df5469910581e728a64fd8c411a725a82300c2'
            expected_epel_fingerprint='91E97D7C4A5E96F17F3E888F6A2FAEA2352C64E5'
            epel_url="https://dl.fedoraproject.org/pub/archive/epel/7/$rpm_arch/"
            ;;
        8)
            epel_key_sha='cd1db21a863185127f2e3b264c97fb1c6c44c316385707999041ea475c110d1c'
            expected_epel_fingerprint='94E279EB8D8F25B21810ADF121EA45AB2F86D6A1'
            case "$PLATFORM_ID" in
                centos-linux-8-systemd)
                    epel_url="https://dl.fedoraproject.org/pub/archive/epel/8.5/Everything/$rpm_arch/"
                    ;;
                centos-stream-8-systemd)
                    epel_url="https://dl.fedoraproject.org/pub/archive/epel/8.9/Everything/$rpm_arch/"
                    ;;
                *) die 'the EL8 EPEL snapshot contract is unavailable' ;;
            esac
            ;;
        9)
            epel_key_sha='fcf0eab4f05a1c0de6363ac4b707600a27a9d774e9b491059e59e6921b255a84'
            expected_epel_fingerprint='FF8AD1344597106ECE813B918A3872BF3228467C'
            epel_url="https://dl.fedoraproject.org/pub/epel/9/Everything/$rpm_arch/"
            ;;
        10)
            epel_key_sha='de390fc168eae5ab2852e9e93d34a0b9ddf05cf9ce90ee28d97de26a4b1f6b93'
            expected_epel_fingerprint='7D8D15CBFC4E62688591FB2633D98517E37ED158'
            epel_url="https://dl.fedoraproject.org/pub/epel/10/Everything/$rpm_arch/"
            ;;
        *) die 'the managed EPEL source generation is unavailable' ;;
    esac
    epel_key_url="https://dl.fedoraproject.org/pub/epel/RPM-GPG-KEY-EPEL-$PLATFORM_RPM_EL_MAJOR"

    case "$PLATFORM_RPM_EL_MAJOR:$rpm_arch" in
        7:x86_64)
            pgdg_key_name=PGDG-RPM-GPG-KEY-RHEL7
            pgdg_key_sha=a18e7cea1aa78189e36f28ca9ebf293826a407a923a75ab4a6ecbc9ad5217f49
            expected_pgdg_fingerprint=F245F0BF96AC182744CAFF2E64FACE1173E3B907
            ;;
        7:aarch64)
            pgdg_key_name=PGDG-RPM-GPG-KEY-AARCH64-RHEL7
            pgdg_key_sha=905462fba9a7755554e3762e93a5e728ba5607e6500a4b212d4eb338a9fa2c8d
            expected_pgdg_fingerprint=C78CD9E6DA3E1F5B5B16FC1A9FCD879F55B374B8
            ;;
        8:x86_64|9:x86_64|10:x86_64)
            pgdg_key_name=PGDG-RPM-GPG-KEY-RHEL
            pgdg_key_sha=a70c9527426017d00fa4e6f9d2941d515357a27a7be82e155248ece53bbe5453
            expected_pgdg_fingerprint=D4BF08AE67A0B4C7A1DBCCD240BCA2B408B40D20
            ;;
        8:aarch64|9:aarch64|10:aarch64)
            pgdg_key_name=PGDG-RPM-GPG-KEY-AARCH64-RHEL
            pgdg_key_sha=cc506fa92aa97e8e58f88551a2ec99a61d9d603f7f2c1ae0c06191f58c29979f
            expected_pgdg_fingerprint=B031F89FC983E98262906B6E177B343BB9738825
            ;;
        *) die "the PGDG RPM source does not support EL ${PLATFORM_RPM_EL_MAJOR:-unknown} architecture $rpm_arch" ;;
    esac
    [[ "$CENTOS_BASE_KEY_FINGERPRINT" == "$expected_base_fingerprint" &&
       "$CENTOS_EPEL_KEY_FINGERPRINT" == "$expected_epel_fingerprint" &&
       "$CENTOS_PGDG_KEY_FINGERPRINT" == "$expected_pgdg_fingerprint" ]] ||
        die 'the preflight and package-source signing contracts disagree'
    pgdg_url="https://download.postgresql.org/pub/repos/yum/14/redhat/rhel-${PLATFORM_RPM_EL_MAJOR}-$rpm_arch"

    ensure_package_source_directory /etc/pki/rpm-gpg
    ensure_package_source_directory /etc/yum.repos.d
    ensure_package_source_directory "$CENTOS_MANAGED_REPO_DIR"
    unexpected_repo_entry="$(find "$CENTOS_MANAGED_REPO_DIR" -mindepth 1 -maxdepth 1 \
        ! -name 'probe-panel-runtime.repo' -print -quit)" ||
        die 'the isolated CentOS repository directory could not be inspected'
    [[ -z "$unexpected_repo_entry" ]] ||
        die "the isolated CentOS repository directory contains an unmanaged entry: $unexpected_repo_entry"
    if [[ "$PLATFORM_RPM_EL_MAJOR" == 8 || "$PLATFORM_RPM_EL_MAJOR" == 9 ]]; then
        ensure_package_source_directory /etc/dnf/modules.d
    fi
    base_key_download="$TEMP_ROOT/centos-signing-key.asc"
    epel_key_download="$TEMP_ROOT/epel-signing-key.asc"
    pgdg_key_download="$TEMP_ROOT/$pgdg_key_name"
    download_fixed_openpgp_key \
        "$base_key_url" "$base_key_sha" "$CENTOS_BASE_KEY_FINGERPRINT" "$base_key_download"
    download_fixed_openpgp_key \
        "$epel_key_url" "$epel_key_sha" "$CENTOS_EPEL_KEY_FINGERPRINT" "$epel_key_download"
    download_fixed_openpgp_key \
        "https://download.postgresql.org/pub/repos/yum/keys/$pgdg_key_name" \
        "$pgdg_key_sha" "$CENTOS_PGDG_KEY_FINGERPRINT" "$pgdg_key_download"
    install_managed_package_source "$base_key_download" "$CENTOS_BASE_KEY_PATH" 644
    install_managed_package_source "$epel_key_download" "$CENTOS_EPEL_KEY_PATH" 644
    install_managed_package_source "$pgdg_key_download" "$PGDG_RPM_KEY_PATH" 644

    repo_candidate="$TEMP_ROOT/probe-panel-runtime.repo"
    : > "$repo_candidate"
    centos_platform_write_repository "$repo_candidate" probe-centos-baseos \
        'Probe Panel pinned CentOS BaseOS' "$baseos_url" "$CENTOS_BASE_KEY_PATH" 1
    centos_platform_write_repository "$repo_candidate" probe-centos-appstream \
        'Probe Panel pinned CentOS AppStream or Updates' "$appstream_url" "$CENTOS_BASE_KEY_PATH" 1
    centos_platform_write_repository "$repo_candidate" probe-centos-builder \
        'Probe Panel pinned CentOS PowerTools, CRB or Extras' "$builder_url" "$CENTOS_BASE_KEY_PATH" 1
    # Fedora EPEL does not publish detached repomd.xml signatures. Its metadata
    # remains TLS-protected and every RPM is still required to match the exact
    # EPEL key pinned above; repo_gpgcheck must not be falsely enabled here.
    centos_platform_write_repository "$repo_candidate" probe-epel \
        'Probe Panel pinned EPEL' "$epel_url" "$CENTOS_EPEL_KEY_PATH" 0
    centos_platform_write_repository "$repo_candidate" probe-pgdg14 \
        "Probe Panel pinned PostgreSQL 14 for EL $PLATFORM_RPM_EL_MAJOR" \
        "$pgdg_url" "$PGDG_RPM_KEY_PATH" 1
    chmod 0644 "$repo_candidate"
    install_managed_package_source "$repo_candidate" "$CENTOS_MANAGED_REPO_PATH" 644
    # Importing keys mutates the RPM database. Commit the complete isolated
    # repository/key set first so every consumed package retains its immutable
    # update source if a later operation fails.
    PACKAGE_SOURCE_CONSUMED=1
    rpmkeys --import "$CENTOS_BASE_KEY_PATH" || die 'rpmkeys could not import the pinned CentOS signing key'
    rpmkeys --import "$CENTOS_EPEL_KEY_PATH" || die 'rpmkeys could not import the pinned EPEL signing key'
    rpmkeys --import "$PGDG_RPM_KEY_PATH" || die 'rpmkeys could not import the pinned PGDG signing key'
    centos_platform_assert_imported_rpm_key "$CENTOS_BASE_KEY_FINGERPRINT"
    centos_platform_assert_imported_rpm_key "$CENTOS_EPEL_KEY_FINGERPRINT"
    centos_platform_assert_imported_rpm_key "$CENTOS_PGDG_KEY_FINGERPRINT"
}

centos_platform_install_packages() {
    local -a repository_options=(
        --noplugins
        "--setopt=reposdir=$CENTOS_MANAGED_REPO_DIR"
        --disablerepo='*'
        "--enablerepo=$CENTOS_REPO_ALLOWLIST"
    )
    if [[ "$PLATFORM_PACKAGE_MANAGER" == dnf ]]; then
        dnf "${repository_options[@]}" --setopt=gpgcheck=True makecache
        if [[ "$PLATFORM_RPM_EL_MAJOR" == 8 || "$PLATFORM_RPM_EL_MAJOR" == 9 ]]; then
            centos_platform_capture_postgresql_module_state
            PACKAGE_SOURCE_CONSUMED=1
            CENTOS_MODULE_MUTATION_STARTED=1
            dnf "${repository_options[@]}" --setopt=gpgcheck=True module disable -y postgresql
            [[ -f "$CENTOS_MODULE_STATE_PATH" && ! -L "$CENTOS_MODULE_STATE_PATH" &&
               "$(stat -c '%u:%g' "$CENTOS_MODULE_STATE_PATH")" == 0:0 ]] ||
                die 'dnf did not create a safe PostgreSQL module-state file'
            CENTOS_MODULE_STATE_MUTATED_SHA="$(sha256sum "$CENTOS_MODULE_STATE_PATH" | awk '{print $1}')"
            [[ "$CENTOS_MODULE_STATE_MUTATED_SHA" =~ ^[0-9a-f]{64}$ ]] ||
                die 'the disabled PostgreSQL module state could not be hashed'
        fi
        PACKAGE_SOURCE_CONSUMED=1
        dnf "${repository_options[@]}" install -y --setopt=install_weak_deps=False \
            --setopt=gpgcheck=True --setopt=keepcache=True "$@"
    elif [[ "$PLATFORM_PACKAGE_MANAGER" == yum ]]; then
        yum "${repository_options[@]}" --setopt=gpgcheck=1 makecache
        PACKAGE_SOURCE_CONSUMED=1
        yum "${repository_options[@]}" install -y --setopt=gpgcheck=1 --setopt=keepcache=1 "$@"
    else
        die 'the CentOS package-manager contract is unavailable'
    fi
}

centos_platform_initialize_postgresql() {
    local preexisting="$1"
    [[ "$preexisting" == 0 || "$preexisting" == 1 ]] ||
        die 'the PostgreSQL preexistence state is invalid'
    [[ "$preexisting" == 0 ]] || return 0

    local pg_data_root=/var/lib/pgsql/14/data
    local pg_setup=/usr/pgsql-14/bin/postgresql-14-setup
    [[ -x "$pg_setup" && ! -L "$pg_setup" ]] ||
        die 'the reviewed PGDG 14 cluster initializer is missing'
    assert_platform_packaged_file "$pg_setup" "$PLATFORM_POSTGRES_SERVER_PACKAGE"
    if [[ ! -f "$pg_data_root/PG_VERSION" ]]; then
        if [[ -d "$pg_data_root" ]] && find "$pg_data_root" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
            die "$pg_data_root is a partial PostgreSQL cluster; refusing to initialize over it"
        fi
        "$pg_setup" initdb
    fi
    [[ -f "$pg_data_root/PG_VERSION" && ! -L "$pg_data_root/PG_VERSION" &&
       "$(tr -d '[:space:]' < "$pg_data_root/PG_VERSION")" == 14 ]] ||
        die 'the initialized PGDG cluster is not PostgreSQL 14'
}

centos_platform_create_service_account() {
    require_command groupadd
    require_command useradd
    groupadd --system probe-api
    useradd --system --gid probe-api --home-dir /nonexistent --no-create-home \
        --shell /sbin/nologin probe-api
}

centos_platform_validate_nologin_shell() {
    [[ "$1" == /sbin/nologin || "$1" == /usr/sbin/nologin ]]
}

centos_platform_disable_default_nginx_site() {
    :
}

# --- END GENERATED PLATFORM ADAPTER: centos ---

if :; then main "$@" 'probe-panel-bootstrap-complete-v1'; fi
