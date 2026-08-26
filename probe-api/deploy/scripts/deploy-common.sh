#!/usr/bin/env bash

# Shared production deployment helpers. Historical full-profile entrypoints
# remain pinned to Debian 13; the management runtime uses the exact platform
# adapter below.
# This file is sourced by install.sh, upgrade.sh, and validate-production.sh.

set -Eeuo pipefail
umask 077

readonly PROBE_ROOT="/srv/probe"
readonly PROBE_API_DIR="${PROBE_ROOT}/api"
readonly PROBE_CONFIG_DIR="${PROBE_ROOT}/config"
readonly PROBE_NGINX_CONFIG_DIR="${PROBE_CONFIG_DIR}/nginx"
readonly PROBE_RELEASES_DIR="${PROBE_ROOT}/releases"
readonly PROBE_BACKUPS_DIR="${PROBE_ROOT}/backups"
readonly PROBE_BACKUP_SCRIPT_DIR="${PROBE_API_DIR}/scripts"
readonly PROBE_POSTGRES_BACKUP_DIR="/var/backups/probe-panel/postgres"
readonly PROBE_ENV_FILE="${PROBE_CONFIG_DIR}/probe-api.env"
readonly PROBE_BACKUP_ENV_FILE="${PROBE_CONFIG_DIR}/probe-postgres-backup.env"
readonly PROBE_PGPASS_FILE="${PROBE_CONFIG_DIR}/probe-postgres.pgpass"
readonly PROBE_ALLOWLIST_FILE="/etc/probe-panel/admin-allowlist.geo"
readonly PROBE_ACTIVE_NGINX_CONFIG="${PROBE_NGINX_CONFIG_DIR}/nginx.conf"
readonly PROBE_NGINX_LINK="/etc/nginx/conf.d/probe-panel.conf"
readonly PROBE_PRIVATE_CA_FILE="/etc/probe-panel/tls/private-ca/ca.pem"
readonly PROBE_SYSTEMD_UNIT="/etc/systemd/system/probe-api.service"
readonly PROBE_BACKUP_SERVICE_UNIT="/etc/systemd/system/probe-postgres-backup.service"
readonly PROBE_BACKUP_TIMER_UNIT="/etc/systemd/system/probe-postgres-backup.timer"
readonly PROBE_MANAGEMENT_LIB_DIR="/usr/local/lib/probe-panel"
readonly PROBE_MANAGEMENT_DEPLOY_COMMON="${PROBE_MANAGEMENT_LIB_DIR}/deploy-common.sh"
readonly PROBE_MANAGEMENT_VALIDATE="${PROBE_MANAGEMENT_LIB_DIR}/validate-management.sh"
readonly PROBE_MANAGEMENT_RESTORE="${PROBE_MANAGEMENT_LIB_DIR}/restore-management.sh"
readonly PROBE_MANAGEMENT_UNINSTALL="${PROBE_MANAGEMENT_LIB_DIR}/uninstall-management.sh"
readonly PROBE_MANAGEMENT_LIFECYCLE_MANIFEST="${PROBE_MANAGEMENT_LIB_DIR}/management-lifecycle.sha256"
readonly PROBE_API_WANTS_LINK="/etc/systemd/system/multi-user.target.wants/probe-api.service"
readonly PROBE_BACKUP_TIMER_WANTS_LINK="/etc/systemd/system/timers.target.wants/probe-postgres-backup.timer"
readonly PROBE_NGINX_WANTS_LINK="/etc/systemd/system/multi-user.target.wants/nginx.service"
# Referenced by install.sh and upgrade.sh after this shared file is sourced.
# shellcheck disable=SC2034
readonly PROBE_DEPLOY_LOCK="/run/lock/probe-panel-deploy.lock"
readonly PROBE_MANAGEMENT_RUNTIME_ABI="probe-linux-systemd-v2"
readonly PROBE_MANAGEMENT_PLATFORM_IDS="debian-9-systemd,debian-10-systemd,debian-11-systemd,debian-12-systemd,debian-13-systemd,ubuntu-18.04-systemd,ubuntu-20.04-systemd,ubuntu-22.04-systemd,ubuntu-24.04-systemd,ubuntu-26.04-systemd,centos-linux-7-systemd,centos-linux-8-systemd,centos-stream-8-systemd,centos-stream-9-systemd,centos-stream-10-systemd"

PROBE_DEPLOY_WORK_ROOT=""
PROBE_VALIDATED_PGHOST=""
PROBE_VALIDATED_PGPORT=""
PROBE_VALIDATED_PGDATABASE=""
PROBE_VALIDATED_PGUSER=""
PROBE_VALIDATED_PGPASSFILE=""
RUNTIME_PLATFORM_ID=""
RUNTIME_SYSTEMD_PROFILE=""
RUNTIME_POSTGRES_SERVICE=""
RUNTIME_CERTBOT_TIMER=""
SERVICE_ASSET_SNAPSHOT_PENDING=""
STAGED_RELEASE_DIR=""
STAGED_RELEASE_INCOMING_PENDING=""
MANAGEMENT_API_WAS_ACTIVE=""
MANAGEMENT_BACKUP_TIMER_WAS_ACTIVE=""
MANAGEMENT_NGINX_WAS_ACTIVE=""
MANAGEMENT_POSTGRES_WAS_ACTIVE=""
MANAGEMENT_ACTIVATION_ROLLBACK_STATE="none"
MANAGEMENT_ROLLBACK_RELEASE_PROFILE=""
MANAGEMENT_ROLLBACK_OLD_API=""
MANAGEMENT_ROLLBACK_OLD_AGENT=""
MANAGEMENT_ROLLBACK_OLD_WEB=""
MANAGEMENT_ROLLBACK_OLD_ADMIN=""
MANAGEMENT_ROLLBACK_OLD_MIGRATIONS=""
MANAGEMENT_ROLLBACK_SERVICE_ASSET_SNAPSHOT=""

cleanup_deploy_work_root() {
    local status=$?
    trap - EXIT
    trap '' HUP INT TERM
    if (( status != 0 )); then
        rollback_pending_management_activation
    fi
    if [[ -n "${STAGED_RELEASE_INCOMING_PENDING:-}" &&
          -d "$STAGED_RELEASE_INCOMING_PENDING" &&
          ! -L "$STAGED_RELEASE_INCOMING_PENDING" ]]; then
        case "$STAGED_RELEASE_INCOMING_PENDING" in
            "$PROBE_RELEASES_DIR"/.incoming-*)
                rm -rf -- "$STAGED_RELEASE_INCOMING_PENDING" ||
                    warn "could not clean staged release: $STAGED_RELEASE_INCOMING_PENDING"
                ;;
            *) warn "refusing to remove unexpected staged release: $STAGED_RELEASE_INCOMING_PENDING" ;;
        esac
    fi
    if [[ -n "$PROBE_DEPLOY_WORK_ROOT" && -d "$PROBE_DEPLOY_WORK_ROOT" &&
          ! -L "$PROBE_DEPLOY_WORK_ROOT" ]]; then
        case "$PROBE_DEPLOY_WORK_ROOT" in
            /var/tmp/probe-build.*|/var/tmp/probe-prebuilt-verify.*)
                rm -rf -- "$PROBE_DEPLOY_WORK_ROOT" ||
                    warn "could not clean deployment work root: $PROBE_DEPLOY_WORK_ROOT"
                ;;
            *) warn "refusing to remove unexpected temporary path: $PROBE_DEPLOY_WORK_ROOT" ;;
        esac
    fi
    exit "$status"
}
trap cleanup_deploy_work_root EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

log() {
    printf '[probe-deploy] %s\n' "$*" >&2
}

warn() {
    printf '[probe-deploy] WARNING: %s\n' "$*" >&2
}

die() {
    printf '[probe-deploy] ERROR: %s\n' "$*" >&2
    exit 1
}

require_root() {
    if [[ "$(id -u)" -ne 0 ]]; then
        die "run this command as root"
    fi
}

validate_management_platform_id() {
    case "$1" in
        debian-9-systemd|debian-10-systemd|debian-11-systemd|debian-12-systemd|debian-13-systemd|\
        ubuntu-18.04-systemd|ubuntu-20.04-systemd|ubuntu-22.04-systemd|ubuntu-24.04-systemd|ubuntu-26.04-systemd|\
        centos-linux-7-systemd|centos-linux-8-systemd|centos-stream-8-systemd|centos-stream-9-systemd|centos-stream-10-systemd) ;;
        *) die "unsupported management runtime platform: ${1:-missing}" ;;
    esac
}

parse_management_os_release_name() {
    local value="$1" length
    [[ -n "$value" ]] || die "NAME in os-release is empty"
    case "$value" in
        \"*)
            length=${#value}
            [[ "$length" -ge 2 && "${value: -1}" == '"' ]] ||
                die "NAME in os-release has unmatched double quotes"
            value="${value:1:length-2}"
            ;;
        \'*)
            length=${#value}
            [[ "$length" -ge 2 && "${value: -1}" == "'" ]] ||
                die "NAME in os-release has unmatched single quotes"
            value="${value:1:length-2}"
            ;;
        *\"*|*\'*) die "NAME in os-release has non-canonical quoting" ;;
    esac
    [[ "$value" =~ ^[A-Za-z0-9._/[:space:]-]+$ ]] ||
        die "NAME in os-release contains unsupported characters"
    printf '%s\n' "$value"
}

parse_management_os_release_token() {
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
        *\"*|*\'*) die "$key in os-release has non-canonical quoting" ;;
    esac
    [[ "$value" =~ ^[A-Za-z0-9._-]+$ ]] ||
        die "$key in os-release contains unsupported characters"
    printf '%s\n' "$value"
}

management_platform_id_from_release() {
    local os_id="$1" version_id="$2" os_name="${3-}"
    case "${os_id}:${version_id}:${os_name}" in
        debian:9:*) printf '%s\n' debian-9-systemd ;;
        debian:10:*) printf '%s\n' debian-10-systemd ;;
        debian:11:*) printf '%s\n' debian-11-systemd ;;
        debian:12:*) printf '%s\n' debian-12-systemd ;;
        debian:13:*) printf '%s\n' debian-13-systemd ;;
        ubuntu:18.04:*) printf '%s\n' ubuntu-18.04-systemd ;;
        ubuntu:20.04:*) printf '%s\n' ubuntu-20.04-systemd ;;
        ubuntu:22.04:*) printf '%s\n' ubuntu-22.04-systemd ;;
        ubuntu:24.04:*) printf '%s\n' ubuntu-24.04-systemd ;;
        ubuntu:26.04:*) printf '%s\n' ubuntu-26.04-systemd ;;
        centos:7:'CentOS Linux') printf '%s\n' centos-linux-7-systemd ;;
        centos:8:'CentOS Linux') printf '%s\n' centos-linux-8-systemd ;;
        centos:8:'CentOS Stream') printf '%s\n' centos-stream-8-systemd ;;
        centos:9:'CentOS Stream') printf '%s\n' centos-stream-9-systemd ;;
        centos:10:'CentOS Stream') printf '%s\n' centos-stream-10-systemd ;;
        *) die "management runtime does not support ${os_id:-unknown} ${version_id:-unknown} ${os_name:-unknown}" ;;
    esac
}

management_platform_nginx_dialect() {
    validate_management_platform_id "$1"
    case "$1" in
        debian-9-systemd|centos-linux-7-systemd) printf '%s\n' classic ;;
        debian-13-systemd|ubuntu-26.04-systemd|centos-stream-10-systemd) printf '%s\n' modern ;;
        *) printf '%s\n' legacy ;;
    esac
}

management_platform_systemd_profile() {
    validate_management_platform_id "$1"
    case "$1" in
        debian-9-systemd|debian-10-systemd|debian-11-systemd|ubuntu-18.04-systemd|ubuntu-20.04-systemd|\
        centos-linux-7-systemd|centos-linux-8-systemd|centos-stream-8-systemd) printf '%s\n' legacy ;;
        *) printf '%s\n' modern ;;
    esac
}

management_platform_systemd_minimum() {
    validate_management_platform_id "$1"
    case "$1" in
        centos-linux-7-systemd) printf '%s\n' 219 ;;
        debian-9-systemd) printf '%s\n' 232 ;;
        ubuntu-18.04-systemd) printf '%s\n' 237 ;;
        centos-linux-8-systemd|centos-stream-8-systemd) printf '%s\n' 239 ;;
        debian-10-systemd) printf '%s\n' 241 ;;
        ubuntu-20.04-systemd) printf '%s\n' 245 ;;
        debian-11-systemd) printf '%s\n' 247 ;;
        ubuntu-22.04-systemd) printf '%s\n' 249 ;;
        debian-12-systemd|centos-stream-9-systemd) printf '%s\n' 252 ;;
        ubuntu-24.04-systemd) printf '%s\n' 255 ;;
        debian-13-systemd|centos-stream-10-systemd) printf '%s\n' 257 ;;
        ubuntu-26.04-systemd) printf '%s\n' 259 ;;
    esac
}

management_platform_package_family() {
    validate_management_platform_id "$1"
    case "$1" in
        centos-*) printf '%s\n' rpm ;;
        *) printf '%s\n' deb ;;
    esac
}

management_platform_postgres_service() {
    validate_management_platform_id "$1"
    if [[ "$(management_platform_package_family "$1")" == rpm ]]; then
        printf '%s\n' postgresql-14.service
    else
        printf '%s\n' postgresql.service
    fi
}

management_platform_certbot_timer() {
    validate_management_platform_id "$1"
    if [[ "$(management_platform_package_family "$1")" == rpm ]]; then
        printf '%s\n' certbot-renew.timer
    else
        printf '%s\n' certbot.timer
    fi
}

management_platform_postgres_bin_dir() {
    validate_management_platform_id "$1"
    if [[ "$(management_platform_package_family "$1")" == rpm ]]; then
        printf '%s\n' /usr/pgsql-14/bin
    else
        printf '%s\n' /usr/bin
    fi
}

assert_runtime_platform_contract() {
    local platform_id="${RUNTIME_PLATFORM_ID:-}"
    [[ -n "$platform_id" ]] || die "runtime platform contract is not initialized"
    validate_management_platform_id "$platform_id"

    local expected_systemd_profile expected_postgres_service expected_certbot_timer
    expected_systemd_profile="$(management_platform_systemd_profile "$platform_id")"
    expected_postgres_service="$(management_platform_postgres_service "$platform_id")"
    expected_certbot_timer="$(management_platform_certbot_timer "$platform_id")"
    [[ -n "${RUNTIME_SYSTEMD_PROFILE:-}" &&
       "$RUNTIME_SYSTEMD_PROFILE" == "$expected_systemd_profile" ]] ||
        die "runtime systemd profile is uninitialized or inconsistent with $platform_id"
    [[ -n "${RUNTIME_POSTGRES_SERVICE:-}" &&
       "$RUNTIME_POSTGRES_SERVICE" == "$expected_postgres_service" ]] ||
        die "runtime PostgreSQL service is uninitialized or inconsistent with $platform_id"
    [[ -n "${RUNTIME_CERTBOT_TIMER:-}" &&
       "$RUNTIME_CERTBOT_TIMER" == "$expected_certbot_timer" ]] ||
        die "runtime Certbot timer is uninitialized or inconsistent with $platform_id"
}

initialize_runtime_platform_contract() {
    local platform_id="${1:-}"
    validate_management_platform_id "$platform_id"

    if [[ -n "${RUNTIME_PLATFORM_ID:-}${RUNTIME_SYSTEMD_PROFILE:-}${RUNTIME_POSTGRES_SERVICE:-}${RUNTIME_CERTBOT_TIMER:-}" ]]; then
        assert_runtime_platform_contract
        [[ "$RUNTIME_PLATFORM_ID" == "$platform_id" ]] ||
            die "runtime platform contract is already initialized for $RUNTIME_PLATFORM_ID"
        return 0
    fi

    RUNTIME_PLATFORM_ID="$platform_id"
    RUNTIME_SYSTEMD_PROFILE="$(management_platform_systemd_profile "$platform_id")"
    RUNTIME_POSTGRES_SERVICE="$(management_platform_postgres_service "$platform_id")"
    RUNTIME_CERTBOT_TIMER="$(management_platform_certbot_timer "$platform_id")"
    assert_runtime_platform_contract
}

runtime_platform_id() {
    assert_runtime_platform_contract
    printf '%s\n' "$RUNTIME_PLATFORM_ID"
}

runtime_systemd_profile() {
    assert_runtime_platform_contract
    printf '%s\n' "$RUNTIME_SYSTEMD_PROFILE"
}

runtime_postgres_service() {
    assert_runtime_platform_contract
    printf '%s\n' "$RUNTIME_POSTGRES_SERVICE"
}

runtime_certbot_timer() {
    assert_runtime_platform_contract
    printf '%s\n' "$RUNTIME_CERTBOT_TIMER"
}

runtime_account_family() {
    management_platform_package_family "$(runtime_platform_id)"
}

runtime_postgres_command() {
    local command_name="${1:-}" bin_dir
    assert_runtime_platform_contract || return "$?"
    case "$command_name" in
        pg_dump|pg_restore|psql) ;;
        *) die "unsupported PostgreSQL runtime command: ${command_name:-missing}" ;;
    esac
    bin_dir="$(management_platform_postgres_bin_dir "$RUNTIME_PLATFORM_ID")" || return "$?"
    printf '%s/%s\n' "$bin_dir" "$command_name"
}

require_runtime_postgres_commands() {
    local command_name command_path
    for command_name in pg_dump pg_restore psql; do
        command_path="$(runtime_postgres_command "$command_name")"
        [[ -x "$command_path" ]] ||
            die "required platform PostgreSQL command is missing or not executable: $command_path"
    done
}

management_platform_id_from_os_release() {
    local os_release="$1"
    [[ -n "$os_release" ]] || die "os-release path is required"
    # os-release(5) permits /etc/os-release to be a relative symlink to the
    # vendor file under /usr/lib, as used by accepted candidate distributions.
    [[ -r "$os_release" && -f "$os_release" ]] ||
        die "$os_release must resolve to a readable regular file"
    local os_id="" version_id="" os_name="" line raw_value id_count=0 version_count=0 name_count=0
    while IFS= read -r line || [[ -n "$line" ]]; do
        case "$line" in
            ''|'#'*) continue ;;
            ID=*)
                ((id_count += 1))
                raw_value="${line#ID=}"
                os_id="$(parse_management_os_release_token ID "$raw_value")"
                ;;
            VERSION_ID=*)
                ((version_count += 1))
                raw_value="${line#VERSION_ID=}"
                version_id="$(parse_management_os_release_token VERSION_ID "$raw_value")"
                ;;
            NAME=*)
                ((name_count += 1))
                raw_value="${line#NAME=}"
                os_name="$(parse_management_os_release_name "$raw_value")"
                ;;
        esac
    done < "$os_release"
    [[ "$id_count" -eq 1 && "$version_count" -eq 1 ]] ||
        die "$os_release must define ID and VERSION_ID exactly once"

    (( name_count <= 1 )) || die "$os_release must define NAME at most once"
    management_platform_id_from_release "$os_id" "$version_id" "$os_name"
}

require_supported_runtime_platform() {
    local detected_platform_id
    detected_platform_id="$(management_platform_id_from_os_release /etc/os-release)"

    command -v systemctl >/dev/null 2>&1 || die "systemctl is required by $PROBE_MANAGEMENT_RUNTIME_ABI"
    command -v ps >/dev/null 2>&1 || die "ps is required by $PROBE_MANAGEMENT_RUNTIME_ABI"
    [[ -d /run/systemd/system && ! -L /run/systemd/system ]] ||
        die "PID 1 systemd is required by $PROBE_MANAGEMENT_RUNTIME_ABI"
    local pid_one systemd_version systemd_minimum package_family
    pid_one="$(ps -p 1 -o comm= 2>/dev/null || :)"
    pid_one="${pid_one//[[:space:]]/}"
    [[ "$pid_one" == systemd ]] || die "PID 1 must be systemd (found ${pid_one:-unknown})"
    systemd_version="$(systemctl --version 2>/dev/null | awk 'NR == 1 && $1 == "systemd" && $2 ~ /^[0-9]+$/ { print $2 }')"
    systemd_minimum="$(management_platform_systemd_minimum "$detected_platform_id")"
    [[ "$systemd_version" =~ ^[0-9]+$ && "$systemd_minimum" =~ ^[0-9]+$ &&
       "$systemd_version" -ge "$systemd_minimum" ]] ||
        die "platform $detected_platform_id requires systemd $systemd_minimum or newer (found ${systemd_version:-unknown})"
    initialize_runtime_platform_contract "$detected_platform_id"
    package_family="$(runtime_account_family)"
    if [[ "$package_family" == rpm ]]; then
        [[ -d /usr/pgsql-14/bin && ! -L /usr/pgsql-14/bin ]] ||
            die "RPM management runtime requires the reviewed PGDG 14 command directory"
        PATH="/usr/pgsql-14/bin:$PATH"
        export PATH
    fi
}

require_debian_13() {
    [[ -r /etc/os-release ]] || die "/etc/os-release is missing"

    local os_id="" version_id=""
    while IFS='=' read -r key value; do
        value="${value%\"}"
        value="${value#\"}"
        case "$key" in
            ID) os_id="$value" ;;
            VERSION_ID) version_id="$value" ;;
        esac
    done < /etc/os-release

    [[ "$os_id" == "debian" && "$version_id" == "13" ]] ||
        die "production scripts support Debian 13 only (found ${os_id:-unknown} ${version_id:-unknown})"
    initialize_runtime_platform_contract debian-13-systemd
}

require_commands() {
    local command_name
    for command_name in "$@"; do
        command -v "$command_name" >/dev/null 2>&1 || die "required command is missing: $command_name"
    done
}

