#!/usr/bin/env bash

# Shared, production-only deployment helpers for Debian 13.
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
# Referenced by install.sh and upgrade.sh after this shared file is sourced.
# shellcheck disable=SC2034
readonly PROBE_DEPLOY_LOCK="/run/lock/probe-panel-deploy.lock"

PROBE_DEPLOY_WORK_ROOT=""
PROBE_VALIDATED_PGHOST=""
PROBE_VALIDATED_PGPORT=""
PROBE_VALIDATED_PGDATABASE=""
PROBE_VALIDATED_PGUSER=""
PROBE_VALIDATED_PGPASSFILE=""

cleanup_deploy_work_root() {
    local status=$?
    trap - EXIT
    if [[ -n "$PROBE_DEPLOY_WORK_ROOT" && -d "$PROBE_DEPLOY_WORK_ROOT" &&
          ! -L "$PROBE_DEPLOY_WORK_ROOT" ]]; then
        case "$PROBE_DEPLOY_WORK_ROOT" in
            /var/tmp/probe-build.*|/var/tmp/probe-prebuilt-verify.*) rm -rf -- "$PROBE_DEPLOY_WORK_ROOT" ;;
            *) warn "refusing to remove unexpected temporary path: $PROBE_DEPLOY_WORK_ROOT" ;;
        esac
    fi
    exit "$status"
}
trap cleanup_deploy_work_root EXIT

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
}

require_commands() {
    local command_name
    for command_name in "$@"; do
        command -v "$command_name" >/dev/null 2>&1 || die "required command is missing: $command_name"
    done
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
        /^[[:space:]]*#/ { next }
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
    [[ "$account_shell" == /usr/sbin/nologin ]] ||
        die "probe-api must use /usr/sbin/nologin as its shell"

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
    # Debian 13's stock file relies on that default, so preserve it while still
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
        die "probe-api UID is outside the Debian system-account range"

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
    require_commands addgroup adduser getent

    local passwd_record group_record
    passwd_record="$(getent passwd probe-api || :)"
    group_record="$(getent group probe-api || :)"
    if [[ -z "$passwd_record" && -z "$group_record" ]]; then
        addgroup --system probe-api
        adduser --system --ingroup probe-api --no-create-home --home /nonexistent \
            --shell /usr/sbin/nologin probe-api
    elif [[ -z "$passwd_record" || -z "$group_record" ]]; then
        die "a partial probe-api service account or group already exists; refusing to repair it"
    fi
    assert_probe_api_service_account
}

canonical_directory() {
    local candidate="$1"
    [[ -d "$candidate" ]] || die "directory does not exist: $candidate"
    readlink -f -- "$candidate"
}

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
    [[ -n "${seen[PROBE_ADMIN_ORIGIN]+x}" ]] ||
        die "PROBE_ADMIN_ORIGIN must be set explicitly in $PROBE_ENV_FILE"
    [[ "${PROBE_ADMIN_ORIGIN:-}" =~ ^https://[^/]+$ ]] ||
        die "PROBE_ADMIN_ORIGIN must be one absolute HTTPS origin"
    [[ "$PROBE_ADMIN_ORIGIN" != "https://admin.example.com" ]] ||
        die "replace the example administrator origin in $PROBE_ENV_FILE"
    [[ -n "${seen[PROBE_AGENT_PUBLIC_URL]+x}" ]] ||
        die "PROBE_AGENT_PUBLIC_URL must be set explicitly in $PROBE_ENV_FILE"
    [[ "${PROBE_AGENT_PUBLIC_URL:-}" =~ ^https://[^/]+$ ]] ||
        die "PROBE_AGENT_PUBLIC_URL must be one absolute HTTPS origin"
    [[ "$PROBE_AGENT_PUBLIC_URL" != "https://api.example.com" ]] ||
        die "replace the example Agent public origin in $PROBE_ENV_FILE"
    [[ -n "${seen[PROBE_AGENT_INSTALLER_URL]+x}" ]] ||
        die "PROBE_AGENT_INSTALLER_URL must be set explicitly in $PROBE_ENV_FILE"
    [[ "${PROBE_AGENT_INSTALLER_URL:-}" =~ ^https://raw[.]githubusercontent[.]com/Kcmose/my-agent/([0-9a-f]{40}|refs/tags/v1[.]0[.]2)/deploy/install[.]sh$ ]] ||
        die "PROBE_AGENT_INSTALLER_URL must use an immutable Kcmose/my-agent GitHub commit or the verified refs/tags/v1.0.2 release"
    [[ -n "${seen[PROBE_AGENT_INSTALL_CA_FILE]+x}" ]] ||
        die "PROBE_AGENT_INSTALL_CA_FILE must be set explicitly, using an empty value in domain mode"
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
            [[ "$PROBE_AGENT_INSTALL_CA_FILE" == "$PROBE_PRIVATE_CA_FILE" ]] ||
                die "IP mode PROBE_AGENT_INSTALL_CA_FILE must be $PROBE_PRIVATE_CA_FILE"
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
    local source_root="$1"
    case "${PROBE_INGRESS_MODE:-}" in
        domain) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx.conf" ;;
        ip) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx-ip.conf" ;;
        *) die "load the explicit ingress mode before selecting an Nginx template" ;;
    esac
}

validate_closed_install_routes() {
    local template_file="$1" result
    result="$(awk '
        /^[[:space:]]*location[[:space:]]+(=|\^~)[[:space:]]+\/install\/?[[:space:]]*\{[[:space:]]*$/ {
            routes++
            if ((getline next_line) > 0) {
                sub(/^[[:space:]]*/, "", next_line)
                sub(/[[:space:]]*$/, "", next_line)
                if (next_line == "return 404;") closed++
            }
        }
        END { printf "%d:%d\n", routes + 0, closed + 0 }
    ' "$template_file")"
    [[ "$result" == "4:4" ]] ||
        die "visitor and administrator /install routes must remain fixed 404 responses"
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
    actual_locations="$(awk '/^[[:space:]]*location[[:space:]]/ {
        sub(/^[[:space:]]*/, "")
        sub(/[[:space:]]*$/, "")
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
    actual_locations="$(awk '/^[[:space:]]*location[[:space:]]/ {
        sub(/^[[:space:]]*/, "")
        sub(/[[:space:]]*$/, "")
        print
    }' "$template_file")"
    [[ "$actual_locations" == "$expected_locations" ]] ||
        die "Nginx source template location contract changed; review and update the validator before deployment"
    validate_closed_install_routes "$template_file"

    [[ "$(grep -Ec '^server \{$' "$template_file")" -eq 6 ]] ||
        die "Nginx source template must contain exactly six server blocks"
    [[ "$(grep -Ec '^[[:space:]]*listen[[:space:]]' "$template_file")" -eq 12 ]] ||
        die "Nginx source template has an unexpected listener count"
    [[ "$(awk '/^[[:space:]]*listen[[:space:]]/ {
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

validate_nginx_fragment_structure() {
    local active_file="$1" template_file="$2"
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
    local template_file="$1"
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

    validate_nginx_fragment_structure "$PROBE_ACTIVE_NGINX_CONFIG" "$template_file"
}

validate_nginx_listen_ports() {
    local dump_file="$1"
    awk -v mode="$PROBE_INGRESS_MODE" '
        /^[[:space:]]*listen[[:space:]]+/ {
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
            } else if (mode == "ip") {
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
            if (mode == "ip" && (seen["18453"] != 2 || seen["18454"] != 2 || seen["18455"] != 2)) bad=1
            if (bad) exit 1
            exit 0
        }
    ' "$dump_file" || die "Nginx listeners do not match PROBE_INGRESS_MODE=$PROBE_INGRESS_MODE"
}

validate_no_duplicate_nginx_hosts() {
    local dump_file="$1" panel_domain admin_domain agent_domain extra domain count
    [[ "$PROBE_INGRESS_MODE" == domain ]] || return 0
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
    local template_file="$1"
    require_commands nginx mktemp
    validate_active_nginx_config "$template_file"

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
    validate_nginx_listen_ports "$dump_file"
    validate_no_duplicate_nginx_hosts "$dump_file"
    rm -f -- "$dump_file"
}

validate_allowlist_with_binary() {
    local api_binary="$1"
    [[ -x "$api_binary" && ! -L "$api_binary" ]] || die "invalid staged API binary: $api_binary"
    assert_secure_file "$PROBE_ALLOWLIST_FILE" root
    setpriv --reuid=probe-api --regid=probe-api --init-groups --reset-env -- \
        /usr/bin/setpriv --inh-caps=-all --ambient-caps=-all -- \
        /usr/bin/test -r "$PROBE_ALLOWLIST_FILE" ||
        die "probe-api cannot read $PROBE_ALLOWLIST_FILE"
    "$api_binary" config validate-admin-allowlist "$PROBE_ALLOWLIST_FILE"
}

validate_ingress_tls_with_binary() {
    local api_binary="$1"
    [[ -x "$api_binary" ]] || die "invalid API binary for ingress TLS validation: $api_binary"

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
    local enabled_state active_state
    enabled_state="$(systemctl is-enabled certbot.timer 2>/dev/null || true)"
    active_state="$(systemctl is-active certbot.timer 2>/dev/null || true)"
    case "$PROBE_INGRESS_MODE" in
        domain)
            [[ "$enabled_state" == enabled ]] ||
                die "domain mode requires certbot.timer to be enabled"
            [[ "$active_state" == active ]] ||
                die "domain mode requires certbot.timer to be active"
            ;;
        ip)
            [[ "$enabled_state" == disabled ]] ||
                die "IP mode requires certbot.timer to be disabled"
            [[ "$active_state" == inactive ]] ||
                die "IP mode requires certbot.timer to be inactive"
            ;;
        *)
            die "load the explicit ingress mode before validating certbot.timer"
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

validate_release_artifacts() {
    local artifact_root="$1"
    local agent_download_root="$artifact_root/agent/downloads/probe-agent"
    [[ -x "$artifact_root/api/probe-api" ]] || die "staged probe-api binary is missing"
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
    validate_static_artifact probe-admin "$artifact_root/admin"
    [[ -d "$artifact_root/migrations" ]] || die "staged migrations are missing"
    "$artifact_root/api/probe-api" version >/dev/null
}

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
    validate_release_artifacts "$artifact_root"
}

validate_prebuilt_bundle() {
    local bundle_root="$1"
    [[ -d "$bundle_root" && ! -L "$bundle_root" ]] ||
        die "prebuilt bundle root is missing or unsafe: $bundle_root"

    local required
    for required in \
        BUNDLE-SHA256SUMS \
        artifacts/api/probe-api \
        setup/probe-setup \
        artifacts/agent/downloads/probe-agent/install.sh \
        artifacts/agent/downloads/probe-agent/SHA256SUMS \
        artifacts/web/index.html \
        artifacts/admin/index.html \
        source/probe-api/config/probe-api.env.example \
        source/probe-api/config/probe-postgres-backup.env.example \
        source/probe-api/deploy/nginx/nginx.conf \
        source/probe-api/deploy/nginx/nginx-ip.conf \
        source/probe-api/deploy/scripts/deploy-common.sh \
        source/probe-api/deploy/scripts/build-release-bundles.sh \
        source/probe-api/deploy/scripts/install-release.sh \
        source/probe-api/deploy/scripts/backup-postgres.sh \
        source/probe-api/deploy/scripts/restore-postgres.sh \
        source/probe-api/deploy/systemd/probe-api.service \
        source/probe-api/deploy/systemd/probe-postgres-backup.service \
        source/probe-api/deploy/systemd/probe-postgres-backup.timer; do
        [[ -f "$bundle_root/$required" && ! -L "$bundle_root/$required" ]] ||
            die "prebuilt bundle is missing a required regular file: $required"
    done

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

    validate_release_artifacts "$bundle_root/artifacts"
    validate_nginx_template_contract "$bundle_root/source/probe-api/deploy/nginx/nginx.conf" domain
    validate_nginx_template_contract "$bundle_root/source/probe-api/deploy/nginx/nginx-ip.conf" ip
    validate_systemd_unit_source "$bundle_root/source/probe-api/deploy/systemd/probe-api.service"
    validate_backup_unit_source \
        "$bundle_root/source/probe-api/deploy/systemd/probe-postgres-backup.service" \
        "$bundle_root/source/probe-api/deploy/systemd/probe-postgres-backup.timer"
}

install_example_file() {
    local source="$1" destination="$2" mode="$3" group="$4"
    local temporary="${destination}.new.$$"
    install -o root -g "$group" -m "$mode" -- "$source" "$temporary"
    mv -Tf -- "$temporary" "$destination"
}

prepare_system_layout() {
    local source_root="$1"

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
    install_example_file "${source_root}/probe-api/deploy/nginx/nginx.conf" \
        "${PROBE_NGINX_CONFIG_DIR}/nginx.conf.example" 0644 root
    install_example_file "${source_root}/probe-api/deploy/nginx/nginx-ip.conf" \
        "${PROBE_NGINX_CONFIG_DIR}/nginx-ip.conf.example" 0644 root
}

install_service_assets() {
    local source_root="$1"
    local unit_tmp="${PROBE_SYSTEMD_UNIT}.new.$$.service"
    install -o root -g root -m 0644 -- "${source_root}/probe-api/deploy/systemd/probe-api.service" "$unit_tmp"
    mv -Tf -- "$unit_tmp" "$PROBE_SYSTEMD_UNIT"

    local backup_script restore_script backup_service_tmp backup_timer_tmp
    backup_script="${PROBE_BACKUP_SCRIPT_DIR}/backup-postgres.sh"
    restore_script="${PROBE_BACKUP_SCRIPT_DIR}/restore-postgres.sh"
    install -o root -g probe-api -m 0750 -- \
        "${source_root}/probe-api/deploy/scripts/backup-postgres.sh" "${backup_script}.new.$$"
    install -o root -g probe-api -m 0750 -- \
        "${source_root}/probe-api/deploy/scripts/restore-postgres.sh" "${restore_script}.new.$$"
    mv -Tf -- "${backup_script}.new.$$" "$backup_script"
    mv -Tf -- "${restore_script}.new.$$" "$restore_script"

    backup_service_tmp="${PROBE_BACKUP_SERVICE_UNIT}.new.$$"
    backup_timer_tmp="${PROBE_BACKUP_TIMER_UNIT}.new.$$"
    install -o root -g root -m 0644 -- \
        "${source_root}/probe-api/deploy/systemd/probe-postgres-backup.service" "$backup_service_tmp"
    install -o root -g root -m 0644 -- \
        "${source_root}/probe-api/deploy/systemd/probe-postgres-backup.timer" "$backup_timer_tmp"
    mv -Tf -- "$backup_service_tmp" "$PROBE_BACKUP_SERVICE_UNIT"
    mv -Tf -- "$backup_timer_tmp" "$PROBE_BACKUP_TIMER_UNIT"

    if [[ -e "$PROBE_NGINX_LINK" || -L "$PROBE_NGINX_LINK" ]]; then
        [[ -L "$PROBE_NGINX_LINK" ]] || die "$PROBE_NGINX_LINK exists and is not a symbolic link"
        [[ "$(readlink -f -- "$PROBE_NGINX_LINK")" == "$PROBE_ACTIVE_NGINX_CONFIG" ]] ||
            die "$PROBE_NGINX_LINK points to an unexpected file"
    else
        ln -s -- "$PROBE_ACTIVE_NGINX_CONFIG" "$PROBE_NGINX_LINK"
    fi
}

validate_systemd_unit_source() {
    local unit_file="$1"
    [[ -f "$unit_file" && ! -L "$unit_file" ]] || die "probe-api systemd unit is missing"
    grep -Fxq 'User=probe-api' "$unit_file" || die "probe-api unit must use the probe-api account"
    grep -Fxq 'Group=probe-api' "$unit_file" || die "probe-api unit must use the probe-api group"
    grep -Fxq 'EnvironmentFile=/srv/probe/config/probe-api.env' "$unit_file" ||
        die "probe-api unit has an unexpected EnvironmentFile"
    grep -Fxq 'ExecStart=/srv/probe/api/probe-api serve' "$unit_file" ||
        die "probe-api unit has an unexpected ExecStart"
    grep -Fxq 'NoNewPrivileges=true' "$unit_file" || die "probe-api unit must enable NoNewPrivileges"
    grep -Fxq 'SocketBindAllow=tcp:8080' "$unit_file" || die "probe-api unit must allow only its loopback port"
    grep -Fxq 'SocketBindDeny=any' "$unit_file" || die "probe-api unit must deny other bind operations"
}

validate_backup_unit_source() {
    local service_file="$1" timer_file="$2"
    [[ -f "$service_file" && ! -L "$service_file" ]] || die "PostgreSQL backup service unit is missing"
    [[ -f "$timer_file" && ! -L "$timer_file" ]] || die "PostgreSQL backup timer unit is missing"
    grep -Fxq 'User=probe-api' "$service_file" || die "PostgreSQL backup service must use the probe-api account"
    grep -Fxq 'Group=probe-api' "$service_file" || die "PostgreSQL backup service must use the probe-api group"
    grep -Fxq 'EnvironmentFile=/srv/probe/config/probe-postgres-backup.env' "$service_file" ||
        die "PostgreSQL backup service has an unexpected EnvironmentFile"
    grep -Fxq 'ExecStart=/srv/probe/api/scripts/backup-postgres.sh' "$service_file" ||
        die "PostgreSQL backup service has an unexpected ExecStart"
    grep -Fxq 'NoNewPrivileges=true' "$service_file" ||
        die "PostgreSQL backup service must enable NoNewPrivileges"
    grep -Fxq 'ProtectSystem=strict' "$service_file" ||
        die "PostgreSQL backup service must protect the system filesystem"
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

    local api_source backup_source timer_source
    api_source="${source_root}/probe-api/deploy/systemd/probe-api.service"
    backup_source="${source_root}/probe-api/deploy/systemd/probe-postgres-backup.service"
    timer_source="${source_root}/probe-api/deploy/systemd/probe-postgres-backup.timer"
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
    [[ -x "${PROBE_BACKUP_SCRIPT_DIR}/backup-postgres.sh" ]] ||
        die "installed PostgreSQL backup script is missing"
    [[ -x "${PROBE_BACKUP_SCRIPT_DIR}/restore-postgres.sh" ]] ||
        die "installed PostgreSQL restore script is missing"
    [[ -f "$PROBE_BACKUP_SERVICE_UNIT" && ! -L "$PROBE_BACKUP_SERVICE_UNIT" ]] ||
        die "installed PostgreSQL backup service is missing"
    [[ -f "$PROBE_BACKUP_TIMER_UNIT" && ! -L "$PROBE_BACKUP_TIMER_UNIT" ]] ||
        die "installed PostgreSQL backup timer is missing"
    validate_backup_unit_source "$PROBE_BACKUP_SERVICE_UNIT" "$PROBE_BACKUP_TIMER_UNIT"
}

validate_backup_credentials() {
    assert_probe_api_service_account
    assert_private_file "$PROBE_PGPASS_FILE" probe-api
    [[ -s "$PROBE_PGPASS_FILE" ]] || die "$PROBE_PGPASS_FILE must not be empty"
    setpriv --reuid=probe-api --regid=probe-api --init-groups --reset-env -- \
        /usr/bin/setpriv --inh-caps=-all --ambient-caps=-all -- \
        /usr/bin/test -r "$PROBE_PGPASS_FILE" ||
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
    local artifact_root="$1" release_id="$2"
    local incoming="${PROBE_RELEASES_DIR}/.incoming-${release_id}"
    local final="${PROBE_RELEASES_DIR}/${release_id}"
    [[ ! -e "$incoming" && ! -e "$final" ]] || die "release identifier already exists: $release_id"

    install -d -o root -g root -m 0755 "$incoming" "$incoming/api" "$incoming/agent" "$incoming/web" "$incoming/admin" "$incoming/migrations"
    install -o root -g root -m 0755 -- "$artifact_root/api/probe-api" "$incoming/api/probe-api"
    cp -a -- "$artifact_root/agent/." "$incoming/agent/"
    cp -a -- "$artifact_root/web/." "$incoming/web/"
    cp -a -- "$artifact_root/admin/." "$incoming/admin/"
    cp -a -- "$artifact_root/migrations/." "$incoming/migrations/"
    find "$incoming/agent" "$incoming/web" "$incoming/admin" "$incoming/migrations" -type d -exec chmod 0755 {} +
    find "$incoming/agent" "$incoming/web" "$incoming/admin" "$incoming/migrations" -type f -exec chmod 0644 {} +
    chmod 0755 "$incoming/agent/downloads/probe-agent/install.sh" \
        "$incoming/agent/downloads/probe-agent/linux-amd64/probe-agent" \
        "$incoming/agent/downloads/probe-agent/linux-arm64/probe-agent"
    chown -R root:root "$incoming"

    (
        cd "$incoming"
        find api agent web admin migrations -type f -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS
    )
    chmod 0644 "$incoming/SHA256SUMS"
    mv -T -- "$incoming" "$final"
    printf '%s\n' "$final"
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
    [[ ! -e "$temporary" && ! -L "$temporary" ]] || return 1
    ln -s -- "$target" "$temporary" || return 1
    mv -Tf -- "$temporary" "$link_path" || return 1
}

activate_release() {
    local release_dir="$1"
    assert_switchable_path "${PROBE_API_DIR}/probe-api"
    assert_switchable_path "${PROBE_ROOT}/agent"
    assert_switchable_path "${PROBE_ROOT}/web"
    assert_switchable_path "${PROBE_ROOT}/admin"
    assert_switchable_path "${PROBE_ROOT}/migrations"

    atomic_release_link "${release_dir}/api/probe-api" "${PROBE_API_DIR}/probe-api" || return 1
    atomic_release_link "${release_dir}/agent" "${PROBE_ROOT}/agent" || return 1
    atomic_release_link "${release_dir}/web" "${PROBE_ROOT}/web" || return 1
    atomic_release_link "${release_dir}/admin" "${PROBE_ROOT}/admin" || return 1
    atomic_release_link "${release_dir}/migrations" "${PROBE_ROOT}/migrations" || return 1
}

validate_switchable_release_paths() {
    assert_switchable_path "${PROBE_API_DIR}/probe-api"
    assert_switchable_path "${PROBE_ROOT}/agent"
    assert_switchable_path "${PROBE_ROOT}/web"
    assert_switchable_path "${PROBE_ROOT}/admin"
    assert_switchable_path "${PROBE_ROOT}/migrations"
}

current_release_target() {
    local link_path="$1"
    if [[ -L "$link_path" ]]; then
        readlink -- "$link_path"
    fi
    return 0
}

restore_release_links() {
    local old_api="$1" old_agent="$2" old_web="$3" old_admin="$4" old_migrations="$5"
    local link_path target
    local -a links=("${PROBE_API_DIR}/probe-api" "${PROBE_ROOT}/agent" "${PROBE_ROOT}/web" "${PROBE_ROOT}/admin" "${PROBE_ROOT}/migrations")
    local -a targets=("$old_api" "$old_agent" "$old_web" "$old_admin" "$old_migrations")

    for ((index=0; index<${#links[@]}; index++)); do
        link_path="${links[$index]}"
        target="${targets[$index]}"
        if [[ -n "$target" ]]; then
            atomic_release_link "$target" "$link_path"
        else
            rm -f -- "$link_path"
        fi
    done
}

create_database_backup() (
    local release_id="$1"
    local temporary="${PROBE_BACKUPS_DIR}/.pre-upgrade-${release_id}.dump"
    local final="${PROBE_BACKUPS_DIR}/pre-upgrade-${release_id}.dump"
    # Invoked indirectly by the EXIT trap below.
    # shellcheck disable=SC2317
    cleanup_failed_database_backup() {
        local status=$?
        trap - EXIT HUP INT TERM
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
        /usr/bin/pg_dump --no-password --format=custom --file="$temporary"; then
        die "PostgreSQL pre-migration backup failed"
    fi
    if [[ ! -s "$temporary" ]]; then
        die "PostgreSQL pre-migration backup is empty"
    fi
    chmod 0600 "$temporary" || {
        die "secure the PostgreSQL pre-migration backup"
    }
    if ! pg_restore --list "$temporary" >/dev/null; then
        die "PostgreSQL pre-migration backup verification failed"
    fi
    mv -T -- "$temporary" "$final" || {
        die "retain the PostgreSQL pre-migration backup"
    }
    trap - EXIT HUP INT TERM
    printf '%s\n' "$final"
)

persist_native_nginx_service() {
    local fragment wants_link
    fragment="$(systemctl show --property=FragmentPath --value nginx.service)"
    case "$fragment" in
        /usr/lib/systemd/system/nginx.service|/lib/systemd/system/nginx.service) ;;
        *) die "nginx.service is not the expected Debian native systemd unit" ;;
    esac

    systemctl add-wants multi-user.target nginx.service >/dev/null ||
        die "persist nginx.service in multi-user.target"
    wants_link="/etc/systemd/system/multi-user.target.wants/nginx.service"
    [[ -L "$wants_link" && "$(readlink -f -- "$wants_link")" == "$(readlink -f -- "$fragment")" ]] ||
        die "nginx.service multi-user.target link is missing or unsafe"
}

run_migrations() {
    local api_binary="$1"
    "$api_binary" migrate status
    "$api_binary" migrate up
    "$api_binary" migrate status
}

validate_runtime_listeners() {
    require_commands ss
    local listeners
    # The production validator also runs inside the one-time Finalizer's
    # ProtectProc=invisible sandbox. Process ownership from ss -p is therefore
    # intentionally unavailable. Nginx has already passed nginx -T, service
    # activation, and is-active checks; validate its reviewed ingress ports by
    # presence while retaining strict loopback checks for API and PostgreSQL.
    listeners="$(ss -H -lnt)"

    awk -v mode="$PROBE_INGRESS_MODE" '
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
            if (mode == "ip" && (port == "18453" || port == "18454" || port == "18455")) {
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
            if (mode == "ip" && (!ingress["18453"] || !ingress["18454"] || !ingress["18455"])) bad=1
            if (api_count < 1 || postgres_count < 1) bad=1
            if (bad) exit 1
            exit 0
        }
    ' <<<"$listeners" || die "runtime listeners violate the ingress or loopback-only service contract"
}

verify_running_services() {
    assert_probe_api_service_account
    systemctl is-active --quiet postgresql.service || die "PostgreSQL is not active"
    systemctl is-active --quiet probe-api.service || die "probe-api is not active"
    systemctl is-active --quiet nginx.service || die "Nginx is not active"
    systemctl is-active --quiet probe-postgres-backup.timer ||
        die "probe-postgres-backup.timer is not active"
    validate_ingress_tls_with_binary "${PROBE_API_DIR}/probe-api"
    validate_certbot_timer_state
    curl --fail --silent --show-error --max-time 10 \
        http://127.0.0.1:8080/internal/health/ready >/dev/null ||
        die "probe-api readiness check failed"
    validate_runtime_listeners
}

deploy_prebuilt_release() {
    local bundle_root="$1" release_id="$2"
    require_commands bash cp env find install sha256sum sort xargs pg_dump pg_restore nginx systemctl systemd-analyze setpriv curl ss flock awk grep sed stat readlink python3

    bundle_root="$(canonical_directory "$bundle_root")"
    [[ "$release_id" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$ ]] ||
        die "prebuilt release identifier is invalid"

    validate_prebuilt_bundle "$bundle_root"

    local source_root="$bundle_root/source"
    local artifact_root="$bundle_root/artifacts"
    prepare_system_layout "$source_root"
    acquire_database_maintenance_lock
    clear_exported_probe_environment
    load_probe_env
    local ingress_mode="$PROBE_INGRESS_MODE" nginx_template
    nginx_template="$(selected_nginx_template "$source_root")"
    validate_active_nginx_config "$nginx_template"
    validate_backup_credentials
    validate_switchable_release_paths
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
    validate_ingress_tls_with_binary "$artifact_root/api/probe-api"
    validate_certbot_timer_state
    install_service_assets "$source_root"
    validate_nginx_runtime_config "$nginx_template"

    local unique_release_id release_dir backup_path
    unique_release_id="${release_id}-$(date -u +%Y%m%dT%H%M%SZ)-$$"
    release_dir="$(stage_release "$artifact_root" "$unique_release_id")"
    validate_release_artifacts "$release_dir"
    (
        cd "$release_dir"
        sha256sum --check --strict SHA256SUMS
    )

    systemctl start postgresql.service
    backup_path="$(create_database_backup "$unique_release_id")"
    log "database backup retained at $backup_path"
    run_migrations "$release_dir/api/probe-api"

    local old_api old_agent old_web old_admin old_migrations
    old_api="$(current_release_target "${PROBE_API_DIR}/probe-api")"
    old_agent="$(current_release_target "${PROBE_ROOT}/agent")"
    old_web="$(current_release_target "${PROBE_ROOT}/web")"
    old_admin="$(current_release_target "${PROBE_ROOT}/admin")"
    old_migrations="$(current_release_target "${PROBE_ROOT}/migrations")"

    if ! activate_release "$release_dir"; then
        restore_release_links "$old_api" "$old_agent" "$old_web" "$old_admin" "$old_migrations"
        die "release link activation failed; the forward database migration remains applied and backup is $backup_path"
    fi

    if ! systemctl daemon-reload ||
       ! systemd-analyze verify "$PROBE_SYSTEMD_UNIT" "$PROBE_BACKUP_SERVICE_UNIT" "$PROBE_BACKUP_TIMER_UNIT" ||
       ! validate_backup_service_assets ||
       ! systemctl enable probe-api.service probe-postgres-backup.timer >/dev/null ||
       ! persist_native_nginx_service ||
       ! systemctl start postgresql.service ||
       ! systemctl restart probe-api.service ||
       ! systemctl start probe-postgres-backup.timer ||
       ! systemctl reload-or-restart nginx.service ||
       ! ( verify_running_services ); then
        warn "new prebuilt release failed runtime verification; restoring prior application links"
        restore_release_links "$old_api" "$old_agent" "$old_web" "$old_admin" "$old_migrations"
        systemctl restart probe-api.service || true
        systemctl reload-or-restart nginx.service || true
        die "release activation failed; the forward database migration remains applied and backup is $backup_path"
    fi

    rm -rf -- "$verify_root"
    PROBE_DEPLOY_WORK_ROOT=""
    log "prebuilt release ${unique_release_id} is active"
    log "previous release directories and $backup_path were retained"
}

deploy_release() {
    local source_root="$1" run_tests="$2" validate_only="$3"
    require_commands bash go npm node cp env find install sha256sum sort xargs pg_dump pg_restore nginx systemctl systemd-analyze setpriv curl ss flock awk grep sed stat readlink python3

    assert_probe_api_service_account
    acquire_database_maintenance_lock
    validate_deployment_script_sources "$source_root"
    clear_exported_probe_environment
    load_probe_env
    local ingress_mode="$PROBE_INGRESS_MODE" nginx_template
    nginx_template="$(selected_nginx_template "$source_root")"
    validate_active_nginx_config "$nginx_template"
    validate_backup_credentials
    validate_systemd_unit_source "${source_root}/probe-api/deploy/systemd/probe-api.service"
    validate_switchable_release_paths

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
    validate_ingress_tls_with_binary "$artifact_root/api/probe-api"
    validate_certbot_timer_state

    if [[ "$validate_only" == true ]]; then
        [[ -f "$PROBE_SYSTEMD_UNIT" ]] || die "installed probe-api systemd unit is missing"
        systemd-analyze verify "$PROBE_SYSTEMD_UNIT"
        validate_backup_service_assets
        systemd-analyze verify "$PROBE_BACKUP_SERVICE_UNIT" "$PROBE_BACKUP_TIMER_UNIT"
        validate_nginx_runtime_config "$nginx_template"
        rm -rf -- "$work_root"
        PROBE_DEPLOY_WORK_ROOT=""
        log "validation completed; no database, release link, or service state was changed"
        return 0
    fi

    install_service_assets "$source_root"
    validate_nginx_runtime_config "$nginx_template"

    release_dir="$(stage_release "$artifact_root" "$release_id")"
    validate_release_artifacts "$release_dir"
    (
        cd "$release_dir"
        sha256sum --check --strict SHA256SUMS
    )
    systemctl start postgresql.service
    backup_path="$(create_database_backup "$release_id")"
    log "database backup retained at $backup_path"
    run_migrations "$release_dir/api/probe-api"

    local old_api old_agent old_web old_admin old_migrations
    old_api="$(current_release_target "${PROBE_API_DIR}/probe-api")"
    old_agent="$(current_release_target "${PROBE_ROOT}/agent")"
    old_web="$(current_release_target "${PROBE_ROOT}/web")"
    old_admin="$(current_release_target "${PROBE_ROOT}/admin")"
    old_migrations="$(current_release_target "${PROBE_ROOT}/migrations")"

    if ! activate_release "$release_dir"; then
        restore_release_links "$old_api" "$old_agent" "$old_web" "$old_admin" "$old_migrations"
        die "release link activation failed; the forward database migration remains applied and backup is $backup_path"
    fi

    if ! systemctl daemon-reload ||
       ! systemd-analyze verify "$PROBE_SYSTEMD_UNIT" "$PROBE_BACKUP_SERVICE_UNIT" "$PROBE_BACKUP_TIMER_UNIT" ||
       ! validate_backup_service_assets ||
       ! systemctl enable probe-api.service probe-postgres-backup.timer >/dev/null ||
       ! persist_native_nginx_service ||
       ! systemctl start postgresql.service ||
       ! systemctl restart probe-api.service ||
       ! systemctl start probe-postgres-backup.timer ||
       ! systemctl reload-or-restart nginx.service ||
       ! ( verify_running_services ); then
        warn "new release failed runtime verification; restoring prior application links"
        restore_release_links "$old_api" "$old_agent" "$old_web" "$old_admin" "$old_migrations"
        systemctl restart probe-api.service || true
        systemctl reload-or-restart nginx.service || true
        die "release activation failed; the forward database migration remains applied and backup is $backup_path"
    fi

    log "release ${release_id} is active"
    log "previous release directories and $backup_path were retained"
    rm -rf -- "$work_root"
    PROBE_DEPLOY_WORK_ROOT=""
}