acquire_root_lock() {
    local lock_file="$1" lock_root lock_root_mode path_identity descriptor_identity
    [[ "$lock_file" == /* && "$lock_file" != */ && "$lock_file" != *[[:space:]]* ]] ||
        die "deployment lock path must be one absolute file path"
    lock_root="${lock_file%/*}"
    [[ -n "$lock_root" && -d "$lock_root" && ! -L "$lock_root" ]] ||
        die "deployment lock parent must be a real directory: $lock_root"
    [[ "$(stat -c '%u:%g' "$lock_root")" == 0:0 ]] ||
        die "deployment lock parent must be owned by root:root: $lock_root"
    lock_root_mode="$(stat -c '%a' "$lock_root")"
    [[ "$lock_root_mode" =~ ^[0-7]{3,4}$ ]] ||
        die "deployment lock parent has an invalid mode: $lock_root"
    if [[ "$lock_root_mode" != 1777 ]]; then
        (( (8#$lock_root_mode & 0022) == 0 )) ||
            die "deployment lock parent must be root:root mode 1777 with sticky bit, or must not be group/world-writable: $lock_root"
    fi

    if [[ -L "$lock_file" || ( -e "$lock_file" && ! -f "$lock_file" ) ]]; then
        die "deployment lock must be a real regular file: $lock_file"
    fi
    if [[ ! -e "$lock_file" ]]; then
        # Debian's /run/lock is a sticky shared directory. noclobber performs
        # an exclusive create so a competing symlink or file is never followed
        # or replaced; the winner is validated below before it is opened.
        (umask 077; set -o noclobber; : > "$lock_file") 2>/dev/null || :
    fi
    [[ -f "$lock_file" && ! -L "$lock_file" ]] ||
        die "deployment lock could not be created as a real regular file: $lock_file"
    [[ "$(stat -c '%u:%g:%a' "$lock_file")" == 0:0:600 ]] ||
        die "deployment lock must be root:root mode 0600: $lock_file"

    exec 9>>"$lock_file"
    [[ -f "$lock_file" && ! -L "$lock_file" ]] ||
        die "deployment lock changed while it was being opened: $lock_file"
    path_identity="$(stat -c '%d:%i' "$lock_file")"
    descriptor_identity="$(stat -Lc '%d:%i' /proc/self/fd/9)"
    [[ "$path_identity" == "$descriptor_identity" ]] ||
        die "deployment lock changed while it was being opened: $lock_file"
    flock --exclusive --nonblock 9 || die "another Probe deployment is in progress"
}

acquire_deployment_lock() {
    acquire_root_lock "$PROBE_DEPLOY_LOCK"
}

login_defs_number() {
    local key="$1" fallback="${2-}" value
    [[ -z "$fallback" || "$fallback" =~ ^[0-9]+$ ]] ||
        die "the internal default for $key must be numeric"
    [[ -f /etc/login.defs && ! -L /etc/login.defs ]] ||
        die "/etc/login.defs must be a regular file, not a symbolic link"
    [[ "$(stat -c '%u:%g' /etc/login.defs)" == 0:0 ]] ||
        die "/etc/login.defs must be owned by root:root"
    local mode
    mode="$(stat -c '%a' /etc/login.defs)"
    [[ "$mode" =~ ^[0-7]{3,4}$ ]] || die "/etc/login.defs has an invalid mode"
    (( (8#$mode & 0022) == 0 )) ||
        die "/etc/login.defs must not be writable by group or other users"

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
    require_commands awk getent id stat

    getent passwd root >/dev/null 2>&1 || die "the system account database is unavailable"
    getent group root >/dev/null 2>&1 || die "the system group database is unavailable"

    local passwd_name_count group_name_count
    passwd_name_count="$(getent passwd | awk -F: '$1 == "probe-api" { count++ } END { print count + 0 }')" ||
        die "could not enumerate the system account database"
    group_name_count="$(getent group | awk -F: '$1 == "probe-api" { count++ } END { print count + 0 }')" ||
        die "could not enumerate the system group database"
    [[ "$passwd_name_count" == 1 && "$group_name_count" == 1 ]] ||
        die "probe-api must have exactly one passwd record and one same-name group record"

    local -a passwd_records group_records uid_records gid_records group_ids group_members
    mapfile -t passwd_records < <(getent passwd probe-api || :)
    mapfile -t group_records < <(getent group probe-api || :)
    (( ${#passwd_records[@]} == 1 )) ||
        die "probe-api must resolve to exactly one service account"
    (( ${#group_records[@]} == 1 )) ||
        die "probe-api must resolve to exactly one same-name primary group"

    local account_name account_uid account_gid account_home account_shell
    local group_name group_gid group_members_field
    [[ "${passwd_records[0]}" =~ ^([^:]*:){6}[^:]*$ ]] ||
        die "the probe-api passwd record is malformed"
    IFS=: read -r account_name _ account_uid account_gid _ account_home account_shell <<< "${passwd_records[0]}"
    [[ "$account_name" == probe-api && "$account_uid" =~ ^[0-9]+$ && "$account_gid" =~ ^[0-9]+$ ]] ||
        die "the probe-api passwd record is malformed"
    [[ "$account_home" == /nonexistent ]] ||
        die "probe-api must use /nonexistent as its home directory"
    local account_family
    account_family="$(runtime_account_family)"
    case "$account_family:$account_shell" in
        deb:/usr/sbin/nologin|rpm:/sbin/nologin|rpm:/usr/sbin/nologin) ;;
        *) die "probe-api must use the platform nologin shell as its shell" ;;
    esac

    local sys_uid_max_raw uid_min_raw sys_uid_max uid_min numeric_uid numeric_gid
    uid_min_raw="$(login_defs_number UID_MIN)"
    (( ${#uid_min_raw} <= 10 && ${#account_uid} <= 10 && ${#account_gid} <= 10 )) ||
        die "probe-api or login.defs contains an out-of-range numeric identifier"
    uid_min=$((10#$uid_min_raw))
    numeric_uid=$((10#$account_uid))
    numeric_gid=$((10#$account_gid))
    (( uid_min >= 2 && uid_min <= 4294967294 && numeric_uid <= 4294967294 && numeric_gid <= 4294967294 )) ||
        die "probe-api or login.defs contains an out-of-range numeric identifier"
    # login.defs(5) defines the omitted SYS_UID_MAX default as UID_MIN-1.
    # Reviewed distribution stock files rely on that default, so preserve it while still
    # rejecting duplicate, malformed, or explicitly unsafe overrides above.
    sys_uid_max_raw="$(login_defs_number SYS_UID_MAX "$((uid_min - 1))")"
    (( ${#sys_uid_max_raw} <= 10 )) ||
        die "probe-api or login.defs contains an out-of-range numeric identifier"
    sys_uid_max=$((10#$sys_uid_max_raw))
    (( sys_uid_max <= 4294967294 )) ||
        die "probe-api or login.defs contains an out-of-range numeric identifier"
    (( sys_uid_max >= 1 && uid_min > sys_uid_max )) ||
        die "/etc/login.defs SYS_UID_MAX must be below UID_MIN"
    (( numeric_uid >= 1 && numeric_uid <= sys_uid_max && numeric_uid < uid_min )) ||
        die "probe-api UID is outside the platform system-account range"

    [[ "${group_records[0]}" =~ ^([^:]*:){3}[^:]*$ ]] ||
        die "the probe-api group record is malformed"
    IFS=: read -r group_name _ group_gid group_members_field <<< "${group_records[0]}"
    [[ "$group_name" == probe-api && "$group_gid" =~ ^[0-9]+$ ]] ||
        die "the probe-api group record is malformed"
    (( 10#$group_gid == numeric_gid )) ||
        die "probe-api must use its same-name group as the primary group"

    mapfile -t uid_records < <(getent passwd "$numeric_uid" || :)
    if (( ${#uid_records[@]} != 1 )) || [[ "${uid_records[0]}" != "${passwd_records[0]}" ]]; then
        die "probe-api must have a unique UID"
    fi
    mapfile -t gid_records < <(getent group "$numeric_gid" || :)
    if (( ${#gid_records[@]} != 1 )) || [[ "${gid_records[0]}" != "${group_records[0]}" ]]; then
        die "the probe-api primary GID must belong to exactly one group"
    fi
    [[ "$(id -g probe-api)" == "$numeric_gid" && "$(id -gn probe-api)" == probe-api ]] ||
        die "probe-api must use its unique same-name primary group"

    local duplicate_uid_owner duplicate_gid_group group_id_output other_primary_user member seen_self=0
    duplicate_uid_owner="$(getent passwd | awk -F: -v wanted_uid="$numeric_uid" '
        $3 == wanted_uid && $1 != "probe-api" && other == "" { other = $1 }
        END { print other }
    ')" || die "could not enumerate accounts while proving the probe-api UID is unique"
    [[ -z "$duplicate_uid_owner" ]] ||
        die "the probe-api UID is also used by another account: $duplicate_uid_owner"
    duplicate_gid_group="$(getent group | awk -F: -v wanted_gid="$numeric_gid" '
        $3 == wanted_gid && $1 != "probe-api" && other == "" { other = $1 }
        END { print other }
    ')" || die "could not enumerate groups while proving the probe-api GID is unique"
    [[ -z "$duplicate_gid_group" ]] ||
        die "the probe-api primary GID is also used by another group: $duplicate_gid_group"

    group_id_output="$(id -G probe-api)" || die "could not enumerate probe-api group membership"
    read -r -a group_ids <<< "$group_id_output"
    if (( ${#group_ids[@]} != 1 )) || [[ "${group_ids[0]}" != "$numeric_gid" ]]; then
        die "probe-api must not have supplementary groups"
    fi

    if [[ -n "$group_members_field" ]]; then
        IFS=, read -r -a group_members <<< "$group_members_field"
        for member in "${group_members[@]}"; do
            [[ "$member" == probe-api && "$seen_self" -eq 0 ]] ||
                die "the probe-api group must not contain other or duplicate explicit members"
            seen_self=1
        done
    fi
    other_primary_user="$(getent passwd | awk -F: -v wanted_gid="$numeric_gid" '
        $4 == wanted_gid && $1 != "probe-api" && other == "" { other = $1 }
        END { print other }
    ')" || die "could not enumerate accounts that use the probe-api primary group"
    [[ -z "$other_primary_user" ]] ||
        die "the probe-api primary group is also used by another account: $other_primary_user"
}

prepare_probe_api_service_account() {
    local account_family
    account_family="$(runtime_account_family)"
    case "$account_family" in
        deb) require_commands addgroup adduser getent ;;
        rpm) require_commands groupadd useradd getent ;;
        *) die "unsupported account-creation platform family: $account_family" ;;
    esac

    local passwd_record group_record
    passwd_record="$(getent passwd probe-api || :)"
    group_record="$(getent group probe-api || :)"
    if [[ -z "$passwd_record" && -z "$group_record" ]]; then
        if [[ "$account_family" == deb ]]; then
            addgroup --system probe-api
            adduser --system --ingroup probe-api --no-create-home --home /nonexistent \
                --shell /usr/sbin/nologin probe-api
        else
            groupadd --system probe-api
            useradd --system --gid probe-api --home-dir /nonexistent --no-create-home \
                --shell /sbin/nologin probe-api
        fi
    elif [[ -z "$passwd_record" || -z "$group_record" ]]; then
        die "a partial probe-api service account or group already exists; refusing to repair it"
    fi
    assert_probe_api_service_account
}

run_as_probe_api_no_environment() {
    (($# > 0)) || die "run_as_probe_api_no_environment requires a command"
    local -a drop_capability_arguments=(--inh-caps=-all)
    if /usr/bin/setpriv --help 2>&1 | grep -Fq -- '--ambient-caps'; then
        drop_capability_arguments+=(--ambient-caps=-all)
    fi
    /usr/bin/env -i HOME=/nonexistent USER=probe-api LOGNAME=probe-api SHELL=/bin/sh PATH=/usr/bin:/bin \
        /usr/bin/setpriv --reuid=probe-api --regid=probe-api --init-groups -- \
        /usr/bin/setpriv "${drop_capability_arguments[@]}" -- "$@"
}

canonical_directory() {
    local candidate="$1"
    [[ -d "$candidate" ]] || die "directory does not exist: $candidate"
    readlink -f -- "$candidate"
}

# MANAGEMENT_BUNDLE_EXCLUDE_BUILD_BEGIN
validate_source_root() {
    local source_root
    source_root="$(canonical_directory "$1")"
    [[ "$source_root" != "$PROBE_ROOT" && "$source_root" != "$PROBE_ROOT"/* ]] ||
        die "source root must be separate from ${PROBE_ROOT}"

    local required
    for required in \
        probe-api/go.mod \
        probe-api/go.sum \
        probe-api/cmd/probe-api/main.go \
        probe-api/config/probe-api.env.example \
        probe-api/config/probe-postgres-backup.env.example \
        probe-api/deploy/nginx/nginx.conf \
        probe-api/deploy/nginx/nginx-ip.conf \
        probe-api/deploy/scripts/deploy-common.sh \
        probe-api/deploy/scripts/build-release-bundles.sh \
        probe-api/deploy/scripts/install.sh \
        probe-api/deploy/scripts/install-release.sh \
        probe-api/deploy/scripts/upgrade.sh \
        probe-api/deploy/scripts/validate-production.sh \
        probe-api/deploy/scripts/backup-postgres.sh \
        probe-api/deploy/scripts/restore-postgres.sh \
        probe-api/deploy/scripts/security-smoke.sh \
        probe-api/deploy/scripts/load-smoke.sh \
        probe-api/deploy/systemd/probe-api.service \
        probe-api/deploy/systemd/probe-postgres-backup.service \
        probe-api/deploy/systemd/probe-postgres-backup.timer \
        probe-agent/go.mod \
        probe-agent/cmd/probe-agent/main.go \
        probe-agent/config/probe-agent.env.example \
        probe-agent/deploy/install.sh \
        probe-agent/deploy/tests/install-contract.sh \
        probe-agent/deploy/systemd/probe-agent.service \
        probe-web/package.json \
        probe-web/package-lock.json \
        probe-web/deploy/static-site.yaml \
        probe-admin/package.json \
        probe-admin/package-lock.json \
        probe-admin/deploy/static-site.yaml; do
        [[ -f "${source_root}/${required}" ]] || die "source tree is incomplete: ${required}"
    done

    local generated
    for generated in \
        probe-web/node_modules probe-web/dist \
        probe-admin/node_modules probe-admin/dist; do
        [[ ! -e "${source_root}/${generated}" ]] ||
            die "source sync contains generated content: ${generated}"
    done

    [[ ! -e "${source_root}/probe-api/probe-api" ]] ||
        die "source sync contains a probe-api build artifact"
    [[ ! -e "${source_root}/probe-agent/probe-agent" ]] ||
        die "source sync contains a probe-agent build artifact"

    local project
    for project in probe-api probe-agent probe-web probe-admin; do
        if find "${source_root}/${project}" -type l -print -quit | grep -q .; then
            die "source project contains a symbolic link: $project"
        fi
    done

    validate_nginx_template_contract "${source_root}/probe-api/deploy/nginx/nginx.conf" domain
    validate_nginx_template_contract "${source_root}/probe-api/deploy/nginx/nginx-ip.conf" ip

    printf '%s\n' "$source_root"
}

validate_deployment_script_sources() {
    local source_root="$1" script
    for script in \
        deploy-common.sh build-release-bundles.sh install.sh install-release.sh upgrade.sh validate-production.sh \
        backup-postgres.sh restore-postgres.sh security-smoke.sh load-smoke.sh; do
        bash -n -- "${source_root}/probe-api/deploy/scripts/${script}" ||
            die "deployment script syntax is invalid: $script"
    done
    sh -n -- "${source_root}/probe-agent/deploy/install.sh" ||
        die "Agent installer syntax is invalid"
    sh -n -- "${source_root}/probe-agent/deploy/tests/install-contract.sh" ||
        die "Agent installer contract test syntax is invalid"
}
# MANAGEMENT_BUNDLE_EXCLUDE_BUILD_END

clear_exported_probe_environment() {
    local assignment key
    while IFS= read -r assignment; do
        key="${assignment%%=*}"
        unset "$key"
    done < <(env | grep -E '^PROBE_[A-Z0-9_]*=' || true)
}

assert_secure_file() {
    local path="$1" expected_owner="$2"
    [[ -f "$path" && ! -L "$path" ]] || die "required regular file is missing: $path"

    local owner mode group_digit other_digit
    owner="$(stat -c '%U' -- "$path")"
    mode="$(stat -c '%a' -- "$path")"
    [[ "$owner" == "$expected_owner" ]] || die "$path must be owned by $expected_owner"
    [[ "$mode" =~ ^[0-7]{3,4}$ ]] || die "cannot validate permissions for $path"
    mode="${mode: -3}"
    group_digit="${mode:1:1}"
    other_digit="${mode:2:1}"
    (( (10#$group_digit & 2) == 0 && (10#$other_digit & 7) == 0 )) ||
        die "$path must not be group-writable or accessible by other users"
}

assert_private_file() {
    local path="$1" expected_owner="$2" mode
    assert_secure_file "$path" "$expected_owner"
    mode="$(stat -c '%a' -- "$path")"
    mode="${mode: -3}"
    (( (8#$mode & 077) == 0 )) ||
        die "$path must not grant group or other permissions"
}

assert_public_root_file() {
    local path="$1" owner_group mode
    [[ -f "$path" && ! -L "$path" ]] || die "required regular file is missing: $path"
    owner_group="$(stat -c '%U:%G' -- "$path")"
    [[ "$owner_group" == root:root ]] || die "$path must be owned by root:root"
    mode="$(stat -c '%a' -- "$path")"
    [[ "$mode" =~ ^[0-7]{3,4}$ ]] || die "cannot validate permissions for $path"
    mode="${mode: -3}"
    [[ "$mode" == 644 ]] || die "$path must have mode 0644"
}

require_integer_between() {
    local name="$1" value="$2" minimum="$3" maximum="$4"
    [[ "$value" =~ ^[0-9]+$ ]] || die "$name must be an integer"
    (( 10#$value >= minimum && 10#$value <= maximum )) ||
        die "$name must be between $minimum and $maximum"
}

load_probe_env() {
    assert_secure_file "$PROBE_ENV_FILE" root

    local raw line key value
    declare -A seen=()
    while IFS= read -r raw || [[ -n "$raw" ]]; do
        line="${raw%$'\r'}"
        [[ -z "$line" || "$line" =~ ^[[:space:]]*# ]] && continue
        [[ "$line" != *$'\t'* && "$line" != *' '* ]] ||
            die "$PROBE_ENV_FILE must use unquoted KEY=value lines without whitespace"
        [[ "$line" =~ ^(PROBE_[A-Z0-9_]+)=(.*)$ ]] ||
            die "invalid line in $PROBE_ENV_FILE"
        key="${BASH_REMATCH[1]}"
        value="${BASH_REMATCH[2]}"
        [[ -z "${seen[$key]+x}" ]] || die "duplicate key in $PROBE_ENV_FILE: $key"
        seen[$key]=1
        export "$key=$value"
    done < "$PROBE_ENV_FILE"

    [[ "${PROBE_API_LISTEN_ADDR:-}" == "127.0.0.1:8080" ]] ||
        die "PROBE_API_LISTEN_ADDR must be exactly 127.0.0.1:8080 in production"
    [[ "${PROBE_DATABASE_URL:-}" =~ ^postgres(ql)?:// ]] ||
        die "PROBE_DATABASE_URL must be an explicit PostgreSQL URL"
    [[ "$PROBE_DATABASE_URL" != *change-me* ]] ||
        die "replace the placeholder database credential in $PROBE_ENV_FILE"
    [[ -n "${seen[PROBE_INGRESS_MODE]+x}" ]] ||
        die "PROBE_INGRESS_MODE must be set explicitly in $PROBE_ENV_FILE"
    [[ "${PROBE_INGRESS_MODE:-}" == domain || "${PROBE_INGRESS_MODE:-}" == ip ]] ||
        die "PROBE_INGRESS_MODE must be exactly domain or ip"
    if [[ -n "${seen[PROBE_INSTALLATION_PROFILE]+x}" ]]; then
        [[ "${PROBE_INSTALLATION_PROFILE:-}" == full || "${PROBE_INSTALLATION_PROFILE:-}" == management ]] ||
            die "PROBE_INSTALLATION_PROFILE must be full or management"
    else
        PROBE_INSTALLATION_PROFILE=full
        export PROBE_INSTALLATION_PROFILE
    fi
    if [[ "$PROBE_INSTALLATION_PROFILE" == management ]]; then
        [[ -n "${seen[PROBE_PLATFORM_ID]+x}" ]] ||
            die "PROBE_PLATFORM_ID must be set explicitly for a management installation"
        validate_management_platform_id "${PROBE_PLATFORM_ID:-}"
        local runtime_platform
        runtime_platform="$(runtime_platform_id)"
        if [[ "$PROBE_PLATFORM_ID" != "$runtime_platform" ]]; then
            die "installed platform $PROBE_PLATFORM_ID does not match this host $runtime_platform"
        fi
    elif [[ -n "${seen[PROBE_PLATFORM_ID]+x}" ]]; then
        die "historical full-profile configuration must not declare PROBE_PLATFORM_ID"
    fi
    [[ -n "${seen[PROBE_ADMIN_ORIGIN]+x}" ]] ||
        die "PROBE_ADMIN_ORIGIN must be set explicitly in $PROBE_ENV_FILE"
    [[ "${PROBE_ADMIN_ORIGIN:-}" =~ ^https://[^/]+$ ]] ||
        die "PROBE_ADMIN_ORIGIN must be one absolute HTTPS origin"
    [[ "$PROBE_ADMIN_ORIGIN" != "https://admin.example.com" ]] ||
        die "replace the example administrator origin in $PROBE_ENV_FILE"
    [[ -n "${seen[PROBE_AGENT_PUBLIC_URL]+x}" ]] ||
        die "PROBE_AGENT_PUBLIC_URL must be set explicitly in $PROBE_ENV_FILE"
    [[ -n "${seen[PROBE_AGENT_INSTALLER_URL]+x}" ]] ||
        die "PROBE_AGENT_INSTALLER_URL must be set explicitly in $PROBE_ENV_FILE"
    [[ -n "${seen[PROBE_AGENT_INSTALL_CA_FILE]+x}" ]] ||
        die "PROBE_AGENT_INSTALL_CA_FILE must be set explicitly, using an empty value in domain mode"
    if [[ "$PROBE_INSTALLATION_PROFILE" == management ]]; then
        if [[ -z "${PROBE_AGENT_PUBLIC_URL:-}" && -n "${PROBE_AGENT_INSTALLER_URL:-}" ]] ||
           [[ -n "${PROBE_AGENT_PUBLIC_URL:-}" && -z "${PROBE_AGENT_INSTALLER_URL:-}" ]]; then
            die "management Agent public URL and installer URL must either both be empty or both be configured"
        fi
        if [[ -z "${PROBE_AGENT_PUBLIC_URL:-}" ]]; then
            [[ -z "${PROBE_AGENT_INSTALL_CA_FILE:-}" ]] ||
                die "management Agent CA file requires configured Agent public and installer URLs"
        else
            [[ "${PROBE_AGENT_PUBLIC_URL:-}" =~ ^https://[^/]+$ ]] ||
                die "PROBE_AGENT_PUBLIC_URL must be one absolute HTTPS origin"
            [[ "$PROBE_AGENT_PUBLIC_URL" != "https://api.example.com" ]] ||
                die "replace the example Agent public origin in $PROBE_ENV_FILE"
            [[ "$PROBE_AGENT_PUBLIC_URL" == "$PROBE_ADMIN_ORIGIN" ]] ||
                die "management PROBE_AGENT_PUBLIC_URL must equal PROBE_ADMIN_ORIGIN"
            [[ "${PROBE_AGENT_INSTALLER_URL:-}" =~ ^https://raw[.]githubusercontent[.]com/Kcmose/my-agent/([0-9a-f]{40}|refs/tags/v1[.]0[.]2)/deploy/install[.]sh$ ]] ||
                die "PROBE_AGENT_INSTALLER_URL must use an immutable Kcmose/my-agent GitHub commit or the verified refs/tags/v1.0.2 release"
            if [[ -n "${PROBE_AGENT_INSTALL_CA_FILE:-}" ]]; then
                [[ "$PROBE_AGENT_INSTALL_CA_FILE" == /* ]] ||
                    die "PROBE_AGENT_INSTALL_CA_FILE must be an absolute path"
                assert_public_root_file "$PROBE_AGENT_INSTALL_CA_FILE"
            fi
        fi
    else
        [[ "${PROBE_AGENT_PUBLIC_URL:-}" =~ ^https://[^/]+$ ]] ||
            die "PROBE_AGENT_PUBLIC_URL must be one absolute HTTPS origin"
        [[ "$PROBE_AGENT_PUBLIC_URL" != "https://api.example.com" ]] ||
            die "replace the example Agent public origin in $PROBE_ENV_FILE"
        [[ "${PROBE_AGENT_INSTALLER_URL:-}" =~ ^https://raw[.]githubusercontent[.]com/Kcmose/my-agent/([0-9a-f]{40}|refs/tags/v1[.]0[.]2)/deploy/install[.]sh$ ]] ||
            die "PROBE_AGENT_INSTALLER_URL must use an immutable Kcmose/my-agent GitHub commit or the verified refs/tags/v1.0.2 release"
    fi
    [[ "${PROBE_ADMIN_ALLOWLIST_FILE:-}" == "$PROBE_ALLOWLIST_FILE" ]] ||
        die "PROBE_ADMIN_ALLOWLIST_FILE must be $PROBE_ALLOWLIST_FILE"
    [[ "${PROBE_TRUSTED_PROXY_CIDRS:-}" == "127.0.0.1/32,::1/128" ]] ||
        die "PROBE_TRUSTED_PROXY_CIDRS must trust only the local Nginx proxy"

    [[ -f "$PROBE_ACTIVE_NGINX_CONFIG" && ! -L "$PROBE_ACTIVE_NGINX_CONFIG" ]] ||
        die "active Nginx fragment is missing while validating the ingress environment"

    case "$PROBE_INGRESS_MODE" in
        domain)
            [[ -z "$PROBE_AGENT_INSTALL_CA_FILE" ]] ||
                die "domain mode must not configure PROBE_AGENT_INSTALL_CA_FILE"
            [[ ! -e "$PROBE_PRIVATE_CA_FILE" && ! -L "$PROBE_PRIVATE_CA_FILE" ]] ||
                die "domain mode must not retain IP-mode private CA material at $PROBE_PRIVATE_CA_FILE"
            ;;
        ip)
            if [[ "$PROBE_INSTALLATION_PROFILE" == full ]]; then
                [[ "$PROBE_AGENT_INSTALL_CA_FILE" == "$PROBE_PRIVATE_CA_FILE" ]] ||
                    die "IP mode PROBE_AGENT_INSTALL_CA_FILE must be $PROBE_PRIVATE_CA_FILE"
            elif [[ -n "${PROBE_AGENT_PUBLIC_URL:-}" ]]; then
                [[ "$PROBE_AGENT_INSTALL_CA_FILE" == "$PROBE_PRIVATE_CA_FILE" ]] ||
                    die "management IP Agent integration must use $PROBE_PRIVATE_CA_FILE"
            fi
            assert_public_root_file "$PROBE_PRIVATE_CA_FILE"
            ;;
    esac
}

canonical_ip_from_origin() {
    local origin="$1" expected_port="$2"
    /usr/bin/python3 - "$origin" "$expected_port" <<'PY'
import ipaddress
import sys
import urllib.parse

origin, expected_port = sys.argv[1], int(sys.argv[2])
try:
    parsed = urllib.parse.urlsplit(origin)
    if parsed.scheme != "https" or parsed.username is not None or parsed.password is not None:
        raise ValueError
    if parsed.path or parsed.query or parsed.fragment or parsed.port != expected_port:
        raise ValueError
    hostname = parsed.hostname or ""
    if "%" in hostname:
        raise ValueError
    address = ipaddress.ip_address(hostname)
    if str(address) != hostname.lower():
        raise ValueError
    if address.is_unspecified or address.is_loopback or address.is_link_local or address.is_multicast:
        raise ValueError
    if isinstance(address, ipaddress.IPv6Address) and address.ipv4_mapped is not None:
        raise ValueError
    if isinstance(address, ipaddress.IPv4Address) and int(address) == 0xFFFFFFFF:
        raise ValueError
except (ValueError, TypeError):
    raise SystemExit(1)
print(address)
PY
}

selected_nginx_template() {
    local source_root="$1" profile="${2:-full}"
    validate_release_profile "$profile"
    local dialect=""
    if [[ "$profile" == management ]]; then
        dialect="$(management_platform_nginx_dialect "${PROBE_PLATFORM_ID:-}")"
    fi
    case "${profile}:${PROBE_INGRESS_MODE:-}:${dialect}" in
        management:domain:modern) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx-management.conf" ;;
        management:ip:modern) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx-management-ip.conf" ;;
        management:domain:legacy) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx-management-legacy.conf" ;;
        management:ip:legacy) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx-management-ip-legacy.conf" ;;
        management:domain:classic) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx-management-classic.conf" ;;
        management:ip:classic) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx-management-ip-classic.conf" ;;
        full:domain:) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx.conf" ;;
        full:ip:) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx-ip.conf" ;;
        *) die "load the explicit ingress mode before selecting an Nginx template" ;;
    esac
}

validate_closed_install_routes() {
    local template_file="$1" expected="${2:-4}" result
    result="$(awk '
        /^[ \t]*location[ \t]+(=|\^~)[ \t]+\/install\/?[ \t]*\{[ \t]*$/ {
            routes++
            if ((getline next_line) > 0) {
                sub(/^[ \t]*/, "", next_line)
                sub(/[ \t]*$/, "", next_line)
                if (next_line == "return 404;") closed++
            }
        }
        END { printf "%d:%d\n", routes + 0, closed + 0 }
    ' "$template_file")"
    [[ "$result" == "${expected}:${expected}" ]] ||
        die "every reviewed /install route must remain a fixed 404 response"
}

validate_management_nginx_template_contract() {
    local template_file="$1" ingress_mode="$2" dialect="${3:-modern}"
    [[ -f "$template_file" && ! -L "$template_file" ]] ||
        die "management Nginx source template is missing: $template_file"
    [[ "$ingress_mode" == domain || "$ingress_mode" == ip ]] ||
        die "unknown management Nginx template mode: $ingress_mode"
    [[ "$dialect" == modern || "$dialect" == legacy || "$dialect" == classic ]] ||
        die "unknown management Nginx HTTP/2 dialect: $dialect"

    local expected_locations actual_locations
    expected_locations="$(cat <<'EOF'
location = /api/v1/auth/login {
location ~ ^/api/v1/(?:auth|admin)(?:/|$) {
location ~ ^/api/v1/panel(?:/|$) {
location = /api/v1/agent/enroll {
location = /api/v1/agent/config {
location = /api/v1/agent/report {
location /api/v1/agent/ {
location = /api/v1/setup {
location ^~ /api/v1/setup/ {
location = /api {
location /api/ {
location = /internal {
location /internal/ {
location @probe_admin_rate_limited {
location @probe_agent_rate_limited {
location ~ (^|/)\. {
location = /install {
location ^~ /install/ {
location = /overview {
location ^~ /overview/ {
location = /probes {
location ^~ /probes/ {
location = /nodes {
location ^~ /nodes/ {
location = /downloads {
location ^~ /downloads/ {
location / {
EOF
)"
    actual_locations="$(awk '/^[ \t]*location[ \t]/ {
        sub(/^[ \t]*/, "")
        sub(/[ \t]*$/, "")
        print
    }' "$template_file")"
    [[ "$actual_locations" == "$expected_locations" ]] ||
        die "management Nginx source template location contract changed"
    validate_closed_install_routes "$template_file" 2

    grep -Fxq '        root /srv/probe/admin;' "$template_file" ||
        die "management Nginx template must serve only the administrator artifact"
    if grep -Eq '/srv/probe/(web|agent)|/downloads/probe-agent' "$template_file"; then
        die "management Nginx template must not expose visitor or Agent download surfaces"
    fi
    [[ "$(grep -Ec '^[[:space:]]*proxy_pass[[:space:]]+http://probe_api;$' "$template_file")" -eq 6 ]] ||
        die "management Nginx template has an unexpected upstream route count"
    # The Nginx variable is a literal template contract, not a shell variable.
    # shellcheck disable=SC2016
    [[ "$(grep -Fxc '        if ($probe_admin_allowed = 0) {' "$template_file")" -eq 4 ]] ||
        die "management allowlist must protect only auth, admin, panel, and static routes"
    [[ "$(grep -Fxc '        proxy_set_header Cookie "";' "$template_file")" -eq 3 &&
       "$(grep -Fxc '        proxy_hide_header Set-Cookie;' "$template_file")" -eq 3 ]] ||
        die "every management-host Agent route must strip browser cookies"
    grep -Fxq '    client_max_body_size 256k;' "$template_file" ||
        die "management Nginx template must accept the bounded Agent report body"
    if [[ "$dialect" == classic ]]; then
        grep -Fxq '    ssl_protocols TLSv1.2;' "$template_file" ||
            die "classic management Nginx template must use TLS 1.2"
        # Literal Nginx variable, not a shell expansion.
        # shellcheck disable=SC2016
        if grep -Fq 'TLSv1.3' "$template_file" || grep -Fq '$request_id' "$template_file"; then
            die "classic management Nginx template contains an unsupported Nginx/OpenSSL feature"
        fi
    else
        grep -Fxq '    ssl_protocols TLSv1.2 TLSv1.3;' "$template_file" ||
            die "management Nginx template must use TLS 1.2 and TLS 1.3"
    fi

    local http2_directive_count
    http2_directive_count="$(grep -Fxc '    http2 on;' "$template_file" || true)"
    if [[ "$ingress_mode" == domain ]]; then
        [[ "$(grep -Ec '^server \{$' "$template_file")" -eq 2 &&
           "$(grep -Ec '^[[:space:]]*listen[[:space:]]' "$template_file")" -eq 4 ]] ||
            die "management domain template must contain exactly two server blocks and four listeners"
        local -a listeners=('    listen 80;' '    listen [::]:80;')
        if [[ "$dialect" == modern ]]; then
            listeners+=('    listen 443 ssl;' '    listen [::]:443 ssl;')
            [[ "$http2_directive_count" -eq 1 ]] ||
                die "modern management domain template must enable HTTP/2 exactly once"
        else
            listeners+=('    listen 443 ssl http2;' '    listen [::]:443 ssl http2;')
            [[ "$http2_directive_count" -eq 0 ]] ||
                die "legacy management domain template must use only listen ... http2"
        fi
        for listener in "${listeners[@]}"; do
            [[ "$(grep -Fxc "$listener" "$template_file")" -eq 1 ]] ||
                die "management domain template listener contract changed: $listener"
        done
    else
        [[ "$(grep -Ec '^server \{$' "$template_file")" -eq 1 &&
           "$(grep -Ec '^[[:space:]]*listen[[:space:]]' "$template_file")" -eq 2 ]] ||
            die "management IP template must contain one server block and two listeners"
        if [[ "$dialect" == modern ]]; then
            [[ "$(grep -Fxc '    listen 18455 ssl default_server;' "$template_file")" -eq 1 &&
               "$(grep -Fxc '    listen [::]:18455 ssl default_server;' "$template_file")" -eq 1 &&
               "$http2_directive_count" -eq 1 ]] ||
                die "modern management IP template must bind TCP 18455 and enable HTTP/2 exactly once"
        else
            [[ "$(grep -Fxc '    listen 18455 ssl http2 default_server;' "$template_file")" -eq 1 &&
               "$(grep -Fxc '    listen [::]:18455 ssl http2 default_server;' "$template_file")" -eq 1 &&
               "$http2_directive_count" -eq 0 ]] ||
                die "legacy management IP template must bind TCP 18455 with listen ... http2"
        fi
    fi
}

validate_ip_nginx_template_contract() {
    local template_file="$1"
    [[ -f "$template_file" && ! -L "$template_file" ]] ||
        die "IP-mode Nginx source template is missing: $template_file"

    local expected_locations actual_locations port
    expected_locations="$(cat <<'EOF'
location ~ ^/api/v1/panel(?:/|$) {
location = /api {
location /api/ {
location = /internal {
location /internal/ {
location ~ (^|/)\. {
location = /install {
location ^~ /install/ {
location = /login {
location ^~ /login/ {
location = /admin {
location ^~ /admin/ {
location = /downloads {
location ^~ /downloads/ {
location / {
location = /api/v1/auth/login {
location ~ ^/api/v1/(?:auth|admin)(?:/|$) {
location ~ ^/api/v1/panel(?:/|$) {
location = /api {
location /api/ {
location = /internal {
location /internal/ {
location @probe_admin_rate_limited {
location ~ (^|/)\. {
location = /install {
location ^~ /install/ {
location = /overview {
location ^~ /overview/ {
location = /probes {
location ^~ /probes/ {
location = /nodes {
location ^~ /nodes/ {
location = /downloads {
location ^~ /downloads/ {
location / {
location = /api/v1/agent/enroll {
location = /api/v1/agent/config {
location = /api/v1/agent/report {
location = /downloads/probe-agent/ca.pem {
location ~ ^/downloads/probe-agent/(?:install[.]sh|SHA256SUMS|probe-agent[.]service|linux-(?:amd64|arm64)/probe-agent)$ {
location /api/v1/agent/ {
location @probe_agent_rate_limited {
location ^~ /api/v1/public/ {
location / {
EOF
)"
    actual_locations="$(awk '/^[ \t]*location[ \t]/ {
        sub(/^[ \t]*/, "")
        sub(/[ \t]*$/, "")
        print
    }' "$template_file")"
    [[ "$actual_locations" == "$expected_locations" ]] ||
        die "IP-mode Nginx source template location contract changed; review the validator before deployment"
    validate_closed_install_routes "$template_file"

    [[ "$(grep -Ec '^server \{$' "$template_file")" -eq 3 ]] ||
        die "IP-mode Nginx template must contain exactly three server blocks"
    [[ "$(grep -Ec '^[[:space:]]*listen[[:space:]]' "$template_file")" -eq 6 ]] ||
        die "IP-mode Nginx template has an unexpected listener count"
    for port in 18453 18454 18455; do
        [[ "$(grep -Fxc "    listen $port ssl default_server;" "$template_file")" -eq 1 &&
           "$(grep -Fxc "    listen [::]:$port ssl default_server;" "$template_file")" -eq 1 ]] ||
            die "IP-mode Nginx template must bind TCP $port exactly once per address family"
    done
    [[ "$(grep -Fc '    server_name _;' "$template_file")" -eq 3 ]] ||
        die "IP-mode Nginx template must use three default servers without domain routing"
    [[ "$(grep -Fo 'PROBE_SETUP_SERVER_IP' "$template_file" | wc -l)" -eq 7 ]] ||
        die "IP-mode Nginx template server-IP placeholder count changed"
    [[ "$(grep -Ec '^[[:space:]]*proxy_pass[[:space:]]' "$template_file")" -eq 7 &&
       "$(grep -Ec '^[[:space:]]*proxy_pass[[:space:]]+http://probe_api;$' "$template_file")" -eq 7 ]] ||
        die "IP-mode Nginx template has an unexpected upstream route count"
    [[ "$(grep -Fxc '    proxy_set_header Cookie "";' "$template_file")" -eq 2 &&
       "$(grep -Fxc '    proxy_hide_header Set-Cookie;' "$template_file")" -eq 2 ]] ||
        die "IP-mode visitor and Agent surfaces must strip cookies in both directions"
    grep -Fxq '        alias /etc/probe-panel/tls/private-ca/ca.pem;' "$template_file" ||
        die "IP-mode Agent CA route must expose only the fixed public CA file"
}

validate_nginx_template_contract() {
    local template_file="$1" ingress_mode="${2:-domain}"
    [[ -f "$template_file" && ! -L "$template_file" ]] ||
        die "Nginx source template is missing: $template_file"

    if [[ "$ingress_mode" == ip ]]; then
        validate_ip_nginx_template_contract "$template_file"
        return
    fi
    [[ "$ingress_mode" == domain ]] || die "unknown Nginx template mode: $ingress_mode"

    local expected_locations actual_locations
    expected_locations="$(cat <<'EOF'
location ~ ^/api/v1/panel(?:/|$) {
location = /api {
location /api/ {
location = /api/v1/setup {
location ^~ /api/v1/setup/ {
location = /internal {
location /internal/ {
location ~ (^|/)\. {
location = /install {
location ^~ /install/ {
location = /login {
location ^~ /login/ {
location = /admin {
location ^~ /admin/ {
location = /downloads {
location ^~ /downloads/ {
location / {
location = /api/v1/auth/login {
location ~ ^/api/v1/(?:auth|admin)(?:/|$) {
location ~ ^/api/v1/panel(?:/|$) {
location = /api/v1/setup {
location ^~ /api/v1/setup/ {
location = /api {
location /api/ {
location = /internal {
location /internal/ {
location @probe_admin_rate_limited {
location ~ (^|/)\. {
location = /install {
location ^~ /install/ {
location = /overview {
location ^~ /overview/ {
location = /probes {
location ^~ /probes/ {
location = /nodes {
location ^~ /nodes/ {
location = /downloads {
location ^~ /downloads/ {
location / {
location = /api/v1/agent/enroll {
location = /api/v1/agent/config {
location = /api/v1/agent/report {
location ~ ^/downloads/probe-agent/(?:ca[.]pem|install[.]sh|SHA256SUMS|probe-agent[.]service|linux-(?:amd64|arm64)/probe-agent)$ {
location /api/v1/agent/ {
location @probe_agent_rate_limited {
location ^~ /api/v1/public/ {
location / {
EOF
)"
    actual_locations="$(awk '/^[ \t]*location[ \t]/ {
        sub(/^[ \t]*/, "")
        sub(/[ \t]*$/, "")
        print
    }' "$template_file")"
    [[ "$actual_locations" == "$expected_locations" ]] ||
        die "Nginx source template location contract changed; review and update the validator before deployment"
    validate_closed_install_routes "$template_file"

    [[ "$(grep -Ec '^server \{$' "$template_file")" -eq 6 ]] ||
        die "Nginx source template must contain exactly six server blocks"
    [[ "$(grep -Ec '^[[:space:]]*listen[[:space:]]' "$template_file")" -eq 12 ]] ||
        die "Nginx source template has an unexpected listener count"
    [[ "$(awk '/^[ \t]*listen[ \t]/ {
        port=$2
        sub(/;$/, "", port)
        sub(/^.*:/, "", port)
        seen[port]++
        total++
        if (port != "80" && port != "443") bad=1
    } END { print (!bad && total == 12 && seen["80"] == 4 && seen["443"] == 8) ? 1 : 0 }' "$template_file")" -eq 1 ]] ||
        die "domain-mode Nginx template must use only its reviewed TCP 80/443 listeners"
    [[ "$(grep -Ec '^[[:space:]]*proxy_pass[[:space:]]' "$template_file")" -eq 7 &&
       "$(grep -Ec '^[[:space:]]*proxy_pass[[:space:]]+http://probe_api;$' "$template_file")" -eq 7 ]] ||
        die "Nginx source template has an unexpected upstream route count"

    local -a template_names=()
    mapfile -t template_names < <(awk '$1 == "server_name" {
        line=""
        for (i=2; i<=NF; i++) {
            value=$i
            sub(/;$/, "", value)
            line=line (line == "" ? "" : " ") value
        }
        print line
    }' "$template_file")
    [[ "${#template_names[@]}" -eq 6 &&
       "${template_names[0]}" == 'panel.example.com admin.example.com api.example.com' &&
       "${template_names[1]}" == 'panel.example.com' &&
       "${template_names[2]}" == 'admin.example.com' &&
       "${template_names[3]}" == 'api.example.com' &&
       "${template_names[4]}" == '_' && "${template_names[5]}" == '_' ]] ||
        die "Nginx source template host contract is invalid"
}

validate_ip_nginx_fragment_structure() {
    local active_file="$1" template_file="$2"
    validate_nginx_template_contract "$template_file" ip

    local admin_ip agent_ip server_ip nginx_host normalized_hash template_hash
    if ! admin_ip="$(canonical_ip_from_origin "$PROBE_ADMIN_ORIGIN" 18455)"; then
        die "IP mode PROBE_ADMIN_ORIGIN must be https://<canonical-IP>:18455"
    fi
    if ! agent_ip="$(canonical_ip_from_origin "$PROBE_AGENT_PUBLIC_URL" 18454)"; then
        die "IP mode PROBE_AGENT_PUBLIC_URL must be https://<canonical-IP>:18454"
    fi
    [[ "$admin_ip" == "$agent_ip" ]] ||
        die "IP mode administrator and Agent origins must use the same server IP"
    server_ip="$admin_ip"
    nginx_host="$server_ip"
    [[ "$server_ip" != *:* ]] || nginx_host="[$server_ip]"
    [[ "$PROBE_ADMIN_ORIGIN" == "https://${nginx_host}:18455" ]] ||
        die "IP mode administrator origin is not canonical"
    [[ "$PROBE_AGENT_PUBLIC_URL" == "https://${nginx_host}:18454" ]] ||
        die "IP mode Agent origin is not canonical"
    [[ "$(grep -Fo -- "$nginx_host" "$active_file" | wc -l)" -eq 7 ]] ||
        die "active IP-mode Nginx fragment has an unexpected server-IP occurrence count"
    ! grep -Fq 'PROBE_SETUP_SERVER_IP' "$active_file" ||
        die "active IP-mode Nginx fragment still contains the setup placeholder"

    normalized_hash="$(awk -v address="$nginx_host" '
        function replace_literal(text, needle, replacement, position, output) {
            output=""
            while ((position=index(text, needle)) > 0) {
                output=output substr(text, 1, position - 1) replacement
                text=substr(text, position + length(needle))
            }
            return output text
        }
        { print replace_literal($0, address, "PROBE_SETUP_SERVER_IP") }
    ' "$active_file" | sha256sum | awk '{print $1}')"
    template_hash="$(sha256sum -- "$template_file" | awk '{print $1}')"
    [[ "$normalized_hash" == "$template_hash" ]] ||
        die "active IP-mode Nginx fragment may differ from the reviewed template only by PROBE_SETUP_SERVER_IP"
}

validate_management_nginx_fragment_structure() {
    local active_file="$1" template_file="$2"
    validate_management_nginx_template_contract \
        "$template_file" "$PROBE_INGRESS_MODE" \
        "$(management_platform_nginx_dialect "${PROBE_PLATFORM_ID:-}")"

    local normalized_hash template_hash
    if [[ "$PROBE_INGRESS_MODE" == ip ]]; then
        local admin_ip nginx_host
        if ! admin_ip="$(canonical_ip_from_origin "$PROBE_ADMIN_ORIGIN" 18455)"; then
            die "management IP mode PROBE_ADMIN_ORIGIN must be https://<canonical-IP>:18455"
        fi
        nginx_host="$admin_ip"
        [[ "$admin_ip" != *:* ]] || nginx_host="[$admin_ip]"
        [[ "$PROBE_ADMIN_ORIGIN" == "https://${nginx_host}:18455" ]] ||
            die "management IP administrator origin is not canonical"
        [[ "$(grep -Fo -- "$nginx_host" "$active_file" | wc -l)" -eq 2 ]] ||
            die "active management IP fragment has an unexpected server-IP occurrence count"
        ! grep -Fq 'PROBE_SETUP_SERVER_IP' "$active_file" ||
            die "active management IP fragment still contains the setup placeholder"
        normalized_hash="$(awk -v address="$nginx_host" '
            function replace_literal(text, needle, replacement, position, output) {
                output=""
                while ((position=index(text, needle)) > 0) {
                    output=output substr(text, 1, position - 1) replacement
                    text=substr(text, position + length(needle))
                }
                return output text
            }
            { print replace_literal($0, address, "PROBE_SETUP_SERVER_IP") }
        ' "$active_file" | sha256sum | awk '{print $1}')"
    elif [[ "$PROBE_INGRESS_MODE" == domain ]]; then
        local -a server_names=()
        mapfile -t server_names < <(awk '$1 == "server_name" {
            value=$2
            sub(/;$/, "", value)
            print value
        }' "$active_file")
        [[ "${#server_names[@]}" -eq 2 && -n "${server_names[0]}" &&
           "${server_names[0]}" == "${server_names[1]}" ]] ||
            die "active management domain fragment must contain exactly one administrator hostname twice"
        local admin_domain="${server_names[0]}"
        [[ "$admin_domain" =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]] ||
            die "invalid management administrator hostname: $admin_domain"
        [[ "$admin_domain" != example.com && "$admin_domain" != *.example.com ]] ||
            die "replace the example.com management hostname in $active_file"
        [[ "$PROBE_ADMIN_ORIGIN" == "https://${admin_domain}" ]] ||
            die "management domain PROBE_ADMIN_ORIGIN must match the administrator hostname"
        normalized_hash="$(awk -v admin="$admin_domain" '
            function replace_literal(text, needle, replacement, position, output) {
                output=""
                while ((position=index(text, needle)) > 0) {
                    output=output substr(text, 1, position - 1) replacement
                    text=substr(text, position + length(needle))
                }
                return output text
            }
            { print replace_literal($0, admin, "admin.example.com") }
        ' "$active_file" | sha256sum | awk '{print $1}')"
    else
        die "load the explicit ingress mode before validating the management Nginx fragment"
    fi
    template_hash="$(sha256sum -- "$template_file" | awk '{print $1}')"
    [[ "$normalized_hash" == "$template_hash" ]] ||
        die "active management Nginx fragment differs from its reviewed template"
}

validate_nginx_fragment_structure() {
    local active_file="$1" template_file="$2" profile="${3:-full}"
    validate_release_profile "$profile"
    if [[ "$profile" == management ]]; then
        validate_management_nginx_fragment_structure "$active_file" "$template_file"
        return
    fi
    if [[ "${PROBE_INGRESS_MODE:-}" == ip ]]; then
        validate_ip_nginx_fragment_structure "$active_file" "$template_file"
        return
    fi
    [[ "${PROBE_INGRESS_MODE:-}" == domain ]] ||
        die "load the explicit ingress mode before validating the active Nginx fragment"
    validate_nginx_template_contract "$template_file" domain

    local -a server_names=()
    mapfile -t server_names < <(awk '$1 == "server_name" {
        line=""
        for (i=2; i<=NF; i++) {
            value=$i
            sub(/;$/, "", value)
            line=line (line == "" ? "" : " ") value
        }
        print line
    }' "$active_file")
    [[ "${#server_names[@]}" -eq 6 ]] || die "active Nginx fragment must contain exactly six server_name directives"

    local panel_domain admin_domain agent_domain extra domain
    read -r panel_domain admin_domain agent_domain extra <<<"${server_names[0]}"
    [[ -n "$panel_domain" && -n "$admin_domain" && -n "$agent_domain" && -z "$extra" ]] ||
        die "the HTTP redirect server must name exactly the panel, admin, and Agent hosts"
    [[ "${server_names[1]}" == "$panel_domain" &&
       "${server_names[2]}" == "$admin_domain" &&
       "${server_names[3]}" == "$agent_domain" &&
       "${server_names[4]}" == '_' && "${server_names[5]}" == '_' ]] ||
        die "active Nginx server_name order does not match the three-host contract"
    [[ "$panel_domain" != "$admin_domain" && "$panel_domain" != "$agent_domain" &&
       "$admin_domain" != "$agent_domain" ]] || die "panel, admin, and Agent domains must be distinct"

    for domain in "$panel_domain" "$admin_domain" "$agent_domain"; do
        [[ "$domain" =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]] ||
            die "invalid production hostname in active Nginx fragment: $domain"
        [[ "$domain" != example.com && "$domain" != *.example.com ]] ||
            die "replace all example.com hostnames in $active_file"
    done
    [[ "$panel_domain" != *"$admin_domain"* && "$admin_domain" != *"$panel_domain"* &&
       "$panel_domain" != *"$agent_domain"* && "$agent_domain" != *"$panel_domain"* &&
       "$admin_domain" != *"$agent_domain"* && "$agent_domain" != *"$admin_domain"* ]] ||
        die "one production hostname must not contain another hostname"

    [[ "$PROBE_ADMIN_ORIGIN" == "https://${admin_domain}" ]] ||
        die "domain mode PROBE_ADMIN_ORIGIN must match the dedicated administrator hostname"
    [[ "$PROBE_AGENT_PUBLIC_URL" == "https://${agent_domain}" ]] ||
        die "domain mode PROBE_AGENT_PUBLIC_URL must match the dedicated Agent hostname"

    local normalized_hash template_hash
    normalized_hash="$(awk \
        -v panel="$panel_domain" -v admin="$admin_domain" -v agent="$agent_domain" '
        function replace_literal(text, needle, replacement, position, output) {
            output=""
            while ((position=index(text, needle)) > 0) {
                output=output substr(text, 1, position - 1) replacement
                text=substr(text, position + length(needle))
            }
            return output text
        }
        {
            line=replace_literal($0, panel, "panel.example.com")
            line=replace_literal(line, admin, "admin.example.com")
            line=replace_literal(line, agent, "api.example.com")
            print line
        }' "$active_file" | sha256sum | awk '{print $1}')"
    template_hash="$(sha256sum -- "$template_file" | awk '{print $1}')"
    [[ "$normalized_hash" == "$template_hash" ]] ||
        die "active Nginx fragment may differ from the reviewed source template only by its three hostnames"
}

validate_active_nginx_config() {
    local template_file="$1" profile="${2:-full}"
    [[ -f "$PROBE_ACTIVE_NGINX_CONFIG" && ! -L "$PROBE_ACTIVE_NGINX_CONFIG" ]] ||
        die "create the active Nginx fragment at $PROBE_ACTIVE_NGINX_CONFIG"

    local owner mode
    owner="$(stat -c '%U' -- "$PROBE_ACTIVE_NGINX_CONFIG")"
    mode="$(stat -c '%a' -- "$PROBE_ACTIVE_NGINX_CONFIG")"
    [[ "$owner" == root ]] || die "$PROBE_ACTIVE_NGINX_CONFIG must be root-owned"
    [[ "$mode" =~ ^[0-7]{3,4}$ ]] || die "cannot validate Nginx config permissions"
    mode="${mode: -3}"
    (( (10#${mode:1:1} & 2) == 0 && (10#${mode:2:1} & 2) == 0 )) ||
        die "$PROBE_ACTIVE_NGINX_CONFIG must not be group/world-writable"

    validate_nginx_fragment_structure "$PROBE_ACTIVE_NGINX_CONFIG" "$template_file" "$profile"
}

validate_nginx_listen_ports() {
    local dump_file="$1" profile="${2:-full}"
    validate_release_profile "$profile"
    awk -v mode="$PROBE_INGRESS_MODE" -v profile="$profile" '
        /^[ \t]*listen[ \t]+/ {
            value=$2
            sub(/;$/, "", value)
            if (value ~ /^unix:/) {
                printf "unsupported Nginx listener: %s\n", value > "/dev/stderr"
                bad=1
                next
            }
            port=value
            sub(/^.*:/, "", port)
            allowed=0
            if (mode == "domain") {
                allowed=(port == "80" || port == "443")
            } else if (mode == "ip" && profile == "management") {
                allowed=(port == "18455")
            } else if (mode == "ip" && profile == "full") {
                allowed=(port == "18453" || port == "18454" || port == "18455")
            } else {
                printf "unsupported PROBE_INGRESS_MODE while validating Nginx listeners: %s\n", mode > "/dev/stderr"
                bad=1
                next
            }
            if (!allowed) {
                printf "Nginx listener outside the %s-mode port contract: %s\n", mode, value > "/dev/stderr"
                bad=1
            }
            seen[port]++
        }
        END {
            if (mode == "domain" && (seen["80"] < 1 || seen["443"] < 1)) bad=1
            if (mode == "ip" && profile == "management" && seen["18455"] != 2) bad=1
            if (mode == "ip" && profile == "full" && (seen["18453"] != 2 || seen["18454"] != 2 || seen["18455"] != 2)) bad=1
            if (bad) exit 1
            exit 0
        }
    ' "$dump_file" || die "Nginx listeners do not match PROBE_INGRESS_MODE=$PROBE_INGRESS_MODE"
}

validate_no_duplicate_nginx_hosts() {
    local dump_file="$1" profile="${2:-full}" panel_domain admin_domain agent_domain extra domain count
    [[ "$PROBE_INGRESS_MODE" == domain ]] || return 0
    if [[ "$profile" == management ]]; then
        admin_domain="$(awk '$1 == "server_name" { value=$2; sub(/;$/, "", value); print value; exit }' "$PROBE_ACTIVE_NGINX_CONFIG")"
        [[ -n "$admin_domain" ]] || die "could not extract the management administrator hostname"
        count="$(awk -v expected="$admin_domain" '$1 == "server_name" {
            for (i=2; i<=NF; i++) { value=$i; sub(/;$/, "", value); if (value == expected) count++ }
        } END { print count + 0 }' "$dump_file")"
        [[ "$count" -eq 2 ]] ||
            die "Nginx runtime must declare management hostname $admin_domain exactly twice"
        return 0
    fi
    read -r panel_domain admin_domain agent_domain extra < <(
        awk '$1 == "server_name" {
            for (i=2; i<=NF; i++) {
                sub(/;$/, "", $i)
                printf "%s%s", $i, (i == NF ? ORS : OFS)
            }
            exit
        }' "$PROBE_ACTIVE_NGINX_CONFIG"
    )
    [[ -n "$panel_domain" && -n "$admin_domain" && -n "$agent_domain" && -z "$extra" ]] ||
        die "could not extract the three active Nginx hostnames"

    for domain in "$panel_domain" "$admin_domain" "$agent_domain"; do
        count="$(awk -v expected="$domain" '$1 == "server_name" {
            for (i=2; i<=NF; i++) {
                value=$i
                sub(/;$/, "", value)
                if (value == expected) count++
            }
        } END { print count + 0 }' "$dump_file")"
        [[ "$count" -eq 2 ]] ||
            die "Nginx runtime must declare $domain exactly twice; remove duplicate server_name declarations"
    done
}

validate_nginx_runtime_config() {
    local template_file="$1" profile="${2:-full}"
    require_commands nginx mktemp
    validate_active_nginx_config "$template_file" "$profile"

    [[ -L "$PROBE_NGINX_LINK" ]] || die "$PROBE_NGINX_LINK must be a symbolic link"
    [[ "$(readlink -f -- "$PROBE_NGINX_LINK")" == "$PROBE_ACTIVE_NGINX_CONFIG" ]] ||
        die "$PROBE_NGINX_LINK must point to $PROBE_ACTIVE_NGINX_CONFIG"

    local dump_file
    dump_file="$(mktemp /var/tmp/probe-nginx-dump.XXXXXX)"
    if ! nginx -t; then
        rm -f -- "$dump_file"
        die "nginx -t failed"
    fi
    if ! nginx -T >"$dump_file" 2>&1; then
        rm -f -- "$dump_file"
        die "nginx -T failed"
    fi
    validate_nginx_listen_ports "$dump_file" "$profile"
    validate_no_duplicate_nginx_hosts "$dump_file" "$profile"
    rm -f -- "$dump_file"
}

validate_allowlist_with_binary() {
    local api_binary="$1"
    [[ -x "$api_binary" && ! -L "$api_binary" ]] || die "invalid staged API binary: $api_binary"
    assert_secure_file "$PROBE_ALLOWLIST_FILE" root
    run_as_probe_api_no_environment /usr/bin/test -r "$PROBE_ALLOWLIST_FILE" ||
        die "probe-api cannot read $PROBE_ALLOWLIST_FILE"
    "$api_binary" config validate-admin-allowlist "$PROBE_ALLOWLIST_FILE"
}

validate_ingress_tls_with_binary() {
    local api_binary="$1" profile="${2:-full}"
    [[ -x "$api_binary" ]] || die "invalid API binary for ingress TLS validation: $api_binary"

    if [[ "$profile" == management ]]; then
        case "$PROBE_INGRESS_MODE" in
            domain)
                local admin_domain
                admin_domain="$(awk '$1 == "server_name" { value=$2; sub(/;$/, "", value); print value; exit }' "$PROBE_ACTIVE_NGINX_CONFIG")"
                [[ -n "$admin_domain" ]] || die "could not extract management hostname for TLS validation"
                "$api_binary" config validate-ingress-tls admin-domain "$admin_domain"
                ;;
            ip)
                local admin_address
                admin_address="$(canonical_ip_from_origin "$PROBE_ADMIN_ORIGIN" 18455)" ||
                    die "management IP origin is invalid while validating ingress TLS"
                "$api_binary" config validate-ingress-tls admin-ip "$admin_address"
                ;;
            *) die "load the explicit ingress mode before validating management ingress TLS" ;;
        esac
        return 0
    fi

    case "$PROBE_INGRESS_MODE" in
        domain)
            local panel_domain admin_domain agent_domain extra
            read -r panel_domain admin_domain agent_domain extra < <(
                awk '$1 == "server_name" {
                    for (i=2; i<=NF; i++) {
                        sub(/;$/, "", $i)
                        printf "%s%s", $i, (i == NF ? ORS : OFS)
                    }
                    exit
                }' "$PROBE_ACTIVE_NGINX_CONFIG"
            )
            [[ -n "$panel_domain" && -n "$admin_domain" && -n "$agent_domain" && -z "$extra" ]] ||
                die "could not extract the three domain-mode ingress hostnames for TLS validation"
            "$api_binary" config validate-ingress-tls domain \
                "$panel_domain" "$admin_domain" "$agent_domain"
            ;;
        ip)
            local admin_address agent_address
            if ! admin_address="$(canonical_ip_from_origin "$PROBE_ADMIN_ORIGIN" 18455)"; then
                die "IP-mode administrator origin is invalid while validating ingress TLS"
            fi
            if ! agent_address="$(canonical_ip_from_origin "$PROBE_AGENT_PUBLIC_URL" 18454)"; then
                die "IP-mode Agent origin is invalid while validating ingress TLS"
            fi
            [[ "$admin_address" == "$agent_address" ]] ||
                die "IP-mode origins disagree while validating ingress TLS"
            "$api_binary" config validate-ingress-tls ip "$admin_address"
            ;;
        *)
            die "load the explicit ingress mode before validating ingress TLS"
            ;;
    esac
}

validate_certbot_timer_state() {
    local profile="${1:-full}"
    if [[ "$profile" == management && "$PROBE_INGRESS_MODE" == ip ]]; then
        return 0
    fi
    local enabled_state active_state timer_unit
    timer_unit="$(runtime_certbot_timer)"
    enabled_state="$(systemctl is-enabled "$timer_unit" 2>/dev/null || true)"
    active_state="$(systemctl is-active "$timer_unit" 2>/dev/null || true)"
    case "$PROBE_INGRESS_MODE" in
        domain)
            [[ "$enabled_state" == enabled ]] ||
                die "domain mode requires $timer_unit to be enabled"
            [[ "$active_state" == active ]] ||
                die "domain mode requires $timer_unit to be active"
            ;;
        ip)
            [[ "$enabled_state" == disabled ]] ||
                die "IP mode requires $timer_unit to be disabled"
            [[ "$active_state" == inactive ]] ||
                die "IP mode requires $timer_unit to be inactive"
            ;;
        *)
            die "load the explicit ingress mode before validating $timer_unit"
            ;;
    esac
}

validate_static_artifact() {
    local name="$1" artifact_dir="$2"
    [[ -d "$artifact_dir" && ! -L "$artifact_dir" ]] || die "$name artifact directory is missing"
    [[ -s "$artifact_dir/index.html" && ! -L "$artifact_dir/index.html" ]] ||
        die "$name index.html is missing"
    if find "$artifact_dir" -type l -print -quit | grep -q .; then
        die "$name artifact contains a symbolic link"
    fi
    if find "$artifact_dir" -type f -name '*.map' -print -quit | grep -q .; then
        die "$name production artifact contains source maps"
    fi
}

validate_release_profile() {
    case "$1" in
        full|management) ;;
        *) die "unsupported release profile: $1" ;;
    esac
}

release_bundle_profile() {
    local bundle_root="$1" manifest="$1/RELEASE-MANIFEST" count profile
    if [[ ! -e "$manifest" && ! -L "$manifest" ]]; then
        printf '%s\n' full
        return 0
    fi
    [[ -f "$manifest" && ! -L "$manifest" ]] ||
        die "release manifest is not a safe regular file"
    count="$(awk '/^profile=/ { count++ } END { print count + 0 }' "$manifest")"
    if [[ "$count" == 0 ]]; then
        printf '%s\n' full
        return 0
    fi
    [[ "$count" == 1 ]] || die "release manifest must contain exactly one profile"
    profile="$(sed -n 's/^profile=//p' "$manifest")"
    validate_release_profile "$profile"
    printf '%s\n' "$profile"
}

validate_management_release_platform() {
    local bundle_root="$1" expected_platform="${2:-}"
    local manifest="$bundle_root/RELEASE-MANIFEST" abi_count platform_count abi platforms
    [[ -n "$expected_platform" ]] || expected_platform="$(runtime_platform_id)"
    validate_management_platform_id "$expected_platform"
    [[ -f "$manifest" && ! -L "$manifest" ]] ||
        die "management release manifest is missing or unsafe"
    abi_count="$(awk '/^runtime_abi=/ { count++ } END { print count + 0 }' "$manifest")"
    platform_count="$(awk '/^platform_ids=/ { count++ } END { print count + 0 }' "$manifest")"
    abi="$(sed -n 's/^runtime_abi=//p' "$manifest")"
    platforms="$(sed -n 's/^platform_ids=//p' "$manifest")"
    [[ "$abi_count" -eq 1 && "$abi" == "$PROBE_MANAGEMENT_RUNTIME_ABI" ]] ||
        die "management release runtime ABI is invalid"
    [[ "$platform_count" -eq 1 && "$platforms" == "$PROBE_MANAGEMENT_PLATFORM_IDS" ]] ||
        die "management release platform allowlist is invalid"
    case ",$platforms," in
        *",$expected_platform,"*) ;;
        *) die "management release does not allow platform $expected_platform" ;;
    esac
}

validate_release_artifacts() {
    local artifact_root="$1" profile="${2:-full}"
    validate_release_profile "$profile"
    [[ -x "$artifact_root/api/probe-api" ]] || die "staged probe-api binary is missing"
    validate_static_artifact probe-admin "$artifact_root/admin"
    [[ -d "$artifact_root/migrations" ]] || die "staged migrations are missing"
    if [[ "$profile" == management ]]; then
        [[ ! -e "$artifact_root/agent" && ! -L "$artifact_root/agent" ]] ||
            die "management release must not contain Agent artifacts"
        [[ ! -e "$artifact_root/web" && ! -L "$artifact_root/web" ]] ||
            die "management release must not contain visitor frontend artifacts"
    else
        local agent_download_root="$artifact_root/agent/downloads/probe-agent"
        [[ -x "$agent_download_root/install.sh" ]] || die "staged Agent installer is missing"
        [[ -x "$agent_download_root/linux-amd64/probe-agent" ]] || die "staged linux-amd64 Agent binary is missing"
        [[ -x "$agent_download_root/linux-arm64/probe-agent" ]] || die "staged linux-arm64 Agent binary is missing"
        [[ -s "$agent_download_root/probe-agent.service" && -s "$agent_download_root/SHA256SUMS" ]] ||
            die "staged Agent service or checksum manifest is missing"
        if find "$artifact_root/agent" -type l -print -quit | grep -q .; then
            die "staged Agent download artifact contains a symbolic link"
        fi
        (
            cd "$agent_download_root"
            sha256sum --check --strict SHA256SUMS
        )
        validate_static_artifact probe-web "$artifact_root/web"
    fi
    "$artifact_root/api/probe-api" version >/dev/null
}

# MANAGEMENT_BUNDLE_EXCLUDE_BUILD_BEGIN
copy_source_project() {
    local source_root="$1" project="$2" build_root="$3"
    mkdir -p -- "$build_root"
    cp -a -- "${source_root}/${project}" "${build_root}/${project}"
}

build_release_artifacts() {
    local source_root="$1" work_root="$2" run_tests="$3" release_id="$4"
    local build_root="${work_root}/source" artifact_root="${work_root}/artifacts"
    local agent_download_root="${artifact_root}/agent/downloads/probe-agent"
    mkdir -p -- "$build_root" "$artifact_root/api" \
        "$agent_download_root/linux-amd64" "$agent_download_root/linux-arm64" \
        "$artifact_root/web" "$artifact_root/admin" "$artifact_root/migrations"

    copy_source_project "$source_root" probe-api "$build_root"
    copy_source_project "$source_root" probe-agent "$build_root"
    copy_source_project "$source_root" probe-web "$build_root"
    copy_source_project "$source_root" probe-admin "$build_root"

    local built_at
    built_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

    log "validating and building probe-api"
    (
        cd "${build_root}/probe-api"
        if [[ "$run_tests" == true ]]; then
            go test ./...
            go vet ./...
        fi
        CGO_ENABLED=0 go build -trimpath \
            -ldflags "-s -w -X main.version=${release_id} -X main.commit=synchronized-source -X main.builtAt=${built_at}" \
            -o "${artifact_root}/api/probe-api" ./cmd/probe-api
    )

    log "validating and cross-building probe-agent independently"
    (
        cd "${build_root}/probe-agent"
        if [[ "$run_tests" == true ]]; then
            go test ./...
            go vet ./...
            sh deploy/tests/install-contract.sh
        fi
        GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${release_id}" \
            -o "${agent_download_root}/linux-amd64/probe-agent" ./cmd/probe-agent
        GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${release_id}" \
            -o "${agent_download_root}/linux-arm64/probe-agent" ./cmd/probe-agent
        install -m 0755 -- deploy/install.sh "${agent_download_root}/install.sh"
        install -m 0644 -- deploy/systemd/probe-agent.service "${agent_download_root}/probe-agent.service"
        (
            cd "$agent_download_root"
            sha256sum -- install.sh probe-agent.service linux-amd64/probe-agent linux-arm64/probe-agent > SHA256SUMS
        )
    )

    log "validating and building probe-web independently"
    (
        cd "${build_root}/probe-web"
        npm ci --no-audit --no-fund
        [[ "$run_tests" != true ]] || npm test
        npm run build
        cp -a -- dist/. "${artifact_root}/web/"
    )

    log "validating and building probe-admin independently"
    (
        cd "${build_root}/probe-admin"
        npm ci --no-audit --no-fund
        [[ "$run_tests" != true ]] || npm test
        npm run build
        cp -a -- dist/. "${artifact_root}/admin/"
    )

    cp -a -- "${build_root}/probe-api/migrations/." "${artifact_root}/migrations/"
    validate_release_artifacts "$artifact_root" full
}
# MANAGEMENT_BUNDLE_EXCLUDE_BUILD_END

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

validate_prebuilt_bundle() {
    local bundle_root="$1" profile="${2:-full}"
    validate_release_profile "$profile"
    [[ -d "$bundle_root" && ! -L "$bundle_root" ]] ||
        die "prebuilt bundle root is missing or unsafe: $bundle_root"
    [[ "$(release_bundle_profile "$bundle_root")" == "$profile" ]] ||
        die "prebuilt bundle profile changed during validation"
    if [[ "$profile" == management ]]; then
        validate_management_release_platform "$bundle_root"
    fi

    local required
    for required in \
        BUNDLE-SHA256SUMS \
        RELEASE-MANIFEST \
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
        source/probe-api/deploy/systemd/probe-api.service \
        source/probe-api/deploy/systemd/probe-api-legacy.service \
        source/probe-api/deploy/systemd/probe-postgres-backup.service \
        source/probe-api/deploy/systemd/probe-postgres-backup.timer \
        source/probe-api/deploy/systemd/probe-postgres-backup-legacy.service \
        source/probe-api/deploy/systemd/probe-postgres-backup-legacy.timer; do
        [[ -f "$bundle_root/$required" && ! -L "$bundle_root/$required" ]] ||
            die "prebuilt bundle is missing a required regular file: $required"
    done
    [[ "$(grep -Fxc "readonly PG_DUMP_BINARY='@PROBE_PG_DUMP@'" \
        "$bundle_root/source/probe-api/deploy/scripts/backup-postgres.sh" || :)" -eq 1 ]] ||
        die 'prebuilt PostgreSQL backup script has an invalid pg_dump render contract'
    [[ "$(grep -Fxc "readonly PSQL_BINARY='@PROBE_PSQL@'" \
        "$bundle_root/source/probe-api/deploy/scripts/restore-postgres.sh" || :)" -eq 1 ]] ||
        die 'prebuilt PostgreSQL restore script has an invalid psql render contract'
    for required in backup-postgres.sh restore-postgres.sh; do
        [[ "$(grep -Fxc "readonly PG_RESTORE_BINARY='@PROBE_PG_RESTORE@'" \
            "$bundle_root/source/probe-api/deploy/scripts/$required" || :)" -eq 1 ]] ||
            die "prebuilt PostgreSQL script has an invalid pg_restore render contract: $required"
    done
    local backup_tokens restore_tokens
    backup_tokens="$(grep -Eo '@PROBE_[A-Z0-9_]+@' \
        "$bundle_root/source/probe-api/deploy/scripts/backup-postgres.sh" | LC_ALL=C sort -u)"
    restore_tokens="$(grep -Eo '@PROBE_[A-Z0-9_]+@' \
        "$bundle_root/source/probe-api/deploy/scripts/restore-postgres.sh" | LC_ALL=C sort -u)"
    [[ "$backup_tokens" == $'@PROBE_PG_DUMP@\n@PROBE_PG_RESTORE@' ]] ||
        die 'prebuilt PostgreSQL backup script has an unexpected render-token set'
    [[ "$restore_tokens" == $'@PROBE_PG_RESTORE@\n@PROBE_PSQL@' ]] ||
        die 'prebuilt PostgreSQL restore script has an unexpected render-token set'
    if [[ "$profile" == full ]]; then
        for required in \
            artifacts/agent/downloads/probe-agent/install.sh \
            artifacts/agent/downloads/probe-agent/SHA256SUMS \
            artifacts/web/index.html \
            source/probe-api/deploy/scripts/build-release-bundles.sh \
            source/probe-api/deploy/nginx/nginx.conf \
            source/probe-api/deploy/nginx/nginx-ip.conf \
            source/probe-api/deploy/setup/probe-panel-finalizer.service; do
            [[ -f "$bundle_root/$required" && ! -L "$bundle_root/$required" ]] ||
                die "prebuilt full bundle is missing a required regular file: $required"
        done
    else
        for required in \
            source/probe-api/deploy/nginx/nginx-management.conf \
            source/probe-api/deploy/nginx/nginx-management-ip.conf \
            source/probe-api/deploy/nginx/nginx-management-legacy.conf \
            source/probe-api/deploy/nginx/nginx-management-ip-legacy.conf \
            source/probe-api/deploy/nginx/nginx-management-classic.conf \
            source/probe-api/deploy/nginx/nginx-management-ip-classic.conf \
            source/probe-api/deploy/setup/probe-panel-finalizer-management.service \
            source/probe-api/deploy/setup/probe-panel-finalizer-management-legacy.service \
            source/probe-api/deploy/setup/probe-panel-setup-legacy.service \
            source/probe-api/deploy/setup/probe-panel-setup-legacy.socket; do
            [[ -f "$bundle_root/$required" && ! -L "$bundle_root/$required" ]] ||
                die "prebuilt management bundle is missing a required regular file: $required"
        done
        for required in \
            source/probe-api/deploy/nginx/nginx.conf \
            source/probe-api/deploy/nginx/nginx-ip.conf \
            source/probe-api/deploy/setup/probe-panel-finalizer.service \
            source/probe-api/deploy/scripts/build-release-bundles.sh \
            source/probe-api/deploy/scripts/install.sh \
            source/probe-api/deploy/scripts/upgrade.sh \
            source/probe-api/deploy/scripts/validate-production.sh; do
            [[ ! -e "$bundle_root/$required" && ! -L "$bundle_root/$required" ]] ||
                die "prebuilt management bundle contains a forbidden source/build asset: $required"
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
            cd "$bundle_root"
            find source/probe-api/deploy -type f -print | LC_ALL=C sort
        )"
        [[ "$actual_deploy_files" == "$expected_deploy_files" ]] ||
            die "prebuilt management deploy assets differ from the reviewed runtime allowlist"

        if ! management_deploy_helper_is_clean \
            "$bundle_root/source/probe-api/deploy/scripts/deploy-common.sh"; then
            die "prebuilt management deploy-common contains forbidden full, Agent-artifact, visitor, or build logic"
        fi
    fi

    if find "$bundle_root" -type l -print -quit | grep -q .; then
        die "prebuilt bundle contains a symbolic link"
    fi
    (
        cd "$bundle_root"
        sha256sum --check --strict BUNDLE-SHA256SUMS
    ) || die "prebuilt bundle checksum verification failed"

    local expected_paths manifest_paths
    expected_paths="$(
        cd "$bundle_root"
        find artifacts setup source -type f -print | LC_ALL=C sort
    )"
    manifest_paths="$(awk '{ print $2 }' "$bundle_root/BUNDLE-SHA256SUMS" | LC_ALL=C sort)"
    [[ -n "$expected_paths" && "$manifest_paths" == "$expected_paths" ]] ||
        die "BUNDLE-SHA256SUMS must cover every artifacts, setup, and source file exactly once"

    validate_release_artifacts "$bundle_root/artifacts" "$profile"
    if [[ "$profile" == management ]]; then
        validate_management_nginx_template_contract \
            "$bundle_root/source/probe-api/deploy/nginx/nginx-management.conf" domain modern
        validate_management_nginx_template_contract \
            "$bundle_root/source/probe-api/deploy/nginx/nginx-management-ip.conf" ip modern
        validate_management_nginx_template_contract \
            "$bundle_root/source/probe-api/deploy/nginx/nginx-management-legacy.conf" domain legacy
        validate_management_nginx_template_contract \
            "$bundle_root/source/probe-api/deploy/nginx/nginx-management-ip-legacy.conf" ip legacy
        validate_management_nginx_template_contract \
            "$bundle_root/source/probe-api/deploy/nginx/nginx-management-classic.conf" domain classic
        validate_management_nginx_template_contract \
            "$bundle_root/source/probe-api/deploy/nginx/nginx-management-ip-classic.conf" ip classic
    else
        validate_nginx_template_contract "$bundle_root/source/probe-api/deploy/nginx/nginx.conf" domain
        validate_nginx_template_contract "$bundle_root/source/probe-api/deploy/nginx/nginx-ip.conf" ip
    fi
    validate_systemd_unit_source "$bundle_root/source/probe-api/deploy/systemd/probe-api.service"
    validate_backup_unit_source \
        "$bundle_root/source/probe-api/deploy/systemd/probe-postgres-backup.service" \
        "$bundle_root/source/probe-api/deploy/systemd/probe-postgres-backup.timer"
    validate_systemd_unit_source "$bundle_root/source/probe-api/deploy/systemd/probe-api-legacy.service"
    validate_backup_unit_source \
        "$bundle_root/source/probe-api/deploy/systemd/probe-postgres-backup-legacy.service" \
        "$bundle_root/source/probe-api/deploy/systemd/probe-postgres-backup-legacy.timer"
}

install_example_file() {
    local source="$1" destination="$2" mode="$3" group="$4"
    local temporary="${destination}.new.$$"
    install -o root -g "$group" -m "$mode" -- "$source" "$temporary"
    mv -Tf -- "$temporary" "$destination"
}

prepare_system_layout() {
    local source_root="$1" profile="${2:-full}"

    prepare_probe_api_service_account

    install -d -o root -g root -m 0755 "$PROBE_ROOT" "$PROBE_API_DIR" "$PROBE_RELEASES_DIR"
    install -d -o root -g probe-api -m 0750 "$PROBE_BACKUP_SCRIPT_DIR"
    install -d -o root -g probe-api -m 0750 "$PROBE_CONFIG_DIR"
    install -d -o root -g root -m 0755 "$PROBE_NGINX_CONFIG_DIR"
    install -d -o root -g root -m 0700 "$PROBE_BACKUPS_DIR"
    install -d -o probe-api -g probe-api -m 0700 "$PROBE_POSTGRES_BACKUP_DIR"
    # Nginx needs directory traversal to serve the public IP-mode CA. Active
    # secrets below this root keep their own fail-closed file permissions.
    [[ ! -L /etc/probe-panel && ( ! -e /etc/probe-panel || -d /etc/probe-panel ) ]] ||
        die "/etc/probe-panel must be a real directory"
    install -d -o root -g root -m 0755 /etc/probe-panel
    [[ ! -L /etc/probe-panel && "$(stat -c '%u:%g:%a' /etc/probe-panel)" == 0:0:755 ]] ||
        die "/etc/probe-panel must be a root:root directory with mode 0755"
    install -d -o root -g root -m 0755 /etc/probe-panel/tls /etc/probe-panel/tls/panel /etc/probe-panel/tls/admin /etc/probe-panel/tls/api

    if [[ ! -e "$PROBE_ALLOWLIST_FILE" ]]; then
        install -o root -g probe-api -m 0640 /dev/null "$PROBE_ALLOWLIST_FILE"
    fi

    install_example_file "${source_root}/probe-api/config/probe-api.env.example" \
        "${PROBE_CONFIG_DIR}/probe-api.env.example" 0640 probe-api
    install_example_file "${source_root}/probe-api/config/probe-postgres-backup.env.example" \
        "${PROBE_CONFIG_DIR}/probe-postgres-backup.env.example" 0600 root
    if [[ "$profile" == management ]]; then
        local domain_template="nginx-management.conf" ip_template="nginx-management-ip.conf" nginx_dialect
        nginx_dialect="$(management_platform_nginx_dialect "$(runtime_platform_id)")"
        case "$nginx_dialect" in
            classic)
                domain_template="nginx-management-classic.conf"
                ip_template="nginx-management-ip-classic.conf"
                ;;
            legacy)
                domain_template="nginx-management-legacy.conf"
                ip_template="nginx-management-ip-legacy.conf"
                ;;
            modern) ;;
            *) die "unsupported management Nginx dialect: $nginx_dialect" ;;
        esac
        install_example_file "${source_root}/probe-api/deploy/nginx/${domain_template}" \
            "${PROBE_NGINX_CONFIG_DIR}/nginx-management.conf.example" 0644 root
        install_example_file "${source_root}/probe-api/deploy/nginx/${ip_template}" \
            "${PROBE_NGINX_CONFIG_DIR}/nginx-management-ip.conf.example" 0644 root
    else
        install_example_file "${source_root}/probe-api/deploy/nginx/nginx.conf" \
            "${PROBE_NGINX_CONFIG_DIR}/nginx.conf.example" 0644 root
        install_example_file "${source_root}/probe-api/deploy/nginx/nginx-ip.conf" \
            "${PROBE_NGINX_CONFIG_DIR}/nginx-ip.conf.example" 0644 root
    fi
}

selected_systemd_asset_name() {
    local modern_name="$1" systemd_profile
    case "$modern_name" in
        probe-api.service|probe-postgres-backup.service|probe-postgres-backup.timer) ;;
        *) die "unsupported Probe Panel systemd asset: $modern_name" ;;
    esac
    systemd_profile="$(runtime_systemd_profile)"
    case "$systemd_profile" in
        legacy) printf '%s-legacy.%s\n' "${modern_name%.*}" "${modern_name##*.}" ;;
        modern) printf '%s\n' "$modern_name" ;;
        *) die "unsupported runtime systemd profile: $systemd_profile" ;;
    esac
}

management_service_asset_paths() {
    printf '%s\n' \
        "$PROBE_SYSTEMD_UNIT" \
        "$PROBE_BACKUP_SERVICE_UNIT" \
        "$PROBE_BACKUP_TIMER_UNIT" \
        "$PROBE_BACKUP_SCRIPT_DIR/backup-postgres.sh" \
        "$PROBE_BACKUP_SCRIPT_DIR/restore-postgres.sh" \
        "$PROBE_MANAGEMENT_DEPLOY_COMMON" \
        "$PROBE_MANAGEMENT_VALIDATE" \
        "$PROBE_MANAGEMENT_RESTORE" \
        "$PROBE_MANAGEMENT_UNINSTALL" \
        "$PROBE_MANAGEMENT_LIFECYCLE_MANIFEST" \
        "$PROBE_NGINX_LINK" \
        "$PROBE_API_WANTS_LINK" \
        "$PROBE_BACKUP_TIMER_WANTS_LINK" \
        "$PROBE_NGINX_WANTS_LINK"
}

validate_management_service_asset_kind() {
    local path="$1" expected_path="${2:-$1}"
    case "$expected_path" in
        "$PROBE_NGINX_LINK"|"$PROBE_API_WANTS_LINK"|"$PROBE_BACKUP_TIMER_WANTS_LINK"|"$PROBE_NGINX_WANTS_LINK")
            [[ -L "$path" ]] ||
                die "management activation asset must be a symbolic link: $path"
            ;;
        *)
            [[ -f "$path" && ! -L "$path" ]] ||
                die "management service asset must be a regular file: $path"
            ;;
    esac
}

management_service_asset_kind_matches() {
    local path="$1" expected_path="${2:-$1}"
    case "$expected_path" in
        "$PROBE_NGINX_LINK"|"$PROBE_API_WANTS_LINK"|"$PROBE_BACKUP_TIMER_WANTS_LINK"|"$PROBE_NGINX_WANTS_LINK")
            [[ -L "$path" ]]
            ;;
        *)
            [[ -f "$path" && ! -L "$path" ]]
            ;;
    esac
}

validate_management_service_asset_snapshot_root() {
    local snapshot_root="$1"
    case "$snapshot_root" in
        /var/tmp/probe-service-assets.*) ;;
        *) die "refusing unexpected management service-asset snapshot path: $snapshot_root" ;;
    esac
    [[ -d "$snapshot_root" && ! -L "$snapshot_root" ]] ||
        die "management service-asset snapshot is missing or unsafe: $snapshot_root"
    [[ "$(stat -c '%u:%g:%a' -- "$snapshot_root")" == 0:0:700 ]] ||
        die "management service-asset snapshot must be root:root mode 0700: $snapshot_root"
}

remove_management_service_asset_temporaries() {
    local path failed=0
    while IFS= read -r path; do
        if ! rm -f -- "${path}.new.$$"; then
            warn "could not remove management service-asset temporary: ${path}.new.$$"
            failed=1
        fi
    done < <(management_service_asset_paths)
    return "$failed"
}

cleanup_failed_service_asset_snapshot() {
    local status=$?
    trap - EXIT
    trap '' HUP INT TERM
    if (( status != 0 )) && [[ -n "${SERVICE_ASSET_SNAPSHOT_PENDING:-}" ]]; then
        case "$SERVICE_ASSET_SNAPSHOT_PENDING" in
                /var/tmp/probe-service-assets.*)
                if [[ ! -L "$SERVICE_ASSET_SNAPSHOT_PENDING" ]]; then
                    rm -rf -- "$SERVICE_ASSET_SNAPSHOT_PENDING" ||
                        warn "could not clean failed service-asset snapshot: $SERVICE_ASSET_SNAPSHOT_PENDING"
                fi
                ;;
        esac
    fi
    exit "$status"
}

snapshot_management_service_assets() (
    set -Eeuo pipefail
    local snapshot_root path state index=0
    trap cleanup_failed_service_asset_snapshot EXIT
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM
    snapshot_root="$(mktemp -d /var/tmp/probe-service-assets.XXXXXX)"
    SERVICE_ASSET_SNAPSHOT_PENDING="$snapshot_root"
    chown root:root "$snapshot_root"
    chmod 0700 "$snapshot_root"
    while IFS= read -r path; do
        state="$snapshot_root/state-$index"
        if [[ -e "$path" || -L "$path" ]]; then
            validate_management_service_asset_kind "$path"
            cp -a -- "$path" "$snapshot_root/item-$index"
            printf '%s\n' present > "$state"
        else
            printf '%s\n' absent > "$state"
        fi
        chmod 0600 "$state"
        ((index += 1))
    done < <(management_service_asset_paths)
    [[ "$index" -eq 14 ]] ||
        die 'management service-asset path inventory changed unexpectedly'
    trap - EXIT HUP INT TERM
    SERVICE_ASSET_SNAPSHOT_PENDING=""
    printf '%s\n' "$snapshot_root"
)

restore_management_service_assets() {
    local snapshot_root="$1" path state item temporary index=0 failed=0
    validate_management_service_asset_snapshot_root "$snapshot_root"

    # Validate the complete journal before changing the first live path.
    while IFS= read -r path; do
        state="$snapshot_root/state-$index"
        item="$snapshot_root/item-$index"
        [[ -f "$state" && ! -L "$state" ]] ||
            die "management service-asset snapshot state is missing: $state"
        case "$(<"$state")" in
            present)
                validate_management_service_asset_kind "$item" "$path"
                ;;
            absent)
                [[ ! -e "$item" && ! -L "$item" ]] ||
                    die "absent management service asset has unexpected snapshot data: $item"
                ;;
            *) die "management service-asset snapshot state is invalid: $state" ;;
        esac
        ((index += 1))
    done < <(management_service_asset_paths)
    [[ "$index" -eq 14 ]] ||
        die 'management service-asset restore inventory changed unexpectedly'

    if ! remove_management_service_asset_temporaries; then
        failed=1
    fi
    index=0
    while IFS= read -r path; do
        state="$snapshot_root/state-$index"
        item="$snapshot_root/item-$index"
        case "$(<"$state")" in
            present)
                temporary="${path}.new.$$"
                if ! cp -a -- "$item" "$temporary"; then
                    warn "could not stage prior management service asset: $path"
                    failed=1
                elif ! mv -Tf -- "$temporary" "$path"; then
                    warn "could not restore prior management service asset: $path"
                    failed=1
                    rm -f -- "$temporary" || :
                fi
                ;;
            absent)
                if [[ -e "$path" || -L "$path" ]]; then
                    if ! management_service_asset_kind_matches "$path"; then
                        warn "refusing to remove an unexpected rollback asset kind: $path"
                        failed=1
                    elif ! rm -f -- "$path"; then
                        warn "could not remove newly installed management service asset: $path"
                        failed=1
                    fi
                fi
                ;;
        esac
        ((index += 1))
    done < <(management_service_asset_paths)
    if (( failed == 0 )); then
        if ! rm -rf -- "$snapshot_root"; then
            warn "could not remove restored management service-asset snapshot: $snapshot_root"
            failed=1
        fi
    fi
    return "$failed"
}

discard_management_service_asset_snapshot() {
    local snapshot_root="$1"
    validate_management_service_asset_snapshot_root "$snapshot_root"
    remove_management_service_asset_temporaries
    rm -rf -- "$snapshot_root"
}

management_systemd_property() {
    local unit="$1" property="$2" output
    [[ "$property" =~ ^[A-Za-z][A-Za-z0-9]*$ ]] || return 1
    output="$(systemctl show --property="$property" "$unit")" || return 1
    [[ "$output" == "$property="* && "$output" != *$'\n'* ]] || return 1
    printf '%s\n' "${output#*=}"
}

capture_management_unit_activity() {
    local destination="$1" unit="$2" load_state active_state value
    load_state="$(management_systemd_property "$unit" LoadState)" ||
        die "could not read LoadState for $unit"
    active_state="$(management_systemd_property "$unit" ActiveState)" ||
        die "could not read ActiveState for $unit"
    case "$load_state:$active_state" in
        loaded:active) value=1 ;;
        loaded:inactive|not-found:inactive) value=0 ;;
        *) die "refusing activation while $unit has unsupported state $load_state/$active_state" ;;
    esac
    printf -v "$destination" '%s' "$value"
}

capture_management_service_activity() {
    capture_management_unit_activity MANAGEMENT_API_WAS_ACTIVE probe-api.service
    capture_management_unit_activity MANAGEMENT_BACKUP_TIMER_WAS_ACTIVE probe-postgres-backup.timer
    capture_management_unit_activity MANAGEMENT_NGINX_WAS_ACTIVE nginx.service
    capture_management_unit_activity MANAGEMENT_POSTGRES_WAS_ACTIVE "$(runtime_postgres_service)"
}

stop_management_unit_to_inactive() {
    local unit="$1" load_state active_state
    if systemctl stop "$unit" >/dev/null 2>&1; then
        return 0
    fi
    load_state="$(management_systemd_property "$unit" LoadState)" || return 1
    active_state="$(management_systemd_property "$unit" ActiveState)" || return 1
    [[ "$load_state:$active_state" == not-found:inactive ]]
}

restore_management_service_activity() {
    local unit expected failed=0
    for expected in \
        "$MANAGEMENT_API_WAS_ACTIVE" \
        "$MANAGEMENT_BACKUP_TIMER_WAS_ACTIVE" \
        "$MANAGEMENT_NGINX_WAS_ACTIVE" \
        "$MANAGEMENT_POSTGRES_WAS_ACTIVE"; do
        [[ "$expected" == 0 || "$expected" == 1 ]] ||
            die 'management service activity snapshot is incomplete'
    done

    if ! systemctl daemon-reload; then
        warn 'could not reload systemd while restoring prior management activity'
        failed=1
    fi
    unit="$(runtime_postgres_service)"
    if [[ "$MANAGEMENT_POSTGRES_WAS_ACTIVE" == 1 ]]; then
        if ! systemctl start "$unit"; then warn "could not restart prior $unit"; failed=1; fi
    else
        if ! stop_management_unit_to_inactive "$unit"; then warn "could not stop newly activated $unit"; failed=1; fi
    fi
    if [[ "$MANAGEMENT_API_WAS_ACTIVE" == 1 ]]; then
        if ! systemctl restart probe-api.service; then warn 'could not restart prior probe-api.service'; failed=1; fi
    else
        if ! stop_management_unit_to_inactive probe-api.service; then warn 'could not stop newly activated probe-api.service'; failed=1; fi
    fi
    if [[ "$MANAGEMENT_BACKUP_TIMER_WAS_ACTIVE" == 1 ]]; then
        if ! systemctl restart probe-postgres-backup.timer; then warn 'could not restart prior backup timer'; failed=1; fi
    else
        if ! stop_management_unit_to_inactive probe-postgres-backup.timer; then warn 'could not stop newly activated backup timer'; failed=1; fi
    fi
    if [[ "$MANAGEMENT_NGINX_WAS_ACTIVE" == 1 ]]; then
        if ! nginx -t; then
            warn 'restored Nginx configuration failed validation'
            failed=1
        elif ! systemctl reload-or-restart nginx.service; then
            warn 'could not restore prior Nginx activity'
            failed=1
        fi
    else
        if ! stop_management_unit_to_inactive nginx.service; then warn 'could not stop newly activated Nginx'; failed=1; fi
    fi
    return "$failed"
}

restore_management_postgres_activity() {
    local unit failed=0
    [[ "$MANAGEMENT_POSTGRES_WAS_ACTIVE" == 0 ||
       "$MANAGEMENT_POSTGRES_WAS_ACTIVE" == 1 ]] ||
        die 'management PostgreSQL activity snapshot is incomplete'
    unit="$(runtime_postgres_service)"
    if [[ "$MANAGEMENT_POSTGRES_WAS_ACTIVE" == 1 ]]; then
        if ! systemctl start "$unit"; then warn "could not restart prior $unit"; failed=1; fi
    else
        if ! stop_management_unit_to_inactive "$unit"; then warn "could not stop newly activated $unit"; failed=1; fi
    fi
    return "$failed"
}

run_management_rollback_step() {
    local description="$1" step_status
    shift
    set +e
    (
        trap - EXIT
        trap '' HUP INT TERM
        set -Eeuo pipefail
        "$@"
    )
    step_status=$?
    set -e
    if (( step_status != 0 )); then
        warn "$description (exit $step_status)"
    fi
    return 0
}

rollback_pending_management_activation() {
    local rollback_state="${MANAGEMENT_ACTIVATION_ROLLBACK_STATE:-none}"
    local snapshot="${MANAGEMENT_ROLLBACK_SERVICE_ASSET_SNAPSHOT:-}"
    [[ "$rollback_state" != none ]] || return 0

    # Clear the journal first so a failed rollback cannot recurse through EXIT.
    MANAGEMENT_ACTIVATION_ROLLBACK_STATE="none"
    MANAGEMENT_ROLLBACK_SERVICE_ASSET_SNAPSHOT=""
    warn "management release activation did not commit; restoring the prior host state"

    if [[ "$rollback_state" == links || "$rollback_state" == runtime ]]; then
        run_management_rollback_step \
            'could not completely restore the prior application links' \
            restore_release_links \
            "$MANAGEMENT_ROLLBACK_RELEASE_PROFILE" \
            "$MANAGEMENT_ROLLBACK_OLD_API" \
            "$MANAGEMENT_ROLLBACK_OLD_AGENT" \
            "$MANAGEMENT_ROLLBACK_OLD_WEB" \
            "$MANAGEMENT_ROLLBACK_OLD_ADMIN" \
            "$MANAGEMENT_ROLLBACK_OLD_MIGRATIONS"
        if [[ -n "$snapshot" ]]; then
            run_management_rollback_step \
                "could not completely restore host assets from $snapshot" \
                restore_management_service_assets "$snapshot"
        fi
        run_management_rollback_step \
            'systemd daemon-reload failed while rolling back management activation' \
            systemctl daemon-reload
    fi

    if [[ "$rollback_state" == postgres ]]; then
        run_management_rollback_step \
            'could not restore the prior PostgreSQL activity' \
            restore_management_postgres_activity
    else
        run_management_rollback_step \
            'could not completely restore the prior service activity' \
            restore_management_service_activity
    fi
}

install_service_assets() {
    local source_root="$1"
    local api_asset backup_service_asset backup_timer_asset postgres_service systemd_profile
    api_asset="$(selected_systemd_asset_name probe-api.service)"
    backup_service_asset="$(selected_systemd_asset_name probe-postgres-backup.service)"
    backup_timer_asset="$(selected_systemd_asset_name probe-postgres-backup.timer)"
    postgres_service="$(runtime_postgres_service)"
    systemd_profile="$(runtime_systemd_profile)"
    require_runtime_postgres_commands

    # Prepare every replacement beside its live target and validate the whole
    # set before the first live rename.  The caller owns a snapshot journal and
    # restores it if any staging, activation, or runtime check fails.
    remove_management_service_asset_temporaries
    local unit_tmp="${PROBE_SYSTEMD_UNIT}.new.$$"
    sed "s/postgresql[.]service/${postgres_service}/g" \
        "${source_root}/probe-api/deploy/systemd/$api_asset" > "$unit_tmp"
    chown root:root "$unit_tmp"
    chmod 0644 "$unit_tmp"

    local backup_script restore_script backup_service_tmp backup_timer_tmp
    local pg_dump_binary pg_restore_binary psql_binary postgres_script actual_tokens
    backup_script="${PROBE_BACKUP_SCRIPT_DIR}/backup-postgres.sh"
    restore_script="${PROBE_BACKUP_SCRIPT_DIR}/restore-postgres.sh"
    pg_dump_binary="$(runtime_postgres_command pg_dump)"
    pg_restore_binary="$(runtime_postgres_command pg_restore)"
    psql_binary="$(runtime_postgres_command psql)"
    [[ "$(grep -Fxc "readonly PG_DUMP_BINARY='@PROBE_PG_DUMP@'" \
        "${source_root}/probe-api/deploy/scripts/backup-postgres.sh")" -eq 1 ]] ||
        die 'PostgreSQL backup source is missing its exact pg_dump render token'
    for postgres_script in backup-postgres.sh restore-postgres.sh; do
        [[ "$(grep -Fxc "readonly PG_RESTORE_BINARY='@PROBE_PG_RESTORE@'" \
            "${source_root}/probe-api/deploy/scripts/${postgres_script}")" -eq 1 ]] ||
            die "PostgreSQL source is missing its exact pg_restore render token: $postgres_script"
    done
    [[ "$(grep -Fxc "readonly PSQL_BINARY='@PROBE_PSQL@'" \
        "${source_root}/probe-api/deploy/scripts/restore-postgres.sh")" -eq 1 ]] ||
        die 'PostgreSQL restore source is missing its exact psql render token'
    actual_tokens="$(grep -Eo '@PROBE_[A-Z0-9_]+@' \
        "${source_root}/probe-api/deploy/scripts/backup-postgres.sh" | LC_ALL=C sort -u)"
    [[ "$actual_tokens" == $'@PROBE_PG_DUMP@\n@PROBE_PG_RESTORE@' ]] ||
        die 'PostgreSQL backup source has an unexpected render-token set'
    actual_tokens="$(grep -Eo '@PROBE_[A-Z0-9_]+@' \
        "${source_root}/probe-api/deploy/scripts/restore-postgres.sh" | LC_ALL=C sort -u)"
    [[ "$actual_tokens" == $'@PROBE_PG_RESTORE@\n@PROBE_PSQL@' ]] ||
        die 'PostgreSQL restore source has an unexpected render-token set'
    sed \
        -e "s#@PROBE_PG_DUMP@#${pg_dump_binary}#g" \
        -e "s#@PROBE_PG_RESTORE@#${pg_restore_binary}#g" \
        "${source_root}/probe-api/deploy/scripts/backup-postgres.sh" > "${backup_script}.new.$$"
    sed \
        -e "s#@PROBE_PSQL@#${psql_binary}#g" \
        -e "s#@PROBE_PG_RESTORE@#${pg_restore_binary}#g" \
        "${source_root}/probe-api/deploy/scripts/restore-postgres.sh" > "${restore_script}.new.$$"
    chown root:probe-api "${backup_script}.new.$$" "${restore_script}.new.$$"
    chmod 0750 "${backup_script}.new.$$" "${restore_script}.new.$$"
    ! grep -Fq '@PROBE_' "${backup_script}.new.$$" "${restore_script}.new.$$" ||
        die 'a PostgreSQL command render token remained in an installed backup script'

    backup_service_tmp="${PROBE_BACKUP_SERVICE_UNIT}.new.$$"
    backup_timer_tmp="${PROBE_BACKUP_TIMER_UNIT}.new.$$"
    sed "s/postgresql[.]service/${postgres_service}/g" \
        "${source_root}/probe-api/deploy/systemd/$backup_service_asset" > "$backup_service_tmp"
    chown root:root "$backup_service_tmp"
    chmod 0644 "$backup_service_tmp"
    install -o root -g root -m 0644 -- \
        "${source_root}/probe-api/deploy/systemd/$backup_timer_asset" "$backup_timer_tmp"

    # Keep the management-only lifecycle tools on the installed host.  They
    # consume the same reviewed runtime helper as install-release.sh, so host
    # validation and ordinary uninstall never fall back to the historical
    # Debian-13/source-built deployment path.
    install -d -o root -g root -m 0755 -- "$PROBE_MANAGEMENT_LIB_DIR"
    local lifecycle_name lifecycle_source lifecycle_destination lifecycle_tmp
    local lifecycle_hash lifecycle_manifest_tmp
    for lifecycle_name in \
        deploy-common.sh validate-management.sh restore-management.sh uninstall-management.sh; do
        lifecycle_source="${source_root}/probe-api/deploy/scripts/${lifecycle_name}"
        lifecycle_destination="${PROBE_MANAGEMENT_LIB_DIR}/${lifecycle_name}"
        lifecycle_tmp="${lifecycle_destination}.new.$$"
        install -o root -g root -m 0755 -- "$lifecycle_source" "$lifecycle_tmp"
    done
    lifecycle_manifest_tmp="${PROBE_MANAGEMENT_LIFECYCLE_MANIFEST}.new.$$"
    : > "$lifecycle_manifest_tmp"
    for lifecycle_name in \
        deploy-common.sh validate-management.sh restore-management.sh uninstall-management.sh; do
        lifecycle_tmp="${PROBE_MANAGEMENT_LIB_DIR}/${lifecycle_name}.new.$$"
        lifecycle_hash="$(sha256sum -- "$lifecycle_tmp")"
        lifecycle_hash="${lifecycle_hash%% *}"
        printf '%s  %s\n' "$lifecycle_hash" "$lifecycle_name" >> "$lifecycle_manifest_tmp"
    done
    chown root:root "$lifecycle_manifest_tmp"
    chmod 0644 "$lifecycle_manifest_tmp"

    local nginx_link_tmp="${PROBE_NGINX_LINK}.new.$$"
    if [[ -e "$PROBE_NGINX_LINK" || -L "$PROBE_NGINX_LINK" ]]; then
        [[ -L "$PROBE_NGINX_LINK" ]] || die "$PROBE_NGINX_LINK exists and is not a symbolic link"
        [[ "$(readlink -f -- "$PROBE_NGINX_LINK")" == "$PROBE_ACTIVE_NGINX_CONFIG" ]] ||
            die "$PROBE_NGINX_LINK points to an unexpected file"
    else
        ln -s -- "$PROBE_ACTIVE_NGINX_CONFIG" "$nginx_link_tmp"
    fi

    validate_systemd_unit_source "$unit_tmp" "$systemd_profile"
    validate_backup_unit_source "$backup_service_tmp" "$backup_timer_tmp" "$systemd_profile"
    for postgres_script in "${backup_script}.new.$$" "${restore_script}.new.$$"; do
        [[ "$(stat -c '%U:%G:%a' -- "$postgres_script")" == root:probe-api:750 ]] ||
            die "staged PostgreSQL script has an unsafe owner or mode: $postgres_script"
        bash -n -- "$postgres_script" || die "staged PostgreSQL script has invalid syntax: $postgres_script"
    done
    for lifecycle_name in \
        deploy-common.sh validate-management.sh restore-management.sh uninstall-management.sh; do
        lifecycle_tmp="${PROBE_MANAGEMENT_LIB_DIR}/${lifecycle_name}.new.$$"
        [[ "$(stat -c '%U:%G:%a' -- "$lifecycle_tmp")" == root:root:755 ]] ||
            die "staged management lifecycle tool has an unsafe owner or mode: $lifecycle_name"
        bash -n -- "$lifecycle_tmp" ||
             die "staged management lifecycle tool has invalid syntax: $lifecycle_name"
    done
    validate_management_lifecycle_manifest "$lifecycle_manifest_tmp" ".new.$$"
    if [[ -L "$nginx_link_tmp" ]]; then
        [[ "$(readlink -- "$nginx_link_tmp")" == "$PROBE_ACTIVE_NGINX_CONFIG" ]] ||
            die 'staged management Nginx link has an unexpected target'
    fi

    mv -Tf -- "$unit_tmp" "$PROBE_SYSTEMD_UNIT"
    mv -Tf -- "$backup_service_tmp" "$PROBE_BACKUP_SERVICE_UNIT"
    mv -Tf -- "$backup_timer_tmp" "$PROBE_BACKUP_TIMER_UNIT"
    mv -Tf -- "${backup_script}.new.$$" "$backup_script"
    mv -Tf -- "${restore_script}.new.$$" "$restore_script"
    # Commit direct entrypoints before their shared helper.  Until the manifest
    # is committed last, every new entrypoint fails its pre-source group check;
    # any old entrypoint can therefore only observe its old shared helper.
    for lifecycle_name in \
        validate-management.sh restore-management.sh uninstall-management.sh deploy-common.sh; do
        lifecycle_destination="${PROBE_MANAGEMENT_LIB_DIR}/${lifecycle_name}"
        mv -Tf -- "${lifecycle_destination}.new.$$" "$lifecycle_destination"
    done
    mv -Tf -- "$lifecycle_manifest_tmp" "$PROBE_MANAGEMENT_LIFECYCLE_MANIFEST"
    if [[ -L "$nginx_link_tmp" ]]; then
        mv -Tf -- "$nginx_link_tmp" "$PROBE_NGINX_LINK"
    fi
}

validate_systemd_unit_source() {
    local unit_file="$1" unit_profile="${2:-}"
    if [[ -z "$unit_profile" ]]; then
        unit_profile=modern
        [[ "${unit_file##*/}" != *-legacy.service ]] || unit_profile=legacy
    fi
    [[ "$unit_profile" == modern || "$unit_profile" == legacy ]] ||
        die "unknown probe-api systemd unit profile: $unit_profile"
    [[ -f "$unit_file" && ! -L "$unit_file" ]] || die "probe-api systemd unit is missing"
    grep -Fxq 'User=probe-api' "$unit_file" || die "probe-api unit must use the probe-api account"
    grep -Fxq 'Group=probe-api' "$unit_file" || die "probe-api unit must use the probe-api group"
    grep -Fxq 'EnvironmentFile=/srv/probe/config/probe-api.env' "$unit_file" ||
        die "probe-api unit has an unexpected EnvironmentFile"
    grep -Fxq 'ExecStart=/srv/probe/api/probe-api serve' "$unit_file" ||
        die "probe-api unit has an unexpected ExecStart"
    grep -Fxq 'NoNewPrivileges=true' "$unit_file" || die "probe-api unit must enable NoNewPrivileges"
    grep -Fxq 'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6' "$unit_file" ||
        die "probe-api unit must restrict its address families"
    if [[ "$unit_profile" == modern ]]; then
        grep -Fxq 'ProtectSystem=strict' "$unit_file" || die "modern probe-api unit must strictly protect the system"
        grep -Fxq 'SocketBindAllow=tcp:8080' "$unit_file" || die "probe-api unit must allow only its loopback port"
        grep -Fxq 'SocketBindDeny=any' "$unit_file" || die "probe-api unit must deny other bind operations"
    else
        grep -Fxq 'ProtectSystem=full' "$unit_file" || die "legacy probe-api unit must protect the system"
        ! grep -Eq '^(SocketBindAllow|SocketBindDeny|ProtectProc|ProtectClock|ProtectKernelLogs|ProtectHostname|StateDirectory|RuntimeDirectoryMode)=' "$unit_file" ||
            die "legacy probe-api unit contains a directive newer than systemd 219"
    fi
}

validate_backup_unit_source() {
    local service_file="$1" timer_file="$2" unit_profile="${3:-}"
    if [[ -z "$unit_profile" ]]; then
        unit_profile=modern
        [[ "${service_file##*/}" != *-legacy.service ]] || unit_profile=legacy
    fi
    [[ "$unit_profile" == modern || "$unit_profile" == legacy ]] ||
        die "unknown PostgreSQL backup systemd unit profile: $unit_profile"
    [[ -f "$service_file" && ! -L "$service_file" ]] || die "PostgreSQL backup service unit is missing"
    [[ -f "$timer_file" && ! -L "$timer_file" ]] || die "PostgreSQL backup timer unit is missing"
    grep -Fxq 'User=probe-api' "$service_file" || die "PostgreSQL backup service must use the probe-api account"
    grep -Fxq 'Group=probe-api' "$service_file" || die "PostgreSQL backup service must use the probe-api group"
    grep -Fxq 'EnvironmentFile=/srv/probe/config/probe-postgres-backup.env' "$service_file" ||
        die "PostgreSQL backup service has an unexpected EnvironmentFile"
    grep -Fxq 'Environment=PATH=/usr/pgsql-14/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin' "$service_file" ||
        die "PostgreSQL backup service has an unexpected command-path contract"
    grep -Fxq 'ExecStart=/srv/probe/api/scripts/backup-postgres.sh' "$service_file" ||
        die "PostgreSQL backup service has an unexpected ExecStart"
    grep -Fxq 'NoNewPrivileges=true' "$service_file" ||
        die "PostgreSQL backup service must enable NoNewPrivileges"
    if [[ "$unit_profile" == modern ]]; then
        grep -Fxq 'ProtectSystem=strict' "$service_file" ||
            die "PostgreSQL backup service must strictly protect the system filesystem"
    else
        grep -Fxq 'ProtectSystem=full' "$service_file" ||
            die "legacy PostgreSQL backup service must protect the system filesystem"
        ! grep -Eq '^(ProtectProc|ProtectClock|ProtectKernelLogs|ProtectHostname|StateDirectory|RuntimeDirectoryMode|RandomizedDelaySec)=' "$service_file" "$timer_file" ||
            die "legacy PostgreSQL backup units contain a directive newer than systemd 219"
    fi
    grep -Fxq 'ReadOnlyPaths=/srv/probe/config' "$service_file" ||
        die "PostgreSQL backup service must read configuration through its protected path"
    grep -Fxq 'ReadWritePaths=/var/backups/probe-panel/postgres' "$service_file" ||
        die "PostgreSQL backup service has an unexpected writable path"
    ! grep -Eq '^Environment=(PG|PROBE_POSTGRES_)' "$service_file" ||
        die "PostgreSQL backup settings must come only from the mandatory environment file"
    grep -Fxq 'Unit=probe-postgres-backup.service' "$timer_file" ||
        die "PostgreSQL backup timer targets an unexpected unit"
    grep -Fxq 'Persistent=true' "$timer_file" || die "PostgreSQL backup timer must be persistent"
}

verify_source_systemd_units() {
    local source_root="$1" work_root="$2" verify_root
    verify_root="${work_root}/systemd-verify"
    install -d -m 0700 -- "$verify_root"

    local api_source backup_source timer_source api_asset backup_service_asset backup_timer_asset
    api_asset="$(selected_systemd_asset_name probe-api.service)"
    backup_service_asset="$(selected_systemd_asset_name probe-postgres-backup.service)"
    backup_timer_asset="$(selected_systemd_asset_name probe-postgres-backup.timer)"
    api_source="${source_root}/probe-api/deploy/systemd/$api_asset"
    backup_source="${source_root}/probe-api/deploy/systemd/$backup_service_asset"
    timer_source="${source_root}/probe-api/deploy/systemd/$backup_timer_asset"
    validate_systemd_unit_source "$api_source"
    validate_backup_unit_source "$backup_source" "$timer_source"

    sed \
        -e 's#^EnvironmentFile=/srv/probe/config/probe-api.env$#EnvironmentFile=-/etc/environment#' \
        -e 's#^ExecStart=/srv/probe/api/probe-api serve$#ExecStart=/usr/bin/true#' \
        "$api_source" > "${verify_root}/probe-api.service"
    sed \
        -e 's#^EnvironmentFile=/srv/probe/config/probe-postgres-backup.env$#EnvironmentFile=-/etc/environment#' \
        -e 's#^ExecStart=/srv/probe/api/scripts/backup-postgres.sh$#ExecStart=/usr/bin/true#' \
        "$backup_source" > "${verify_root}/probe-postgres-backup.service"
    cp -- "$timer_source" "${verify_root}/probe-postgres-backup.timer"
    systemd-analyze verify \
        "${verify_root}/probe-api.service" \
        "${verify_root}/probe-postgres-backup.service" \
        "${verify_root}/probe-postgres-backup.timer"
}

validate_backup_service_assets() {
    local postgres_script
    require_runtime_postgres_commands
    [[ -x "${PROBE_BACKUP_SCRIPT_DIR}/backup-postgres.sh" ]] ||
        die "installed PostgreSQL backup script is missing"
    [[ -x "${PROBE_BACKUP_SCRIPT_DIR}/restore-postgres.sh" ]] ||
        die "installed PostgreSQL restore script is missing"
    for postgres_script in backup-postgres.sh restore-postgres.sh; do
        [[ "$(stat -c '%U:%G:%a' -- "${PROBE_BACKUP_SCRIPT_DIR}/${postgres_script}")" == root:probe-api:750 ]] ||
            die "installed PostgreSQL script must be root:probe-api mode 0750: $postgres_script"
    done
    grep -Fxq "readonly PG_DUMP_BINARY='$(runtime_postgres_command pg_dump)'" \
        "${PROBE_BACKUP_SCRIPT_DIR}/backup-postgres.sh" ||
        die 'installed PostgreSQL backup script has an unexpected pg_dump command path'
    for postgres_script in backup-postgres.sh restore-postgres.sh; do
        grep -Fxq "readonly PG_RESTORE_BINARY='$(runtime_postgres_command pg_restore)'" \
            "${PROBE_BACKUP_SCRIPT_DIR}/${postgres_script}" ||
            die "installed PostgreSQL script has an unexpected pg_restore command path: $postgres_script"
    done
    grep -Fxq "readonly PSQL_BINARY='$(runtime_postgres_command psql)'" \
        "${PROBE_BACKUP_SCRIPT_DIR}/restore-postgres.sh" ||
        die 'installed PostgreSQL restore script has an unexpected psql command path'
    ! grep -Fq '@PROBE_' \
        "${PROBE_BACKUP_SCRIPT_DIR}/backup-postgres.sh" \
        "${PROBE_BACKUP_SCRIPT_DIR}/restore-postgres.sh" ||
        die 'installed PostgreSQL backup/restore script contains an unresolved command token'
    [[ -f "$PROBE_BACKUP_SERVICE_UNIT" && ! -L "$PROBE_BACKUP_SERVICE_UNIT" ]] ||
        die "installed PostgreSQL backup service is missing"
    [[ -f "$PROBE_BACKUP_TIMER_UNIT" && ! -L "$PROBE_BACKUP_TIMER_UNIT" ]] ||
        die "installed PostgreSQL backup timer is missing"
    validate_backup_unit_source "$PROBE_BACKUP_SERVICE_UNIT" "$PROBE_BACKUP_TIMER_UNIT" \
        "$(runtime_systemd_profile)"
}

validate_backup_credentials() {
    assert_probe_api_service_account
    assert_private_file "$PROBE_PGPASS_FILE" probe-api
    [[ -s "$PROBE_PGPASS_FILE" ]] || die "$PROBE_PGPASS_FILE must not be empty"
    run_as_probe_api_no_environment /usr/bin/test -r "$PROBE_PGPASS_FILE" ||
        die "probe-api cannot read $PROBE_PGPASS_FILE"
    assert_private_file "$PROBE_BACKUP_ENV_FILE" root
    [[ -s "$PROBE_BACKUP_ENV_FILE" ]] || die "$PROBE_BACKUP_ENV_FILE must not be empty"

    local raw line key value required
    declare -A values=()
    while IFS= read -r raw || [[ -n "$raw" ]]; do
        line="${raw%$'\r'}"
        [[ -z "$line" || "$line" =~ ^[[:space:]]*# ]] && continue
        [[ ! "$line" =~ [[:space:]] ]] ||
            die "$PROBE_BACKUP_ENV_FILE must use unquoted KEY=value lines without whitespace"
        [[ "$line" =~ ^([A-Z][A-Z0-9_]*)=(.+)$ ]] || die "invalid line in $PROBE_BACKUP_ENV_FILE"
        key="${BASH_REMATCH[1]}"
        value="${BASH_REMATCH[2]}"
        case "$key" in
            PGHOST|PGPORT|PGDATABASE|PGUSER|PGPASSFILE|PROBE_POSTGRES_BACKUP_DIR|PROBE_POSTGRES_DAILY_KEEP|PROBE_POSTGRES_WEEKLY_KEEP|PROBE_POSTGRES_WEEKLY_DAY) ;;
            *) die "unsupported key in $PROBE_BACKUP_ENV_FILE: $key" ;;
        esac
        [[ -z "${values[$key]+x}" ]] || die "duplicate key in $PROBE_BACKUP_ENV_FILE: $key"
        values[$key]="$value"
    done < "$PROBE_BACKUP_ENV_FILE"

    for required in \
        PGHOST PGPORT PGDATABASE PGUSER PGPASSFILE \
        PROBE_POSTGRES_BACKUP_DIR PROBE_POSTGRES_DAILY_KEEP \
        PROBE_POSTGRES_WEEKLY_KEEP PROBE_POSTGRES_WEEKLY_DAY; do
        [[ -n "${values[$required]:-}" ]] || die "missing required key in $PROBE_BACKUP_ENV_FILE: $required"
    done

    [[ "${values[PGHOST]}" == '127.0.0.1' || "${values[PGHOST]}" == /* ]] ||
        die "PGHOST must be 127.0.0.1 or an absolute local Unix-socket directory"
    require_integer_between PGPORT "${values[PGPORT]}" 1 65535
    [[ "${values[PGDATABASE]}" =~ ^[A-Za-z_][A-Za-z0-9_.-]*$ ]] ||
        die "PGDATABASE must be a database name, not a URL or connection string"
    [[ "${values[PGUSER]}" =~ ^[A-Za-z_][A-Za-z0-9_.-]*$ ]] || die "PGUSER has an unsafe value"
    [[ "${values[PGPASSFILE]}" == "$PROBE_PGPASS_FILE" ]] ||
        die "PGPASSFILE must be $PROBE_PGPASS_FILE"
    [[ "${values[PROBE_POSTGRES_BACKUP_DIR]}" == "$PROBE_POSTGRES_BACKUP_DIR" ]] ||
        die "PROBE_POSTGRES_BACKUP_DIR must be $PROBE_POSTGRES_BACKUP_DIR"
    require_integer_between PROBE_POSTGRES_DAILY_KEEP "${values[PROBE_POSTGRES_DAILY_KEEP]}" 1 365
    require_integer_between PROBE_POSTGRES_WEEKLY_KEEP "${values[PROBE_POSTGRES_WEEKLY_KEEP]}" 1 260
    require_integer_between PROBE_POSTGRES_WEEKLY_DAY "${values[PROBE_POSTGRES_WEEKLY_DAY]}" 1 7

    PROBE_VALIDATED_PGHOST="${values[PGHOST]}"
    PROBE_VALIDATED_PGPORT="${values[PGPORT]}"
    PROBE_VALIDATED_PGDATABASE="${values[PGDATABASE]}"
    PROBE_VALIDATED_PGUSER="${values[PGUSER]}"
    PROBE_VALIDATED_PGPASSFILE="${values[PGPASSFILE]}"
}

acquire_database_maintenance_lock() {
    [[ -d "$PROBE_POSTGRES_BACKUP_DIR" && ! -L "$PROBE_POSTGRES_BACKUP_DIR" ]] ||
        die "PostgreSQL backup root is missing or unsafe: $PROBE_POSTGRES_BACKUP_DIR"
    exec 8<"$PROBE_POSTGRES_BACKUP_DIR"
    flock -n 8 || die "a PostgreSQL backup, restore, or another deployment is running"
}

disable_default_nginx_site() {
    local default_link="/etc/nginx/sites-enabled/default"
    [[ -e "$default_link" || -L "$default_link" ]] || return 0
    [[ -L "$default_link" ]] || die "$default_link is not the Debian package symlink; remove it manually after review"
    [[ "$(readlink -f -- "$default_link")" == "/etc/nginx/sites-available/default" ]] ||
        die "$default_link has an unexpected target; remove it manually after review"
    unlink -- "$default_link"
    log "disabled the Debian default Nginx site"
}

stage_release() {
    local artifact_root="$1" release_id="$2" profile="${3:-full}"
    local incoming="${PROBE_RELEASES_DIR}/.incoming-${release_id}"
    local final="${PROBE_RELEASES_DIR}/${release_id}"
    validate_release_profile "$profile"
    STAGED_RELEASE_DIR=""
    STAGED_RELEASE_INCOMING_PENDING=""
    [[ ! -e "$incoming" && ! -e "$final" ]] || die "release identifier already exists: $release_id"
    STAGED_RELEASE_INCOMING_PENDING="$incoming"

    install -d -o root -g root -m 0755 "$incoming" "$incoming/api" "$incoming/admin" "$incoming/migrations"
    install -o root -g root -m 0755 -- "$artifact_root/api/probe-api" "$incoming/api/probe-api"
    cp -a -- "$artifact_root/admin/." "$incoming/admin/"
    cp -a -- "$artifact_root/migrations/." "$incoming/migrations/"
    local -a release_paths=("$incoming/admin" "$incoming/migrations")
    if [[ "$profile" == full ]]; then
        install -d -o root -g root -m 0755 "$incoming/agent" "$incoming/web"
        cp -a -- "$artifact_root/agent/." "$incoming/agent/"
        cp -a -- "$artifact_root/web/." "$incoming/web/"
        release_paths+=("$incoming/agent" "$incoming/web")
    fi
    find "${release_paths[@]}" -type d -exec chmod 0755 {} +
    find "${release_paths[@]}" -type f -exec chmod 0644 {} +
    if [[ "$profile" == full ]]; then
        chmod 0755 "$incoming/agent/downloads/probe-agent/install.sh" \
            "$incoming/agent/downloads/probe-agent/linux-amd64/probe-agent" \
            "$incoming/agent/downloads/probe-agent/linux-arm64/probe-agent"
    fi
    chown -R root:root "$incoming"

    (
        cd "$incoming"
        find . -mindepth 2 -type f -print0 | sed -z 's#^[.]/##' | sort -z | xargs -0 sha256sum > SHA256SUMS
    )
    chmod 0644 "$incoming/SHA256SUMS"
    mv -T -- "$incoming" "$final"
    STAGED_RELEASE_INCOMING_PENDING=""
    STAGED_RELEASE_DIR="$final"
}

assert_switchable_path() {
    local path="$1"
    [[ ! -e "$path" && ! -L "$path" ]] && return 0
    [[ -L "$path" ]] || die "$path is not release-managed; move the existing path aside before deployment"
    local resolved
    resolved="$(readlink -f -- "$path")"
    [[ "$resolved" == "$PROBE_RELEASES_DIR"/* ]] || die "$path points outside $PROBE_RELEASES_DIR"
}

atomic_release_link() {
    local target="$1" link_path="$2"
    local temporary="${link_path}.next.$$"
    if [[ -e "$temporary" || -L "$temporary" ]]; then
        [[ -L "$temporary" ]] || return 1
        rm -f -- "$temporary" || return 1
    fi
    ln -s -- "$target" "$temporary" || return 1
    if ! mv -Tf -- "$temporary" "$link_path"; then
        rm -f -- "$temporary" || :
        return 1
    fi
}

activate_release() {
    local release_dir="$1" profile="${2:-full}"
    validate_release_profile "$profile"
    assert_switchable_path "${PROBE_API_DIR}/probe-api"
    assert_switchable_path "${PROBE_ROOT}/admin"
    assert_switchable_path "${PROBE_ROOT}/migrations"
    if [[ "$profile" == full ]]; then
        assert_switchable_path "${PROBE_ROOT}/agent"
        assert_switchable_path "${PROBE_ROOT}/web"
    fi

    atomic_release_link "${release_dir}/api/probe-api" "${PROBE_API_DIR}/probe-api" || return 1
    if [[ "$profile" == full ]]; then
        atomic_release_link "${release_dir}/agent" "${PROBE_ROOT}/agent" || return 1
        atomic_release_link "${release_dir}/web" "${PROBE_ROOT}/web" || return 1
    fi
    atomic_release_link "${release_dir}/admin" "${PROBE_ROOT}/admin" || return 1
    atomic_release_link "${release_dir}/migrations" "${PROBE_ROOT}/migrations" || return 1
}

validate_switchable_release_paths() {
    local profile="${1:-full}"
    validate_release_profile "$profile"
    assert_switchable_path "${PROBE_API_DIR}/probe-api"
    assert_switchable_path "${PROBE_ROOT}/admin"
    assert_switchable_path "${PROBE_ROOT}/migrations"
    if [[ "$profile" == full ]]; then
        assert_switchable_path "${PROBE_ROOT}/agent"
        assert_switchable_path "${PROBE_ROOT}/web"
    fi
}

current_release_target() {
    local link_path="$1"
    if [[ -L "$link_path" ]]; then
        readlink -- "$link_path"
    fi
    return 0
}

restore_release_links() {
    local profile="$1" old_api="$2" old_agent="$3" old_web="$4" old_admin="$5" old_migrations="$6"
    validate_release_profile "$profile"
    local link_path target index failed=0
    local -a links=("${PROBE_API_DIR}/probe-api" "${PROBE_ROOT}/admin" "${PROBE_ROOT}/migrations")
    local -a targets=("$old_api" "$old_admin" "$old_migrations")
    if [[ "$profile" == full ]]; then
        links+=("${PROBE_ROOT}/agent" "${PROBE_ROOT}/web")
        targets+=("$old_agent" "$old_web")
    fi

    for ((index=0; index<${#links[@]}; index++)); do
        link_path="${links[$index]}"
        target="${targets[$index]}"
        if [[ -n "$target" ]]; then
            if ! atomic_release_link "$target" "$link_path"; then
                warn "could not restore prior application link: $link_path"
                failed=1
            fi
        elif ! rm -f -- "$link_path"; then
            warn "could not remove newly activated application link: $link_path"
            failed=1
        fi
    done
    return "$failed"
}

create_database_backup() (
    set -Eeuo pipefail
    local release_id="$1"
    local temporary="${PROBE_BACKUPS_DIR}/.pre-upgrade-${release_id}.dump"
    local final="${PROBE_BACKUPS_DIR}/pre-upgrade-${release_id}.dump"
    # Invoked indirectly by the EXIT trap below.
    # shellcheck disable=SC2317,SC2329
    cleanup_failed_database_backup() {
        local status=$?
        trap - EXIT
        trap '' HUP INT TERM
        if (( status != 0 )); then
            rm -f -- "$temporary" || true
        fi
        exit "$status"
    }
    trap cleanup_failed_database_backup EXIT
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM

    [[ ! -e "$temporary" && ! -e "$final" ]] || die "database backup already exists: $final"
    [[ -n "$PROBE_VALIDATED_PGHOST" && -n "$PROBE_VALIDATED_PGPORT" &&
       -n "$PROBE_VALIDATED_PGDATABASE" && -n "$PROBE_VALIDATED_PGUSER" &&
       -n "$PROBE_VALIDATED_PGPASSFILE" ]] ||
        die "validated PostgreSQL backup connection is unavailable"

    log "creating a PostgreSQL backup before migrations"
    if ! /usr/bin/env -i \
        PGHOST="$PROBE_VALIDATED_PGHOST" \
        PGPORT="$PROBE_VALIDATED_PGPORT" \
        PGDATABASE="$PROBE_VALIDATED_PGDATABASE" \
        PGUSER="$PROBE_VALIDATED_PGUSER" \
        PGPASSFILE="$PROBE_VALIDATED_PGPASSFILE" \
        "$(runtime_postgres_command pg_dump)" --no-password --format=custom --file="$temporary"; then
        die "PostgreSQL pre-migration backup failed"
    fi
    if [[ ! -s "$temporary" ]]; then
        die "PostgreSQL pre-migration backup is empty"
    fi
    chmod 0600 "$temporary" || {
        die "secure the PostgreSQL pre-migration backup"
    }
    if ! "$(runtime_postgres_command pg_restore)" --list "$temporary" >/dev/null; then
        die "PostgreSQL pre-migration backup verification failed"
    fi
    mv -T -- "$temporary" "$final" || {
        die "retain the PostgreSQL pre-migration backup"
    }
    trap - EXIT HUP INT TERM
    printf '%s\n' "$final"
)

persist_native_nginx_service() {
    local fragment wants_link output
    output="$(systemctl show --property=FragmentPath nginx.service)" ||
        die "read nginx.service FragmentPath"
    [[ "$output" == FragmentPath=* && "$output" != *$'\n'* ]] ||
        die "nginx.service returned a malformed FragmentPath"
    fragment="${output#*=}"
    case "$fragment" in
        /usr/lib/systemd/system/nginx.service|/lib/systemd/system/nginx.service) ;;
        *) die "nginx.service is not an accepted native systemd unit" ;;
    esac

    systemctl add-wants multi-user.target nginx.service >/dev/null ||
        die "persist nginx.service in multi-user.target"
    wants_link="/etc/systemd/system/multi-user.target.wants/nginx.service"
    [[ -L "$wants_link" && "$(readlink -f -- "$wants_link")" == "$(readlink -f -- "$fragment")" ]] ||
        die "nginx.service multi-user.target link is missing or unsafe"
}

run_migrations() {
    local api_binary="$1"
    "$api_binary" migrate status || return 1
    "$api_binary" migrate up || return 1
    "$api_binary" migrate status || return 1
}

validate_runtime_listeners() {
    local profile="${1:-full}"
    require_commands ss
    local listeners
    # The production validator also runs inside the one-time Finalizer's
    # ProtectProc=invisible sandbox. Process ownership from ss -p is therefore
    # intentionally unavailable. Nginx has already passed nginx -T, service
    # activation, and is-active checks; validate its reviewed ingress ports by
    # presence while retaining strict loopback checks for API and PostgreSQL.
    listeners="$(ss -H -lnt)"

    awk -v mode="$PROBE_INGRESS_MODE" -v profile="$profile" '
        BEGIN {
            if (mode != "domain" && mode != "ip") {
                printf "unsupported ingress mode while validating runtime listeners: %s\n", mode > "/dev/stderr"
                bad=1
            }
        }
        {
            endpoint=$4
            port=endpoint
            sub(/^.*:/, "", port)
            host=endpoint
            sub(/:[^:]*$/, "", host)
            sub(/^\[/, "", host)
            sub(/\]$/, "", host)
            if (mode == "domain" && (port == "80" || port == "443")) {
                ingress[port]=1
            }
            if (mode == "ip" && ((profile == "management" && port == "18455") ||
                (profile == "full" && (port == "18453" || port == "18454" || port == "18455")))) {
                ingress[port]=1
            }
            if (port == "8080") {
                api_count++
                if (host != "127.0.0.1") {
                    printf "probe-api listener is not IPv4 loopback: %s\n", $4 > "/dev/stderr"
                    bad=1
                }
            }
            if (port == "5432") {
                postgres_count++
                if (host != "127.0.0.1" && host != "::1") {
                    printf "PostgreSQL listener is not loopback: %s\n", $4 > "/dev/stderr"
                    bad=1
                }
            }
        }
        END {
            if (mode == "domain" && (!ingress["80"] || !ingress["443"])) bad=1
            if (mode == "ip" && profile == "management" && !ingress["18455"]) bad=1
            if (mode == "ip" && profile == "full" && (!ingress["18453"] || !ingress["18454"] || !ingress["18455"])) bad=1
            if (api_count < 1 || postgres_count < 1) bad=1
            if (bad) exit 1
            exit 0
        }
    ' <<<"$listeners" || die "runtime listeners violate the ingress or loopback-only service contract"
}

verify_running_services() {
    local profile="${1:-full}"
    assert_probe_api_service_account
    systemctl is-active --quiet "$(runtime_postgres_service)" || die "PostgreSQL is not active"
    systemctl is-active --quiet probe-api.service || die "probe-api is not active"
    systemctl is-active --quiet nginx.service || die "Nginx is not active"
    systemctl is-active --quiet probe-postgres-backup.timer ||
        die "probe-postgres-backup.timer is not active"
    validate_ingress_tls_with_binary "${PROBE_API_DIR}/probe-api" "$profile"
    validate_certbot_timer_state "$profile"
    curl --fail --silent --show-error --max-time 10 \
        http://127.0.0.1:8080/internal/health/ready >/dev/null ||
        die "probe-api readiness check failed"
    validate_runtime_listeners "$profile"
}

management_installed_nginx_template() {
    case "${PROBE_INGRESS_MODE:-}" in
        domain) printf '%s\n' "${PROBE_NGINX_CONFIG_DIR}/nginx-management.conf.example" ;;
        ip) printf '%s\n' "${PROBE_NGINX_CONFIG_DIR}/nginx-management-ip.conf.example" ;;
        *) die 'load the installed management ingress mode before selecting its retained template' ;;
    esac
}

management_lifecycle_asset_names() {
    printf '%s\n' \
        deploy-common.sh \
        validate-management.sh \
        restore-management.sh \
        uninstall-management.sh
}

validate_management_lifecycle_manifest() {
    local manifest="$1" asset_suffix="${2:-}"
    local line lifecycle_name lifecycle_path expected_hash actual_hash index=0
    local -a expected_names=(
        deploy-common.sh
        validate-management.sh
        restore-management.sh
        uninstall-management.sh
    )

    case "$manifest:$asset_suffix" in
        "$PROBE_MANAGEMENT_LIFECYCLE_MANIFEST:") ;;
        "${PROBE_MANAGEMENT_LIFECYCLE_MANIFEST}.new.$$:.new.$$") ;;
        *) die "refusing an unexpected management lifecycle manifest: $manifest" ;;
    esac
    [[ -f "$manifest" && ! -L "$manifest" ]] ||
        die "management lifecycle manifest is missing or unsafe: $manifest"
    [[ "$(stat -c '%u:%g:%a' -- "$manifest")" == 0:0:644 ]] ||
        die "management lifecycle manifest must be root:root mode 0644: $manifest"

    while IFS= read -r line || [[ -n "$line" ]]; do
        (( index < ${#expected_names[@]} )) ||
            die 'management lifecycle manifest contains more than four entries'
        lifecycle_name="${expected_names[index]}"
        expected_hash="${line:0:64}"
        [[ "$expected_hash" =~ ^[0-9a-f]{64}$ &&
           "${line:64:2}" == "  " &&
           "${line:66}" == "$lifecycle_name" &&
           "${#line}" -eq $((66 + ${#lifecycle_name})) ]] ||
            die "management lifecycle manifest has a noncanonical entry for $lifecycle_name"
        lifecycle_path="${PROBE_MANAGEMENT_LIB_DIR}/${lifecycle_name}${asset_suffix}"
        [[ -f "$lifecycle_path" && ! -L "$lifecycle_path" && -x "$lifecycle_path" ]] ||
            die "management lifecycle tool is missing or unsafe: $lifecycle_path"
        [[ "$(stat -c '%u:%g:%a' -- "$lifecycle_path")" == 0:0:755 ]] ||
            die "management lifecycle tool must be root:root mode 0755: $lifecycle_path"
        bash -n -- "$lifecycle_path" ||
            die "management lifecycle tool has invalid syntax: $lifecycle_path"
        actual_hash="$(sha256sum -- "$lifecycle_path")"
        actual_hash="${actual_hash%% *}"
        [[ "$actual_hash" == "$expected_hash" ]] ||
            die "management lifecycle checksum does not match: $lifecycle_path"
        ((index += 1))
    done < "$manifest"
    (( index == ${#expected_names[@]} )) ||
        die 'management lifecycle manifest must contain exactly four canonical entries'

    if [[ -z "$asset_suffix" ]]; then
        (
            cd -- "$PROBE_MANAGEMENT_LIB_DIR"
            sha256sum --check --strict --status -- "$PROBE_MANAGEMENT_LIFECYCLE_MANIFEST"
        ) || die 'installed management lifecycle checksum verification failed'
    fi
}

validate_management_lifecycle_assets() {
    validate_management_lifecycle_manifest "$PROBE_MANAGEMENT_LIFECYCLE_MANIFEST"
}

validate_installed_management_host() {
    clear_exported_probe_environment
    load_probe_env
    [[ "$PROBE_INSTALLATION_PROFILE" == management ]] ||
        die 'the installed product is not the management-only profile'

    local nginx_template api_real admin_real migrations_real release_dir
    nginx_template="$(management_installed_nginx_template)"
    [[ -f "$nginx_template" && ! -L "$nginx_template" ]] ||
        die "the retained management Nginx template is missing: $nginx_template"

    validate_backup_credentials
    validate_switchable_release_paths management
    validate_systemd_unit_source "$PROBE_SYSTEMD_UNIT" "$(runtime_systemd_profile)"
    validate_backup_service_assets
    validate_management_lifecycle_assets
    systemd-analyze verify \
        "$PROBE_SYSTEMD_UNIT" "$PROBE_BACKUP_SERVICE_UNIT" "$PROBE_BACKUP_TIMER_UNIT"
    validate_nginx_runtime_config "$nginx_template" management

    api_real="$(readlink -f -- "$PROBE_API_DIR/probe-api")" ||
        die 'the active management API release link is broken'
    admin_real="$(readlink -f -- "$PROBE_ROOT/admin")" ||
        die 'the active administrator release link is broken'
    migrations_real="$(readlink -f -- "$PROBE_ROOT/migrations")" ||
        die 'the active migration release link is broken'
    release_dir="$(dirname -- "$(dirname -- "$api_real")")"
    [[ "$release_dir" == "$PROBE_RELEASES_DIR"/* && -d "$release_dir" && ! -L "$release_dir" ]] ||
        die 'the active management release directory is outside the managed release root'
    [[ "$admin_real" == "$release_dir/admin" ]] ||
        die 'the administrator SPA is not from the active management release'
    [[ "$migrations_real" == "$release_dir/migrations" ]] ||
        die 'the migration tree is not from the active management release'
    [[ -f "$release_dir/SHA256SUMS" && ! -L "$release_dir/SHA256SUMS" ]] ||
        die 'the active management release checksum manifest is missing'
    (
        cd "$release_dir" || die 'could not enter the active management release'
        sha256sum --check --strict SHA256SUMS
    )
    validate_release_artifacts "$release_dir" management staged
    validate_allowlist_with_binary "$api_real"
    validate_ingress_tls_with_binary "$api_real" management
    validate_certbot_timer_state management
    log "installed management host assets are valid for $RUNTIME_PLATFORM_ID"
}

deploy_prebuilt_release() {
    local bundle_root="$1" release_id="$2" expected_profile="${3:-auto}"
    require_supported_runtime_platform
    require_commands bash cp env find install sha256sum sort xargs nginx systemctl systemd-analyze setpriv curl ss flock awk grep sed stat readlink python3
    require_runtime_postgres_commands

    bundle_root="$(canonical_directory "$bundle_root")"
    [[ "$release_id" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$ ]] ||
        die "prebuilt release identifier is invalid"

    local release_profile
    release_profile="$(release_bundle_profile "$bundle_root")"
    case "$expected_profile" in
        auto) ;;
        full|management)
            [[ "$release_profile" == "$expected_profile" ]] ||
                die "release profile mismatch: expected $expected_profile, bundle is $release_profile"
            ;;
        *) die "unsupported expected release profile: $expected_profile" ;;
    esac
    validate_prebuilt_bundle "$bundle_root" "$release_profile"

    local source_root="$bundle_root/source"
    local artifact_root="$bundle_root/artifacts"
    prepare_system_layout "$source_root" "$release_profile"
    acquire_database_maintenance_lock
    clear_exported_probe_environment
    load_probe_env
    local ingress_mode="$PROBE_INGRESS_MODE" nginx_template
    [[ "$PROBE_INSTALLATION_PROFILE" == "$release_profile" ]] ||
        die "installed API profile does not match the prebuilt release profile"
    nginx_template="$(selected_nginx_template "$source_root" "$release_profile")"
    validate_active_nginx_config "$nginx_template" "$release_profile"
    validate_backup_credentials
    validate_switchable_release_paths "$release_profile"
    clear_exported_probe_environment

    local verify_root
    verify_root="$(mktemp -d /var/tmp/probe-prebuilt-verify.XXXXXX)"
    PROBE_DEPLOY_WORK_ROOT="$verify_root"
    verify_source_systemd_units "$source_root" "$verify_root"

    clear_exported_probe_environment
    load_probe_env
    [[ "$PROBE_INGRESS_MODE" == "$ingress_mode" ]] ||
        die "PROBE_INGRESS_MODE changed during prebuilt release validation"
    validate_allowlist_with_binary "$artifact_root/api/probe-api"
    [[ "$PROBE_INSTALLATION_PROFILE" == "$release_profile" ]] ||
        die "installed API profile changed during prebuilt release validation"
    validate_ingress_tls_with_binary "$artifact_root/api/probe-api" "$release_profile"
    validate_certbot_timer_state "$release_profile"

    local unique_release_id release_dir backup_path
    unique_release_id="${release_id}-$(date -u +%Y%m%dT%H%M%SZ)-$$"
    stage_release "$artifact_root" "$unique_release_id" "$release_profile"
    release_dir="$STAGED_RELEASE_DIR"
    [[ "$release_dir" == "$PROBE_RELEASES_DIR/$unique_release_id" ]] ||
        die 'staged release did not publish its exact final directory'
    validate_release_artifacts "$release_dir" "$release_profile"
    (
        cd "$release_dir"
        sha256sum --check --strict SHA256SUMS
    )

    capture_management_service_activity
    MANAGEMENT_ACTIVATION_ROLLBACK_STATE="maintenance"
    if ! stop_management_unit_to_inactive probe-postgres-backup.timer; then
        die 'the PostgreSQL backup timer could not enter the pre-migration maintenance window'
    fi
    if ! stop_management_unit_to_inactive probe-api.service; then
        die 'probe-api could not enter the pre-migration maintenance window'
    fi
    if ! systemctl start "$(runtime_postgres_service)"; then
        die 'PostgreSQL could not enter the pre-migration maintenance window'
    fi
    local backup_status
    set +e
    backup_path="$(create_database_backup "$unique_release_id")"
    backup_status=$?
    set -e
    if (( backup_status != 0 )); then
        die 'database backup failed before management release activation'
    fi
    log "database backup retained at $backup_path"
    if ! run_migrations "$release_dir/api/probe-api"; then
        die "database migration failed; the verified backup is $backup_path"
    fi

    local old_api old_agent old_web old_admin old_migrations
    old_api="$(current_release_target "${PROBE_API_DIR}/probe-api")"
    old_admin="$(current_release_target "${PROBE_ROOT}/admin")"
    old_migrations="$(current_release_target "${PROBE_ROOT}/migrations")"
    old_agent=""
    old_web=""
    if [[ "$release_profile" == full ]]; then
        old_agent="$(current_release_target "${PROBE_ROOT}/agent")"
        old_web="$(current_release_target "${PROBE_ROOT}/web")"
    fi

    MANAGEMENT_ROLLBACK_RELEASE_PROFILE="$release_profile"
    MANAGEMENT_ROLLBACK_OLD_API="$old_api"
    MANAGEMENT_ROLLBACK_OLD_AGENT="$old_agent"
    MANAGEMENT_ROLLBACK_OLD_WEB="$old_web"
    MANAGEMENT_ROLLBACK_OLD_ADMIN="$old_admin"
    MANAGEMENT_ROLLBACK_OLD_MIGRATIONS="$old_migrations"

    local service_asset_status service_asset_snapshot snapshot_status
    set +e
    service_asset_snapshot="$(snapshot_management_service_assets)"
    snapshot_status=$?
    set -e
    if (( snapshot_status != 0 )); then
        die "could not snapshot management host assets; the verified backup is $backup_path"
    fi
    MANAGEMENT_ROLLBACK_SERVICE_ASSET_SNAPSHOT="$service_asset_snapshot"
    MANAGEMENT_ACTIVATION_ROLLBACK_STATE="links"
    if ! activate_release "$release_dir" "$release_profile"; then
        die "release link activation failed; the forward database migration remains applied and backup is $backup_path"
    fi

    set +e
    (
        trap - EXIT
        set -Eeuo pipefail
        install_service_assets "$source_root"
        validate_nginx_runtime_config "$nginx_template" "$release_profile"
    )
    service_asset_status=$?
    set -e
    if (( service_asset_status != 0 )); then
        die "release service asset activation failed; the forward database migration remains applied and backup is $backup_path"
    fi
    local host_asset_status runtime_verify_status
    set +e
    (
        trap - EXIT
        trap 'exit 129' HUP
        trap 'exit 130' INT
        trap 'exit 143' TERM
        set -Eeuo pipefail
        systemctl daemon-reload
        systemd-analyze verify "$PROBE_SYSTEMD_UNIT" "$PROBE_BACKUP_SERVICE_UNIT" "$PROBE_BACKUP_TIMER_UNIT"
        validate_backup_service_assets
        systemctl enable probe-api.service probe-postgres-backup.timer >/dev/null
        persist_native_nginx_service
        systemctl start "$(runtime_postgres_service)"
    )
    host_asset_status=$?
    set -e
    if (( host_asset_status != 0 )); then
        die "release host asset activation failed; the forward database migration remains applied and backup is $backup_path"
    fi
    MANAGEMENT_ACTIVATION_ROLLBACK_STATE="runtime"
    set +e
    (
        trap - EXIT
        trap 'exit 129' HUP
        trap 'exit 130' INT
        trap 'exit 143' TERM
        set -Eeuo pipefail
        systemctl restart probe-api.service
        systemctl start probe-postgres-backup.timer
        systemctl reload-or-restart nginx.service
        verify_running_services "$release_profile"
    )
    runtime_verify_status=$?
    set -e
    if (( runtime_verify_status != 0 )); then
        die "release activation failed; the forward database migration remains applied and backup is $backup_path"
    fi

    MANAGEMENT_ACTIVATION_ROLLBACK_STATE="none"
    MANAGEMENT_ROLLBACK_SERVICE_ASSET_SNAPSHOT=""
    run_management_rollback_step \
        "management activation committed, but snapshot cleanup requires manual removal: $service_asset_snapshot" \
        discard_management_service_asset_snapshot "$service_asset_snapshot"

    if rm -rf -- "$verify_root"; then
        PROBE_DEPLOY_WORK_ROOT=""
    else
        warn "management activation committed, but verification workspace cleanup requires manual removal: $verify_root"
    fi
    log "prebuilt release ${unique_release_id} is active"
    log "previous release directories and $backup_path were retained"
}

# MANAGEMENT_BUNDLE_EXCLUDE_BUILD_BEGIN
deploy_release() {
    local source_root="$1" run_tests="$2" validate_only="$3"
    require_commands bash go npm node cp env find install sha256sum sort xargs pg_dump pg_restore nginx systemctl systemd-analyze setpriv curl ss flock awk grep sed stat readlink python3

    assert_probe_api_service_account
    acquire_database_maintenance_lock
    validate_deployment_script_sources "$source_root"
    clear_exported_probe_environment
    load_probe_env
    local ingress_mode="$PROBE_INGRESS_MODE" nginx_template
    nginx_template="$(selected_nginx_template "$source_root" full)"
    validate_active_nginx_config "$nginx_template" full
    validate_backup_credentials
    validate_systemd_unit_source "${source_root}/probe-api/deploy/systemd/probe-api.service"
    validate_switchable_release_paths full

    local release_id work_root artifact_root release_dir backup_path
    release_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
    work_root="$(mktemp -d /var/tmp/probe-build.XXXXXX)"
    PROBE_DEPLOY_WORK_ROOT="$work_root"

    clear_exported_probe_environment
    build_release_artifacts "$source_root" "$work_root" "$run_tests" "$release_id"
    verify_source_systemd_units "$source_root" "$work_root"
    artifact_root="${work_root}/artifacts"
    load_probe_env
    [[ "$PROBE_INGRESS_MODE" == "$ingress_mode" ]] ||
        die "PROBE_INGRESS_MODE changed during release build; refusing to switch ingress mode"
    validate_allowlist_with_binary "$artifact_root/api/probe-api"
    validate_ingress_tls_with_binary "$artifact_root/api/probe-api" full
    validate_certbot_timer_state full

    if [[ "$validate_only" == true ]]; then
        [[ -f "$PROBE_SYSTEMD_UNIT" ]] || die "installed probe-api systemd unit is missing"
        systemd-analyze verify "$PROBE_SYSTEMD_UNIT"
        validate_backup_service_assets
        systemd-analyze verify "$PROBE_BACKUP_SERVICE_UNIT" "$PROBE_BACKUP_TIMER_UNIT"
        validate_nginx_runtime_config "$nginx_template" full
        rm -rf -- "$work_root"
        PROBE_DEPLOY_WORK_ROOT=""
        log "validation completed; no database, release link, or service state was changed"
        return 0
    fi

    install_service_assets "$source_root"
    validate_nginx_runtime_config "$nginx_template" full

    stage_release "$artifact_root" "$release_id" full
    release_dir="$STAGED_RELEASE_DIR"
    [[ "$release_dir" == "$PROBE_RELEASES_DIR/$release_id" ]] ||
        die 'staged release did not publish its exact final directory'
    validate_release_artifacts "$release_dir" full
    (
        cd "$release_dir"
        sha256sum --check --strict SHA256SUMS
    )
    systemctl start "$(runtime_postgres_service)"
    backup_path="$(create_database_backup "$release_id")"
    log "database backup retained at $backup_path"
    run_migrations "$release_dir/api/probe-api"

    local old_api old_agent old_web old_admin old_migrations
    old_api="$(current_release_target "${PROBE_API_DIR}/probe-api")"
    old_agent="$(current_release_target "${PROBE_ROOT}/agent")"
    old_web="$(current_release_target "${PROBE_ROOT}/web")"
    old_admin="$(current_release_target "${PROBE_ROOT}/admin")"
    old_migrations="$(current_release_target "${PROBE_ROOT}/migrations")"

    if ! activate_release "$release_dir" full; then
        restore_release_links full "$old_api" "$old_agent" "$old_web" "$old_admin" "$old_migrations"
        die "release link activation failed; the forward database migration remains applied and backup is $backup_path"
    fi

    if ! systemctl daemon-reload ||
       ! systemd-analyze verify "$PROBE_SYSTEMD_UNIT" "$PROBE_BACKUP_SERVICE_UNIT" "$PROBE_BACKUP_TIMER_UNIT" ||
       ! validate_backup_service_assets ||
       ! systemctl enable probe-api.service probe-postgres-backup.timer >/dev/null ||
       ! persist_native_nginx_service ||
       ! systemctl start "$(runtime_postgres_service)" ||
       ! systemctl restart probe-api.service ||
       ! systemctl start probe-postgres-backup.timer ||
       ! systemctl reload-or-restart nginx.service ||
       ! ( verify_running_services full ); then
        warn "new release failed runtime verification; restoring prior application links"
        restore_release_links full "$old_api" "$old_agent" "$old_web" "$old_admin" "$old_migrations"
        systemctl restart probe-api.service || true
        systemctl reload-or-restart nginx.service || true
        die "release activation failed; the forward database migration remains applied and backup is $backup_path"
    fi

    log "release ${release_id} is active"
    log "previous release directories and $backup_path were retained"
    rm -rf -- "$work_root"
    PROBE_DEPLOY_WORK_ROOT=""
}
# MANAGEMENT_BUNDLE_EXCLUDE_BUILD_END
