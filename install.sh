#!/usr/bin/env bash

# Probe Panel server bootstrap for Debian 13. This script installs verified
# prebuilt application assets plus the runtime packages required by the local
# setup flow. It never receives database or administrator credentials.

set -Eeuo pipefail
umask 077

PROGRAM="${0##*/}"
PANEL_VERSION="${PROBE_PANEL_RELEASE_VERSION:-v1.1.0}"
RELEASE_BASE_URL="${PROBE_PANEL_RELEASE_BASE_URL:-https://github.com/Kcmose/super-my/releases/download/${PANEL_VERSION}}"
SUPER_MY_REF="refs/tags/v1.1.0"
WEB_REF="refs/tags/v1.0.0"
AGENT_REF="refs/tags/v1.0.2"
LEGACY_PANEL_VERSION="v1.0.0"
LEGACY_SUPER_MY_REF="refs/tags/v1.0.0"
LEGACY_AGENT_REF="refs/tags/v1.0.1"

SETUP_STATE_ROOT="/var/lib/probe-panel/setup"
SETUP_STATE_FILE="${SETUP_STATE_ROOT}/state.json"
LEGACY_SETUP_CODE_FILE="${SETUP_STATE_ROOT}/setup-code.json"
SETUP_CONFIG_ROOT="/etc/probe-panel"
SETUP_ENV_FILE="${SETUP_CONFIG_ROOT}/setup.env"
SETUP_UI_ROOT="/srv/probe/setup-ui"
RELEASES_ROOT="/srv/probe/releases"
PROGRAM_ROOT="/usr/local/lib/probe-panel"
SETUP_BINARY="${PROGRAM_ROOT}/probe-setup"
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

TEMP_ROOT=""
INSTALLED_RELEASE=""
CREATED_STATE=0
CREATED_ENV=0
INSTALLED_UNIT=0
INSTALL_COMPLETED=0
NGINX_MASKED_BY_INSTALLER=0
SETUP_SERVER_IP=""
VERIFIED_BUNDLE_ROOT=""
LEGACY_RELEASE=""
MIGRATION_STATE_STATUS=""
MIGRATION_BACKUP=""
MIGRATION_UI_STAGE=""
MIGRATION_UI_OLD=""
MIGRATION_QUIESCED=0
MIGRATION_STARTED=0
MIGRATION_UI_SWAPPED=0
MIGRATION_NEW_RELEASE=0
MIGRATION_COMPLETED=0
MIGRATION_SOCKET_UNIT_INSTALLED=0

usage() {
    cat <<EOF
Usage: ${PROGRAM} [install|migrate-bootstrap|status|uninstall]

Commands:
  install      Install the root-SSH-only first-run setup service (default).
  migrate-bootstrap
               Safely replace an unfinished, verified v1.0.0 bootstrap with
               v1.1.0. Formal or recovery-state installations are refused.
  status       Show bootstrap files and setup-service status without secrets.
  uninstall    Remove bootstrap programs while preserving configuration,
               setup state, PostgreSQL data, and backups.
  purge        Not supported. Data removal must be an explicit, separately
               reviewed operation with a verified final backup.
  -h, --help   Show this help.

The installer accepts no database or administrator credentials. After install,
open /install through the printed root SSH tunnel and enter secrets only there.
Nginx remains stopped and disabled until successful finalization; PostgreSQL
starts with a verified local-only listener.
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

require_debian_13() {
    [[ -r /etc/os-release ]] || die '/etc/os-release is missing'

    local os_id='' version_id='' key value
    while IFS='=' read -r key value; do
        value="${value%\"}"
        value="${value#\"}"
        case "$key" in
            ID) os_id="$value" ;;
            VERSION_ID) version_id="$value" ;;
        esac
    done < /etc/os-release

    [[ "$os_id" == debian && "$version_id" == 13 ]] ||
        die "only Debian 13 is supported (found ${os_id:-unknown} ${version_id:-unknown})"
}

detect_architecture() {
    case "$(uname -m)" in
        x86_64|amd64) printf '%s\n' amd64 ;;
        aarch64|arm64) printf '%s\n' arm64 ;;
        *) die "unsupported architecture: $(uname -m); expected amd64 or arm64" ;;
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
    [[ "$PANEL_VERSION" == v1.1.0 ]] ||
        die 'this installer is pinned to the verified Probe Panel v1.1.0 release'
    validate_https_url "$RELEASE_BASE_URL"
}

unit_is_installed() {
    local load_state fragment
    load_state="$(systemctl show --property=LoadState --value "$1" 2>/dev/null || :)"
    fragment="$(systemctl show --property=FragmentPath --value "$1" 2>/dev/null || :)"
    [[ -n "$fragment" || ( -n "$load_state" && "$load_state" != not-found ) ]]
}

assert_fresh_target() {
    local path
    if [[ -e "$LEGACY_SETUP_CODE_FILE" || -L "$LEGACY_SETUP_CODE_FILE" ]]; then
        die "a legacy bootstrap record is present; do not reinstall over it. If it is an unfinished exact v1.0.0 pending/configuring bootstrap, run this pinned v1.1.0 installer with 'migrate-bootstrap'; every other state requires reviewed recovery"
    fi
    for path in \
        "$SETUP_BINARY" "$SETUP_UNIT" "$SETUP_SOCKET_UNIT" "$FINALIZER_UNIT" "$FINALIZER_PATH_UNIT" \
        "$SETUP_ENV_FILE" "$SETUP_UI_ROOT" \
        "$SETUP_STATE_FILE" "$LEGACY_SETUP_CODE_FILE" \
        /etc/systemd/system/probe-api.service \
        /etc/nginx/conf.d/probe-panel.conf \
        /srv/probe/api/probe-api /srv/probe/admin /srv/probe/web; do
        if [[ -e "$path" || -L "$path" ]]; then
            die "existing or partial Probe Panel installation found at $path; refusing to overwrite it"
        fi
    done
    if unit_is_installed "$SETUP_SERVICE" || unit_is_installed "$SETUP_SOCKET_SERVICE" || unit_is_installed "$FINALIZER_SERVICE" ||
       unit_is_installed "$FINALIZER_PATH_SERVICE" || unit_is_installed probe-api.service; then
        die 'an existing Probe Panel systemd service was found; refusing to replace it'
    fi
    if systemctl is-active --quiet nginx.service; then
        die 'Nginx is already active; use a dedicated host or stop and review the existing sites before installing'
    fi
}

install_runtime_dependencies() {
    log 'installing download tools and first-run runtime dependencies'
    local nginx_enable_state
    nginx_enable_state="$(systemctl is-enabled nginx.service 2>/dev/null || :)"
    [[ "$nginx_enable_state" != masked ]] ||
        die 'nginx.service is already masked; review the existing host before installation'
    systemctl mask nginx.service >/dev/null
    NGINX_MASKED_BY_INSTALLER=1
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y --no-install-recommends \
        ca-certificates curl python3 \
        nginx postgresql postgresql-client certbot \
        iproute2 util-linux
    for dependency in curl python3 sha256sum nginx psql certbot ss ip runuser setpriv; do
        require_command "$dependency"
    done
}

assert_local_postgresql() {
    local listen_addresses address
    local -a postgres_addresses=()
    listen_addresses="$(runuser -u postgres -- psql --no-psqlrc -Atqc 'SHOW listen_addresses')" ||
        die 'could not query the local PostgreSQL listener configuration'
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
        systemctl stop postgresql.service >/dev/null 2>&1 || :
        die 'PostgreSQL TCP 5432 is exposed beyond loopback; the service was stopped'
    fi
}

prepare_runtime_services() {
    log 'keeping Nginx disabled until setup finalization'
    systemctl stop nginx.service
    systemctl disable nginx.service >/dev/null
    systemctl unmask nginx.service >/dev/null
    NGINX_MASKED_BY_INSTALLER=0
    systemctl reset-failed nginx.service >/dev/null 2>&1 || :
    systemctl is-active --quiet nginx.service && die 'Nginx did not stop cleanly'
    local default_site=/etc/nginx/sites-enabled/default
    if [[ -e "$default_site" || -L "$default_site" ]]; then
        [[ -L "$default_site" && "$(readlink -f -- "$default_site")" == /etc/nginx/sites-available/default ]] ||
            die "$default_site is not Debian's stock default-site symlink; review it manually"
        unlink -- "$default_site"
        log 'disabled the stock Debian Nginx default site'
    fi
    log 'starting local PostgreSQL for the setup wizard'
    systemctl enable --now postgresql.service >/dev/null
    systemctl is-active --quiet postgresql.service || die 'PostgreSQL did not start'
    assert_local_postgresql
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
    [[ "$account_shell" == /usr/sbin/nologin ]] ||
        die 'probe-api must use /usr/sbin/nologin as its shell'

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
    # Debian 13's stock file relies on that default, so preserve it while still
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
        die 'probe-api UID is outside the Debian system-account range'

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
    require_command addgroup
    require_command adduser
    require_command getent

    local passwd_record group_record
    passwd_record="$(getent passwd probe-api || :)"
    group_record="$(getent group probe-api || :)"
    if [[ -z "$passwd_record" && -z "$group_record" ]]; then
        addgroup --system probe-api
        adduser --system --ingroup probe-api --no-create-home --home /nonexistent \
            --shell /usr/sbin/nologin probe-api
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
    raise SystemExit(f"invalid detected server IP: {error}")
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
    curl -q --fail --silent --show-error --location \
        --proto '=https' --proto-redir '=https' --tlsv1.2 \
        --connect-timeout 15 --max-time "$maximum_seconds" \
        --output "$destination" "$url"
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
import os
import sys
import tarfile
from pathlib import PurePosixPath

archive_path, destination = sys.argv[1:]
destination_real = os.path.realpath(destination)
seen = set()

with tarfile.open(archive_path, mode="r:gz") as bundle:
    members = bundle.getmembers()
    if not members or len(members) > 20000:
        raise SystemExit("release archive is empty or has too many entries")
    for member in members:
        path = PurePosixPath(member.name)
        if path.is_absolute() or not path.parts or ".." in path.parts:
            raise SystemExit(f"unsafe archive path: {member.name!r}")
        if any(part in ("", ".") for part in path.parts):
            raise SystemExit(f"non-canonical archive path: {member.name!r}")
        if member.name in seen:
            raise SystemExit(f"duplicate archive path: {member.name!r}")
        seen.add(member.name)
        if member.issym() or member.islnk():
            raise SystemExit(f"links are forbidden in release archives: {member.name!r}")
        if not (member.isfile() or member.isdir()):
            raise SystemExit(f"special files are forbidden in release archives: {member.name!r}")
        resolved = os.path.realpath(os.path.join(destination_real, *path.parts))
        if os.path.commonpath((destination_real, resolved)) != destination_real:
            raise SystemExit(f"archive path escapes extraction root: {member.name!r}")
    bundle.extractall(destination_real, members=members, filter="data")
PY
}

validate_release_bundle() {
    local root="$1" architecture="$2"
    local manifest="$root/RELEASE-MANIFEST"
    local required
    [[ -f "$manifest" && ! -L "$manifest" ]] || die 'release metadata is missing'
    grep -Fxq 'format=probe-panel-release-v1' "$manifest" || die 'release metadata format is invalid'
    grep -Fxq "version=$PANEL_VERSION" "$manifest" || die 'release metadata version is invalid'
    grep -Fxq "architecture=linux-$architecture" "$manifest" || die 'release architecture does not match this server'
    grep -Fxq "super_my_ref=$SUPER_MY_REF" "$manifest" || die 'server source ref is not the verified release ref'
    grep -Fxq "my_ref=$WEB_REF" "$manifest" || die 'visitor source ref is not the verified release ref'
    grep -Fxq "my_agent_ref=$AGENT_REF" "$manifest" || die 'Agent source ref is not the verified release ref'

    for required in \
        BUNDLE-SHA256SUMS \
        artifacts/api/probe-api \
        setup/probe-setup \
        artifacts/admin/index.html \
        artifacts/web/index.html \
        artifacts/agent/downloads/probe-agent/install.sh \
        artifacts/agent/downloads/probe-agent/SHA256SUMS \
        artifacts/agent/downloads/probe-agent/probe-agent.service \
        artifacts/agent/downloads/probe-agent/linux-amd64/probe-agent \
        artifacts/agent/downloads/probe-agent/linux-arm64/probe-agent \
        source/probe-api/config/probe-api.env.example \
        source/probe-api/config/probe-postgres-backup.env.example \
        source/probe-api/deploy/scripts/deploy-common.sh \
        source/probe-api/deploy/scripts/build-release-bundles.sh \
        source/probe-api/deploy/nginx/nginx.conf \
        source/probe-api/deploy/nginx/nginx-ip.conf \
        source/probe-api/deploy/scripts/install-release.sh \
        source/probe-api/deploy/scripts/backup-postgres.sh \
        source/probe-api/deploy/scripts/restore-postgres.sh \
        source/probe-api/deploy/setup/probe-panel-setup.service \
        source/probe-api/deploy/setup/probe-panel-setup.socket \
        source/probe-api/deploy/setup/probe-panel-finalizer.service \
        source/probe-api/deploy/setup/probe-panel-finalizer.path \
        source/probe-api/deploy/systemd/probe-api.service \
        source/probe-api/deploy/systemd/probe-postgres-backup.service \
        source/probe-api/deploy/systemd/probe-postgres-backup.timer; do
        [[ -f "$root/$required" && ! -L "$root/$required" ]] ||
            die "release bundle is incomplete: $required"
    done
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

    local unit="$root/source/probe-api/deploy/setup/probe-panel-setup.service"
    local socket_unit="$root/source/probe-api/deploy/setup/probe-panel-setup.socket"
    grep -Fxq 'User=root' "$unit" || die 'setup service must use the root-owned setup state'
    grep -Fxq 'EnvironmentFile=/etc/probe-panel/setup.env' "$unit" || die 'setup service environment path changed'
    grep -Fxq 'ExecStart=/usr/local/lib/probe-panel/probe-setup serve' "$unit" || die 'setup service executable path changed'
    grep -Fxq 'CapabilityBoundingSet=' "$unit" || die 'HTTP setup service must have an empty capability set'
    grep -Fxq 'ReadWritePaths=/run/probe-panel-setup' "$unit" || die 'HTTP setup runtime request path changed'
    grep -Fxq 'RestrictAddressFamilies=AF_UNIX' "$unit" || die 'setup service must accept Unix sockets only'
    grep -Fxq 'PrivateNetwork=true' "$unit" || die 'setup service must have a private network namespace'
    [[ "$(grep -Fc 'SocketBindAllow=' "$unit")" -eq 0 ]] || die 'setup service must not bind any IP socket'
    grep -Fxq 'SocketBindDeny=any' "$unit" || die 'setup service must deny every other bind operation'
    grep -Fxq 'ListenStream=/run/probe-panel-setup/setup.sock' "$socket_unit" || die 'setup Unix socket path changed'
    grep -Fxq 'SocketMode=0600' "$socket_unit" || die 'setup Unix socket must be root-private'
    grep -Fxq 'DirectoryMode=0700' "$socket_unit" || die 'setup Unix socket directory must be root-private'
    grep -Fxq 'RemoveOnStop=yes' "$socket_unit" || die 'setup Unix socket must be removed when stopped'

    local finalizer_unit="$root/source/probe-api/deploy/setup/probe-panel-finalizer.service"
    local finalizer_path="$root/source/probe-api/deploy/setup/probe-panel-finalizer.path"
    grep -Fxq 'ExecStart=/usr/local/lib/probe-panel/probe-setup finalize' "$finalizer_unit" ||
        die 'setup finalizer executable path changed'
    local finalizer_sleep_line finalizer_socket_stop_line finalizer_service_stop_line
    [[ "$(grep -Fc 'ExecStopPost=' "$finalizer_unit")" -eq 3 ]] ||
        die 'setup finalizer must have exactly three reviewed post-stop actions'
    finalizer_sleep_line="$(grep -Fnx 'ExecStopPost=/usr/bin/sleep 20' "$finalizer_unit" | cut -d: -f1)"
    finalizer_socket_stop_line="$(grep -Fnx 'ExecStopPost=/usr/bin/systemctl stop probe-panel-setup.socket' "$finalizer_unit" | cut -d: -f1)"
    finalizer_service_stop_line="$(grep -Fnx 'ExecStopPost=/usr/bin/systemctl --no-block stop probe-panel-setup.service' "$finalizer_unit" | cut -d: -f1)"
    [[ "$finalizer_sleep_line" =~ ^[0-9]+$ &&
       "$finalizer_socket_stop_line" =~ ^[0-9]+$ &&
       "$finalizer_service_stop_line" =~ ^[0-9]+$ &&
       "$finalizer_sleep_line" -lt "$finalizer_socket_stop_line" &&
       "$finalizer_socket_stop_line" -lt "$finalizer_service_stop_line" ]] ||
        die 'setup finalizer must delay, stop the socket, then stop the setup service in that order'
    grep -Fxq 'TimeoutStartSec=30min' "$finalizer_unit" ||
        die 'setup finalizer timeout must match the 30-minute broker deadline'
    grep -Fxq 'CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE CAP_FOWNER CAP_NET_BIND_SERVICE CAP_SETGID CAP_SETUID' "$finalizer_unit" ||
        die 'setup finalizer capability boundary changed'
    grep -Fxq 'AmbientCapabilities=CAP_SETGID CAP_SETUID' "$finalizer_unit" ||
        die 'setup finalizer identity-switch capability contract changed'
    grep -Fxq 'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK' "$finalizer_unit" ||
        die 'setup finalizer listener-inspection address-family contract changed'
    local writable_path
    for writable_path in \
        /etc/probe-panel /etc/nginx/conf.d /etc/nginx/sites-enabled /etc/systemd/system /etc/letsencrypt \
        /var/lib/letsencrypt /var/log/letsencrypt /var/log/nginx /var/lib/nginx /var/lib/probe-panel/setup /srv/probe \
        /var/backups/probe-panel /run/lock /run/probe-panel-setup; do
        grep -Fxq "ReadWritePaths=$writable_path" "$finalizer_unit" ||
            die "setup finalizer is missing its reviewed writable path: $writable_path"
    done
    ! grep -Fxq 'ReadWritePaths=/etc/nginx' "$finalizer_unit" ||
        die 'setup finalizer must not make the entire Nginx configuration directory writable'
    [[ "$(grep -Fc 'SocketBindAllow=' "$finalizer_unit")" -eq 5 ]] ||
        die 'setup finalizer must have exactly five reviewed ingress bind rules'
    local finalizer_bind_rule
    for finalizer_bind_rule in tcp:80 tcp:443 tcp:18453 tcp:18454 tcp:18455; do
        grep -Fxq "SocketBindAllow=$finalizer_bind_rule" "$finalizer_unit" ||
            die "setup finalizer is missing its reviewed ingress bind rule: $finalizer_bind_rule"
    done
    grep -Fxq 'SocketBindDeny=any' "$finalizer_unit" || die 'setup finalizer bind policy changed'
    ! grep -Fxq 'SocketBindAllow=tcp:18080' "$finalizer_unit" ||
        die 'non-HTTP setup finalizer must not bind the setup listener'
    grep -Fxq 'PathExists=/run/probe-panel-setup/finalize.json' "$finalizer_path" ||
        die 'setup finalizer request path changed'
}

assert_root_regular_file() {
    local path="$1" mode="$2"
    [[ -f "$path" && ! -L "$path" ]] || die "legacy bootstrap file is missing or unsafe: $path"
    [[ "$(stat -c '%U:%G' "$path")" == root:root ]] ||
        die "legacy bootstrap file is not owned by root:root: $path"
    [[ "$(stat -c '%a' "$path")" == "$mode" ]] ||
        die "legacy bootstrap file has an unexpected mode: $path"
}

assert_root_directory() {
    local path="$1" mode="$2"
    [[ -d "$path" && ! -L "$path" ]] || die "legacy bootstrap directory is missing or unsafe: $path"
    [[ "$(stat -c '%U:%G' "$path")" == root:root && "$(stat -c '%a' "$path")" == "$mode" ]] ||
        die "legacy bootstrap directory ownership or mode is invalid: $path"
}

assert_directory_contains_only() {
    local label="$1" directory="$2"
    shift 2
    local entry entry_name allowed_name allowed

    # Keep glob options scoped to a subshell. Explicit hidden-file patterns
    # avoid both skipping dotfiles and treating an unmatched literal `.*` as
    # an unexpected entry on otherwise valid, empty layouts.
    (
        shopt -s nullglob
        for entry in "$directory"/* "$directory"/.[!.]* "$directory"/..?*; do
            entry_name="${entry##*/}"
            allowed=0
            for allowed_name in "$@"; do
                if [[ "$entry_name" == "$allowed_name" ]]; then
                    allowed=1
                    break
                fi
            done
            ((allowed == 1)) || die "$label contains an unexpected entry: $entry"
        done
    )
}

validate_legacy_release_bundle() {
    local root="$1" architecture="$2"
    local manifest="$root/RELEASE-MANIFEST" required expected_paths manifest_paths entry

    [[ -d "$root" && ! -L "$root" ]] || die 'the immutable v1.0.0 release directory is missing or unsafe'
    [[ "$(stat -c '%U:%G' "$root")" == root:root ]] || die 'the v1.0.0 release is not root-owned'
    assert_root_regular_file "$root/$MANAGED_MARKER" 600
    assert_root_regular_file "$manifest" 644
    # The immutable v1.0.0 bootstrap copies its verified checksum manifest
    # into the installed release under the installer umask, so its exact
    # deployed mode is 0600 rather than the source archive's 0644.
    assert_root_regular_file "$root/BUNDLE-SHA256SUMS" 600

    [[ "$(wc -l < "$manifest")" -eq 6 ]] || die 'the v1.0.0 release metadata has unexpected fields'
    grep -Fxq 'format=probe-panel-release-v1' "$manifest" || die 'the v1.0.0 release metadata format is invalid'
    grep -Fxq "version=$LEGACY_PANEL_VERSION" "$manifest" || die 'the legacy release version is not v1.0.0'
    grep -Fxq "architecture=linux-$architecture" "$manifest" || die 'the v1.0.0 release architecture does not match this server'
    grep -Fxq "super_my_ref=$LEGACY_SUPER_MY_REF" "$manifest" || die 'the v1.0.0 server source ref is invalid'
    grep -Fxq "my_ref=$WEB_REF" "$manifest" || die 'the v1.0.0 visitor source ref is invalid'
    grep -Fxq "my_agent_ref=$LEGACY_AGENT_REF" "$manifest" || die 'the v1.0.0 Agent source ref is invalid'

    assert_directory_contains_only \
        'the v1.0.0 release' "$root" \
        artifacts setup source BUNDLE-SHA256SUMS RELEASE-MANIFEST "$MANAGED_MARKER"
    [[ -z "$(find "$root" -type l -print -quit)" ]] || die 'the v1.0.0 release contains a symbolic link'
    [[ -z "$(find "$root" ! -type d ! -type f -print -quit)" ]] || die 'the v1.0.0 release contains a special file'

    for required in \
        BUNDLE-SHA256SUMS RELEASE-MANIFEST setup/probe-setup \
        artifacts/api/probe-api artifacts/admin/index.html artifacts/web/index.html \
        artifacts/migrations source/probe-api/deploy/nginx/nginx.conf \
        source/probe-api/deploy/setup/probe-panel-setup.service \
        source/probe-api/deploy/setup/probe-panel-finalizer.service \
        source/probe-api/deploy/setup/probe-panel-finalizer.path; do
        [[ -e "$root/$required" && ! -L "$root/$required" ]] ||
            die "the v1.0.0 release is incomplete: $required"
    done
    [[ -d "$root/artifacts/migrations" ]] || die 'the v1.0.0 migrations path is invalid'

    expected_paths="$(
        cd "$root"
        find artifacts setup source -type f -print | LC_ALL=C sort
    )"
    manifest_paths="$(awk '{ print $2 }' "$root/BUNDLE-SHA256SUMS" | LC_ALL=C sort)"
    [[ -n "$expected_paths" && "$manifest_paths" == "$expected_paths" ]] ||
        die 'the v1.0.0 internal checksum manifest does not exactly cover the release'
    (
        cd "$root"
        sha256sum --check --strict BUNDLE-SHA256SUMS
    ) >/dev/null || die 'the v1.0.0 immutable release failed its internal SHA256 verification'
}

validate_legacy_bootstrap_metadata() {
    local legacy_release="$1"
    python3 - \
        "$SETUP_STATE_ROOT" "$SETUP_STATE_FILE" "$LEGACY_SETUP_CODE_FILE" \
        "$SETUP_CONFIG_ROOT" "$SETUP_ENV_FILE" "$PROGRAM_ROOT" "$SETUP_BINARY" \
        "$SETUP_UI_ROOT" "$legacy_release/artifacts/admin" "$legacy_release" <<'PY'
import datetime
import hashlib
import json
import os
import re
import stat
import sys

(
    state_root, state_file, code_file, config_root, env_file,
    program_root, setup_binary, ui_root, release_ui, legacy_release,
) = sys.argv[1:]
marker = ".probe-panel-bootstrap-managed"
rfc3339_utc = re.compile(r"^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?Z$")

def fail(message):
    raise SystemExit(message)

def require_dir(path, mode):
    info = os.lstat(path)
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_gid != 0 or stat.S_IMODE(info.st_mode) != mode:
        fail(f"unsafe legacy directory: {path}")

def require_file(path, mode):
    info = os.lstat(path)
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_gid != 0 or stat.S_IMODE(info.st_mode) != mode:
        fail(f"unsafe legacy file: {path}")

def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            fail("legacy JSON contains duplicate fields")
        result[key] = value
    return result

def load_json(path):
    with open(path, "r", encoding="utf-8") as handle:
        contents = handle.read()
    if not contents.endswith("\n") or "\r" in contents:
        fail("legacy JSON is not canonically newline terminated")
    value = json.loads(contents, object_pairs_hook=unique_object)
    if contents != json.dumps(value, separators=(",", ":")) + "\n":
        fail("legacy JSON is not in the canonical format written by v1.0.0")
    return value

def utc_time(value, field):
    if not isinstance(value, str) or not rfc3339_utc.fullmatch(value):
        fail(f"{field} is not a UTC RFC3339 timestamp")
    try:
        parsed = datetime.datetime.fromisoformat(value[:-1] + "+00:00")
    except ValueError:
        fail(f"{field} is not a UTC RFC3339 timestamp")
    if parsed.tzinfo != datetime.timezone.utc:
        fail(f"{field} is not UTC")
    return parsed

require_dir(state_root, 0o700)
require_dir(config_root, 0o750)
require_dir(program_root, 0o755)
require_dir(ui_root, 0o755)
require_dir(release_ui, 0o700)
require_file(state_file, 0o600)
require_file(code_file, 0o600)
require_file(env_file, 0o600)
require_file(setup_binary, 0o755)
require_file(os.path.join(program_root, marker), 0o600)
require_file(os.path.join(ui_root, marker), 0o600)

if set(os.listdir(state_root)) != {"state.json", "setup-code.json"}:
    fail("legacy setup state directory contains unexpected entries")
if set(os.listdir(config_root)) != {"setup.env"}:
    fail("legacy setup configuration directory contains unexpected entries")
if set(os.listdir(program_root)) != {"probe-setup", marker}:
    fail("legacy bootstrap program directory contains unexpected entries")
if os.path.lexists(os.path.join(release_ui, marker)):
    fail("immutable v1.0.0 admin artifact unexpectedly contains a bootstrap marker")

state = load_json(state_file)
if (
    set(state) != {"version", "status", "updated_at"}
    or type(state.get("version")) is not int
    or state.get("version") != 1
):
    fail("legacy setup state schema is invalid")
status = state.get("status")
if status not in {"pending", "configuring"}:
    fail("only pending or configuring v1.0.0 bootstrap state can be migrated")
utc_time(state.get("updated_at"), "state.updated_at")

code = load_json(code_file)
allowed_code_fields = {"code_sha256", "expires_at", "consumed_at"}
if set(code) - allowed_code_fields or not {"code_sha256", "expires_at"} <= set(code):
    fail("legacy setup code record schema is invalid")
digest = code.get("code_sha256")
if not isinstance(digest, str) or len(digest) != 64 or any(character not in "0123456789abcdef" for character in digest):
    fail("legacy setup code digest is invalid")
expires_at = utc_time(code.get("expires_at"), "code.expires_at")
consumed = code.get("consumed_at")
if status == "pending" and (set(code) != {"code_sha256", "expires_at"} or consumed is not None):
    fail("pending legacy state has an unexpected or consumed setup code")
if status == "configuring" and (set(code) != allowed_code_fields or consumed is None):
    fail("configuring legacy state has no canonical consumed setup code")
if consumed is not None:
    consumed_at = utc_time(consumed, "code.consumed_at")
    if consumed_at > expires_at or consumed_at < expires_at - datetime.timedelta(minutes=30):
        fail("legacy setup code consumption time is invalid")

expected_environment = {
    "PROBE_SETUP_LISTEN_ADDR": "127.0.0.1:18080",
    "PROBE_SETUP_STATE_FILE": state_file,
    "PROBE_SETUP_CODE_FILE": code_file,
    "PROBE_SETUP_ADMIN_ROOT": ui_root,
    "PROBE_SETUP_FINALIZE_REQUEST_FILE": "/run/probe-panel-setup/finalize.json",
    "PROBE_SETUP_FINALIZE_RESULT_FILE": "/run/probe-panel-setup/result.json",
    "PROBE_SETUP_BUNDLE_ROOT": legacy_release,
    "PROBE_SETUP_RELEASE_ID": "v1.0.0",
}
environment = {}
environment_order = []
with open(env_file, "r", encoding="utf-8") as handle:
    for raw_line in handle:
        if not raw_line.endswith("\n"):
            fail("legacy setup environment is not newline terminated")
        line = raw_line.rstrip("\n")
        if not line or line.startswith("#") or "=" not in line:
            fail("legacy setup environment is malformed")
        key, value = line.split("=", 1)
        if key in environment:
            fail("legacy setup environment contains duplicate fields")
        environment[key] = value
        environment_order.append(key)
if environment != expected_environment or environment_order != list(expected_environment):
    fail("legacy setup environment does not match the pinned v1.0.0 layout")

def tree(root, directory_mode, ignored=()):
    result = {}
    for current, directories, files in os.walk(root, topdown=True, followlinks=False):
        for name in directories:
            path = os.path.join(current, name)
            info = os.lstat(path)
            if (
                not stat.S_ISDIR(info.st_mode)
                or info.st_uid != 0
                or info.st_gid != 0
                or stat.S_IMODE(info.st_mode) != directory_mode
            ):
                fail(f"unsafe directory in legacy setup UI: {path}")
        for name in files:
            relative = os.path.relpath(os.path.join(current, name), root).replace(os.sep, "/")
            if relative in ignored:
                continue
            path = os.path.join(current, name)
            info = os.lstat(path)
            if (
                not stat.S_ISREG(info.st_mode)
                or info.st_uid != 0
                or info.st_gid != 0
                or stat.S_IMODE(info.st_mode) != 0o644
            ):
                fail(f"unsafe file in legacy setup UI: {path}")
            digest = hashlib.sha256()
            with open(path, "rb") as handle:
                for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                    digest.update(chunk)
            result[relative] = digest.hexdigest()
    return result

if tree(ui_root, 0o755, {marker}) != tree(release_ui, 0o700):
    fail("active setup UI does not exactly match the immutable v1.0.0 release")
print(status)
PY
}

validate_legacy_bootstrap() {
    local architecture="$1" path
    LEGACY_RELEASE="$RELEASES_ROOT/probe-panel-${LEGACY_PANEL_VERSION}-linux-${architecture}"

    assert_root_directory /srv/probe 755
    assert_root_directory "$RELEASES_ROOT" 755
    assert_root_directory /var/lib/probe-panel 700
    assert_directory_contains_only \
        'the legacy releases directory' "$RELEASES_ROOT" "${LEGACY_RELEASE##*/}"
    assert_directory_contains_only \
        'the legacy /srv/probe layout' /srv/probe releases setup-ui
    assert_directory_contains_only \
        'the legacy /var/lib/probe-panel layout' /var/lib/probe-panel setup

    for path in \
        "$SETUP_SOCKET_UNIT" "$SETUP_SOCKET_PATH" \
        "$FINALIZER_REQUEST_FILE" "$FINALIZER_RESULT_FILE" \
        /etc/systemd/system/probe-api.service /etc/systemd/system/probe-api.service.d \
        /etc/systemd/system/probe-postgres-backup.service \
        /etc/systemd/system/probe-postgres-backup.service.d \
        /etc/systemd/system/probe-postgres-backup.timer \
        /etc/systemd/system/probe-postgres-backup.timer.d \
        /etc/systemd/system/probe-panel-setup.service.d \
        /etc/systemd/system/probe-panel-setup.socket.d \
        /etc/systemd/system/probe-panel-finalizer.service.d \
        /etc/systemd/system/probe-panel-finalizer.path.d \
        /etc/nginx/conf.d/probe-panel.conf \
        /etc/probe-panel/tls /etc/probe-panel/admin-allowlist.geo \
        /srv/probe/api /srv/probe/admin /srv/probe/web /srv/probe/agent \
        /srv/probe/migrations /srv/probe/config; do
        [[ ! -e "$path" && ! -L "$path" ]] ||
            die "formal, finalizing, or mixed installation data exists at $path; v1.0.0 bootstrap migration is refused"
    done
    if [[ -e "$FINALIZER_RUNTIME_ROOT" || -L "$FINALIZER_RUNTIME_ROOT" ]]; then
        [[ -d "$FINALIZER_RUNTIME_ROOT" && ! -L "$FINALIZER_RUNTIME_ROOT" &&
           "$(stat -c '%U:%G' "$FINALIZER_RUNTIME_ROOT")" == root:root &&
           "$(stat -c '%a' "$FINALIZER_RUNTIME_ROOT")" == 700 ]] ||
            die 'the legacy setup runtime directory is unsafe'
        [[ -z "$(find "$FINALIZER_RUNTIME_ROOT" -mindepth 1 -print -quit)" ]] ||
            die 'the legacy setup runtime directory contains an unknown or partial finalization artifact'
    fi
    unit_is_installed probe-api.service && die 'a formal Probe API systemd unit exists; bootstrap migration is refused'
    unit_is_installed probe-postgres-backup.service &&
        die 'a formal PostgreSQL backup service exists; bootstrap migration is refused'
    unit_is_installed probe-postgres-backup.timer &&
        die 'a formal PostgreSQL backup timer exists; bootstrap migration is refused'
    unit_is_installed "$SETUP_SOCKET_SERVICE" && die 'a setup socket unit already exists; the installation is not an exact v1.0.0 bootstrap'
    unit_is_installed "$SETUP_SERVICE" || die 'the v1.0.0 setup service is not installed'
    unit_is_installed "$FINALIZER_SERVICE" || die 'the v1.0.0 finalizer service is not installed'
    unit_is_installed "$FINALIZER_PATH_SERVICE" || die 'the v1.0.0 finalizer path is not installed'
    case "$(systemctl is-active "$FINALIZER_SERVICE" 2>/dev/null || :)" in
        active|activating|reloading|failed)
            die 'the setup finalizer is active or failed; migration is refused'
            ;;
    esac
    ! systemctl is-active --quiet nginx.service || die 'Nginx is active; the host is not an unfinished v1.0.0 bootstrap'
    [[ "$(systemctl is-enabled nginx.service 2>/dev/null || :)" == disabled ]] ||
        die 'Nginx is not disabled exactly as required by the unfinished v1.0.0 bootstrap'

    validate_legacy_release_bundle "$LEGACY_RELEASE" "$architecture"
    assert_root_regular_file "$SETUP_BINARY" 755
    assert_root_regular_file "$SETUP_UNIT" 644
    assert_root_regular_file "$FINALIZER_UNIT" 644
    assert_root_regular_file "$FINALIZER_PATH_UNIT" 644
    cmp -s -- "$SETUP_BINARY" "$LEGACY_RELEASE/setup/probe-setup" ||
        die 'the active setup binary does not match the immutable v1.0.0 release'
    cmp -s -- "$SETUP_UNIT" "$LEGACY_RELEASE/source/probe-api/deploy/setup/probe-panel-setup.service" ||
        die 'the active setup service does not match the immutable v1.0.0 release'
    cmp -s -- "$FINALIZER_UNIT" "$LEGACY_RELEASE/source/probe-api/deploy/setup/probe-panel-finalizer.service" ||
        die 'the active finalizer service does not match the immutable v1.0.0 release'
    cmp -s -- "$FINALIZER_PATH_UNIT" "$LEGACY_RELEASE/source/probe-api/deploy/setup/probe-panel-finalizer.path" ||
        die 'the active finalizer path does not match the immutable v1.0.0 release'

    MIGRATION_STATE_STATUS="$(validate_legacy_bootstrap_metadata "$LEGACY_RELEASE")" ||
        die 'the legacy setup state, code, environment, or UI failed strict validation'
    [[ "$MIGRATION_STATE_STATUS" == pending || "$MIGRATION_STATE_STATUS" == configuring ]] ||
        die 'the legacy bootstrap state is not migratable'
}

ensure_secure_directory() {
    local path="$1" mode="$2"
    if [[ -L "$path" || ( -e "$path" && ! -d "$path" ) ]]; then
        die "$path must be a real directory"
    fi
    install -d -o root -g root -m "$mode" "$path"
    [[ "$(stat -c '%U:%G' "$path")" == root:root ]] || die "$path must be owned by root"
}

write_setup_environment() {
    local temporary
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
    [[ ! -e "$path" && ! -L "$path" ]] && return 0
    [[ -d "$path" && ! -L "$path" ]] || {
        warn "refusing to remove unexpected path: $path"
        return 1
    }
    [[ -f "$path/$MANAGED_MARKER" && ! -L "$path/$MANAGED_MARKER" ]] || {
        warn "refusing to remove unmarked directory: $path"
        return 1
    }
    case "$path" in
        /srv/probe/setup-ui|/srv/probe/releases/probe-panel-v1.0.0-linux-amd64|/srv/probe/releases/probe-panel-v1.0.0-linux-arm64|/srv/probe/releases/probe-panel-v1.1.0-linux-amd64|/srv/probe/releases/probe-panel-v1.1.0-linux-arm64|/usr/local/lib/probe-panel)
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

    if [[ "$NGINX_MASKED_BY_INSTALLER" -eq 1 ]]; then
        systemctl unmask nginx.service >/dev/null 2>&1 || :
        NGINX_MASKED_BY_INSTALLER=0
    fi

    if [[ "$INSTALL_COMPLETED" -ne 1 ]]; then
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
    fi

    if [[ -n "$TEMP_ROOT" && "$TEMP_ROOT" == /var/tmp/probe-panel-bootstrap.* &&
          -d "$TEMP_ROOT" && ! -L "$TEMP_ROOT" ]]; then
        rm -rf -- "$TEMP_ROOT"
    fi
    exit "$status"
}

download_verified_current_release() {
    local architecture="$1"
    local asset_name release_url manifest_url archive manifest extraction_root bundle_root
    asset_name="probe-panel-${PANEL_VERSION}-linux-${architecture}.tar.gz"
    release_url="${RELEASE_BASE_URL}/${asset_name}"
    manifest_url="${RELEASE_BASE_URL}/SHA256SUMS"
    archive="$TEMP_ROOT/$asset_name"
    manifest="$TEMP_ROOT/SHA256SUMS"
    extraction_root="$TEMP_ROOT/extracted"
    install -d -m 0700 "$extraction_root"

    log "downloading immutable $PANEL_VERSION release for linux-$architecture"
    download_file "$manifest" "$manifest_url" 60
    [[ -s "$manifest" && "$(stat -c '%s' "$manifest")" -le 1048576 ]] ||
        die 'release SHA256SUMS is empty or exceeds 1 MiB'
    download_file "$archive" "$release_url" 600
    [[ -s "$archive" && "$(stat -c '%s' "$archive")" -le 536870912 ]] ||
        die 'release archive is empty or exceeds 512 MiB'
    verify_release_archive "$manifest" "$archive" "$asset_name"
    safe_extract_archive "$archive" "$extraction_root"

    bundle_root="$extraction_root/probe-panel-${PANEL_VERSION}-linux-${architecture}"
    [[ -d "$bundle_root" && ! -L "$bundle_root" ]] || die 'release archive has an unexpected root directory'
    [[ "$(find "$extraction_root" -mindepth 1 -maxdepth 1 -type d | wc -l)" -eq 1 ]] ||
        die 'release archive must contain exactly one root directory'
    validate_release_bundle "$bundle_root" "$architecture"
    VERIFIED_BUNDLE_ROOT="$bundle_root"
}

atomic_install_root_file() {
    local source="$1" destination="$2" mode="$3" temporary
    temporary="$(mktemp "${destination}.migration.XXXXXX")"
    if ! install -o root -g root -m "$mode" "$source" "$temporary"; then
        rm -f -- "$temporary"
        return 1
    fi
    if ! mv -f -- "$temporary" "$destination"; then
        rm -f -- "$temporary"
        return 1
    fi
}

write_pending_setup_state() {
    python3 - "$SETUP_STATE_FILE" <<'PY'
import datetime
import json
import os
import stat
import sys
import tempfile

path = sys.argv[1]
info = os.lstat(path)
if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_gid != 0 or stat.S_IMODE(info.st_mode) != 0o600:
    raise SystemExit("legacy setup state became unsafe during migration")
directory = os.path.dirname(path)
record = {
    "version": 1,
    "status": "pending",
    "updated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z"),
}
descriptor, temporary = tempfile.mkstemp(prefix=".state.migration.", dir=directory)
try:
    os.fchmod(descriptor, 0o600)
    os.fchown(descriptor, 0, 0)
    handle = os.fdopen(descriptor, "wb", closefd=True)
    descriptor = -1
    with handle:
        handle.write(json.dumps(record, separators=(",", ":")).encode("utf-8") + b"\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temporary, path)
    directory_fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(directory_fd)
    finally:
        os.close(directory_fd)
except BaseException:
    if descriptor >= 0:
        os.close(descriptor)
    try:
        os.unlink(temporary)
    except FileNotFoundError:
        pass
    raise
PY
}

remove_legacy_setup_code() {
    python3 - "$LEGACY_SETUP_CODE_FILE" <<'PY'
import os
import stat
import sys

path = sys.argv[1]
info = os.lstat(path)
if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_gid != 0 or stat.S_IMODE(info.st_mode) != 0o600:
    raise SystemExit("legacy setup code record became unsafe during migration")
directory = os.path.dirname(path)
os.unlink(path)
directory_fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
try:
    os.fsync(directory_fd)
finally:
    os.close(directory_fd)
PY
    [[ ! -e "$LEGACY_SETUP_CODE_FILE" && ! -L "$LEGACY_SETUP_CODE_FILE" ]] ||
        die 'legacy setup code record was not removed'
}

legacy_pending_code_is_usable() {
    python3 - "$SETUP_STATE_FILE" "$LEGACY_SETUP_CODE_FILE" <<'PY' >/dev/null 2>&1
import datetime
import json
import os
import stat
import sys

state_path, code_path = sys.argv[1:]
for path in (state_path, code_path):
    info = os.lstat(path)
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_gid != 0 or stat.S_IMODE(info.st_mode) != 0o600:
        raise SystemExit(1)
with open(state_path, "r", encoding="utf-8") as handle:
    state = json.load(handle)
with open(code_path, "r", encoding="utf-8") as handle:
    code = json.load(handle)
if state.get("status") != "pending" or "consumed_at" in code:
    raise SystemExit(1)
value = code.get("expires_at")
if not isinstance(value, str) or not value.endswith("Z"):
    raise SystemExit(1)
expires_at = datetime.datetime.fromisoformat(value[:-1] + "+00:00")
if datetime.datetime.now(datetime.timezone.utc) >= expires_at:
    raise SystemExit(1)
PY
}

remove_migration_ui_tree() {
    local path="$1"
    [[ ! -e "$path" && ! -L "$path" ]] && return 0
    case "$path" in
        /srv/probe/.setup-ui-v1.0.0-migration.*)
            [[ -d "$path" && ! -L "$path" && -f "$path/$MANAGED_MARKER" && ! -L "$path/$MANAGED_MARKER" ]] || {
                warn "refusing to remove unsafe legacy migration UI tree: $path"
                return 1
            }
            ;;
        /srv/probe/.setup-ui-v1.1.0-stage.*)
            [[ -d "$path" && ! -L "$path" && "$(stat -c '%U:%G' "$path")" == root:root ]] || {
                warn "refusing to remove unsafe staged migration UI tree: $path"
                return 1
            }
            ;;
        *) warn "refusing to remove unexpected migration UI path: $path"; return 1 ;;
    esac
    rm -rf -- "$path"
}

rollback_migration_setup_socket() {
    if [[ "$MIGRATION_SOCKET_UNIT_INSTALLED" -ne 1 &&
          ! -e "$SETUP_SOCKET_UNIT" && ! -L "$SETUP_SOCKET_UNIT" ]]; then
        return 0
    fi
    local failed=0
    systemctl stop "$SETUP_SOCKET_SERVICE" >/dev/null 2>&1 || failed=1
    systemctl disable "$SETUP_SOCKET_SERVICE" >/dev/null 2>&1 || failed=1
    return "$failed"
}

cleanup_migration() {
    local status=$? rollback_ready=1 preserve_temp=0
    trap - EXIT HUP INT TERM
    set +e

    if [[ "$status" -ne 0 && "$MIGRATION_STARTED" -eq 1 && "$MIGRATION_COMPLETED" -ne 1 ]]; then
        warn 'v1.1.0 bootstrap migration failed; restoring the verified v1.0.0 bootstrap files'
        systemctl stop "$FINALIZER_PATH_SERVICE" "$FINALIZER_SERVICE" >/dev/null 2>&1 || rollback_ready=0
        systemctl disable "$FINALIZER_PATH_SERVICE" >/dev/null 2>&1 || rollback_ready=0
        systemctl stop "$SETUP_SERVICE" >/dev/null 2>&1 || rollback_ready=0
        rollback_migration_setup_socket || rollback_ready=0
        systemctl disable "$SETUP_SERVICE" >/dev/null 2>&1 || rollback_ready=0

        if [[ "$MIGRATION_UI_SWAPPED" -eq 1 ||
              ( -n "$MIGRATION_UI_OLD" && -d "$MIGRATION_UI_OLD" && ! -L "$MIGRATION_UI_OLD" ) ]]; then
            remove_managed_tree "$SETUP_UI_ROOT" >/dev/null 2>&1 || rollback_ready=0
            if [[ ! -e "$SETUP_UI_ROOT" && ! -L "$SETUP_UI_ROOT" &&
                  -d "$MIGRATION_UI_OLD" && ! -L "$MIGRATION_UI_OLD" ]]; then
                if mv -T -- "$MIGRATION_UI_OLD" "$SETUP_UI_ROOT"; then
                    MIGRATION_UI_OLD=''
                else
                    rollback_ready=0
                fi
            else
                rollback_ready=0
            fi
        fi

        if [[ -d "$MIGRATION_BACKUP" && ! -L "$MIGRATION_BACKUP" ]]; then
            cp -a -- "$MIGRATION_BACKUP/probe-setup" "$SETUP_BINARY" || rollback_ready=0
            cp -a -- "$MIGRATION_BACKUP/probe-panel-setup.service" "$SETUP_UNIT" || rollback_ready=0
            cp -a -- "$MIGRATION_BACKUP/probe-panel-finalizer.service" "$FINALIZER_UNIT" || rollback_ready=0
            cp -a -- "$MIGRATION_BACKUP/probe-panel-finalizer.path" "$FINALIZER_PATH_UNIT" || rollback_ready=0
            cp -a -- "$MIGRATION_BACKUP/setup.env" "$SETUP_ENV_FILE" || rollback_ready=0
            cp -a -- "$MIGRATION_BACKUP/state.json" "$SETUP_STATE_FILE" || rollback_ready=0
            cp -a -- "$MIGRATION_BACKUP/setup-code.json" "$LEGACY_SETUP_CODE_FILE" || rollback_ready=0
        else
            rollback_ready=0
        fi
        rm -f -- "$SETUP_SOCKET_UNIT" || rollback_ready=0
        systemctl daemon-reload >/dev/null 2>&1 || rollback_ready=0
        if [[ "$rollback_ready" -eq 1 && "$MIGRATION_STATE_STATUS" == pending ]] &&
           legacy_pending_code_is_usable; then
            if ! systemctl enable --now "$SETUP_SERVICE" >/dev/null 2>&1 ||
               ! systemctl enable --now "$FINALIZER_PATH_SERVICE" >/dev/null 2>&1; then
                rollback_ready=0
                systemctl stop "$FINALIZER_PATH_SERVICE" "$SETUP_SERVICE" >/dev/null 2>&1 || :
                systemctl disable "$SETUP_SERVICE" "$FINALIZER_PATH_SERVICE" >/dev/null 2>&1 || :
                warn 'the restored pending v1.0.0 bootstrap could not be restarted; services remain disabled'
            fi
        else
            systemctl disable "$SETUP_SERVICE" "$FINALIZER_PATH_SERVICE" >/dev/null 2>&1 || :
            warn 'the old files could not be proven fully restored, or the configuring session/setup code cannot survive a safe restart; v1.0.0 services remain disabled. Review the first error and re-run migrate-bootstrap instead of starting them'
        fi
        if [[ "$rollback_ready" -ne 1 ]]; then
            preserve_temp=1
            warn "automatic rollback was incomplete; the root-only recovery copy is preserved at $TEMP_ROOT"
        fi
    elif [[ "$status" -ne 0 && "$MIGRATION_QUIESCED" -eq 1 && "$MIGRATION_COMPLETED" -ne 1 ]]; then
        if [[ "$MIGRATION_STATE_STATUS" == pending &&
              ! -e "$FINALIZER_REQUEST_FILE" && ! -L "$FINALIZER_REQUEST_FILE" &&
              -f "$SETUP_STATE_FILE" && ! -L "$SETUP_STATE_FILE" ]] &&
           grep -Eq '"status"[[:space:]]*:[[:space:]]*"pending"' "$SETUP_STATE_FILE" &&
           legacy_pending_code_is_usable; then
            if ! systemctl start "$SETUP_SERVICE" >/dev/null 2>&1 ||
               ! systemctl start "$FINALIZER_PATH_SERVICE" >/dev/null 2>&1; then
                warn 'the unchanged pending v1.0.0 bootstrap could not be resumed; leave it stopped and re-run migrate-bootstrap after reviewing systemd logs'
            fi
        else
            systemctl disable "$SETUP_SERVICE" "$FINALIZER_PATH_SERVICE" >/dev/null 2>&1 || :
            warn 'the quiesced legacy bootstrap could not be resumed safely. Leave it stopped and re-run migrate-bootstrap after reviewing the reported mismatch'
        fi
    fi

    if [[ "$MIGRATION_COMPLETED" -ne 1 && "$MIGRATION_NEW_RELEASE" -eq 1 && "$preserve_temp" -eq 0 ]]; then
        remove_managed_tree "$INSTALLED_RELEASE" >/dev/null 2>&1 || :
    fi
    if [[ -n "$MIGRATION_UI_STAGE" ]]; then
        remove_migration_ui_tree "$MIGRATION_UI_STAGE" >/dev/null 2>&1 || :
    fi
    if [[ -n "$MIGRATION_UI_OLD" && "$MIGRATION_COMPLETED" -eq 1 ]]; then
        remove_migration_ui_tree "$MIGRATION_UI_OLD" >/dev/null 2>&1 || :
    fi
    if [[ "$preserve_temp" -eq 0 && -n "$TEMP_ROOT" && "$TEMP_ROOT" == /var/tmp/probe-panel-bootstrap.* &&
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
    require_debian_13
    require_command uname
    require_command systemctl
    require_command apt-get
    validate_release_settings
    assert_fresh_target
    trap cleanup_install EXIT
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM

    local architecture asset_name release_url manifest_url archive manifest extraction_root bundle_root
    local init_output unit_source socket_unit_source system_directory
    architecture="$(detect_architecture)"
    asset_name="probe-panel-${PANEL_VERSION}-linux-${architecture}.tar.gz"
    release_url="${RELEASE_BASE_URL}/${asset_name}"
    manifest_url="${RELEASE_BASE_URL}/SHA256SUMS"

    install_runtime_dependencies
    prepare_probe_api_account
    prepare_runtime_services
    detect_server_ip
    require_command awk
    require_command cut
    require_command grep
    require_command install
    require_command mktemp
    require_command stat

    TEMP_ROOT="$(mktemp -d /var/tmp/probe-panel-bootstrap.XXXXXX)"
    archive="$TEMP_ROOT/$asset_name"
    manifest="$TEMP_ROOT/SHA256SUMS"
    extraction_root="$TEMP_ROOT/extracted"
    install -d -m 0700 "$extraction_root"

    log "downloading immutable $PANEL_VERSION release for linux-$architecture"
    download_file "$manifest" "$manifest_url" 60
    [[ -s "$manifest" && "$(stat -c '%s' "$manifest")" -le 1048576 ]] ||
        die 'release SHA256SUMS is empty or exceeds 1 MiB'
    download_file "$archive" "$release_url" 600
    [[ -s "$archive" && "$(stat -c '%s' "$archive")" -le 536870912 ]] ||
        die 'release archive is empty or exceeds 512 MiB'
    verify_release_archive "$manifest" "$archive" "$asset_name"
    safe_extract_archive "$archive" "$extraction_root"

    bundle_root="$extraction_root/probe-panel-${PANEL_VERSION}-linux-${architecture}"
    [[ -d "$bundle_root" && ! -L "$bundle_root" ]] || die 'release archive has an unexpected root directory'
    [[ "$(find "$extraction_root" -mindepth 1 -maxdepth 1 -type d | wc -l)" -eq 1 ]] ||
        die 'release archive must contain exactly one root directory'
    validate_release_bundle "$bundle_root" "$architecture"

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
    ensure_secure_directory /etc/letsencrypt 0700
    ensure_secure_directory /var/lib/letsencrypt 0700
    ensure_secure_directory /var/log/letsencrypt 0700
    ensure_secure_directory /var/backups/probe-panel 0700
    ensure_secure_directory /usr/local/lib 0755
    for system_directory in /etc/nginx/conf.d /etc/systemd/system /run/lock; do
        [[ -d "$system_directory" && ! -L "$system_directory" ]] ||
            die "required system directory is missing or unsafe: $system_directory"
    done

    INSTALLED_RELEASE="$RELEASES_ROOT/probe-panel-${PANEL_VERSION}-linux-${architecture}"
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

    unit_source="$INSTALLED_RELEASE/source/probe-api/deploy/setup/probe-panel-setup.service"
    socket_unit_source="$INSTALLED_RELEASE/source/probe-api/deploy/setup/probe-panel-setup.socket"
    install -o root -g root -m 0644 "$unit_source" "$SETUP_UNIT"
    install -o root -g root -m 0644 "$socket_unit_source" "$SETUP_SOCKET_UNIT"
    install -o root -g root -m 0644 \
        "$INSTALLED_RELEASE/source/probe-api/deploy/setup/probe-panel-finalizer.service" \
        "$FINALIZER_UNIT"
    install -o root -g root -m 0644 \
        "$INSTALLED_RELEASE/source/probe-api/deploy/setup/probe-panel-finalizer.path" \
        "$FINALIZER_PATH_UNIT"
    INSTALLED_UNIT=1
    write_setup_environment

    init_output="$(
        PROBE_SETUP_STATE_FILE="$SETUP_STATE_FILE" \
        PROBE_SETUP_ADMIN_ROOT="$SETUP_UI_ROOT" \
        "$SETUP_BINARY" init
    )"
    [[ -z "$init_output" ]] || die 'setup helper unexpectedly printed sensitive initialization output'
    CREATED_STATE=1

    systemctl daemon-reload
    systemd-analyze verify "$SETUP_SOCKET_UNIT" "$SETUP_UNIT" "$FINALIZER_UNIT" "$FINALIZER_PATH_UNIT"
    systemctl enable --now "$SETUP_SOCKET_SERVICE"
    systemctl enable --now "$SETUP_SERVICE"
    systemctl enable --now "$FINALIZER_PATH_SERVICE"
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
    printf '域名全部留空时默认使用 %s 的 18453/18454/18455 HTTPS 端口；全部填写时使用 ACME 域名模式。\n' "$SETUP_SERVER_IP"
}

migrate_bootstrap_action() {
    require_root
    require_debian_13
    require_command uname
    require_command systemctl
    validate_release_settings
    local command_name
    for command_name in \
        awk chmod chown cmp cp curl cut find grep id install ip journalctl mktemp mv \
        python3 rm rmdir sha256sum sleep sort ss stat systemd-analyze wc; do
        require_command "$command_name"
    done

    local architecture unit_source socket_unit_source
    architecture="$(detect_architecture)"
    validate_legacy_bootstrap "$architecture"
    detect_server_ip
    assert_probe_api_service_account

    TEMP_ROOT="$(mktemp -d /var/tmp/probe-panel-bootstrap.XXXXXX)"
    trap cleanup_migration EXIT
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM

    # Download and fully verify the new immutable bundle while the proven old
    # broker is still available. The short quiesced window below then contains
    # only the stable re-check, backup, atomic file switches, and readiness.
    download_verified_current_release "$architecture"
    INSTALLED_RELEASE="$RELEASES_ROOT/probe-panel-${PANEL_VERSION}-linux-${architecture}"
    [[ ! -e "$INSTALLED_RELEASE" && ! -L "$INSTALLED_RELEASE" ]] ||
        die "the v1.1.0 release directory already exists; refusing an ambiguous migration: $INSTALLED_RELEASE"

    log 'quiescing the v1.0.0 setup broker before the final migration proof'
    MIGRATION_QUIESCED=1
    systemctl stop "$FINALIZER_PATH_SERVICE"
    case "$(systemctl is-active "$FINALIZER_SERVICE" 2>/dev/null || :)" in
        active|activating|reloading|failed)
            die 'the v1.0.0 finalizer began running while migration was being quiesced; migration is refused and finalization is left untouched'
            ;;
    esac
    systemctl stop "$SETUP_SERVICE"
    validate_legacy_bootstrap "$architecture"

    MIGRATION_BACKUP="$TEMP_ROOT/legacy-backup"
    install -d -o root -g root -m 0700 "$MIGRATION_BACKUP"
    cp -a -- "$SETUP_BINARY" "$MIGRATION_BACKUP/probe-setup"
    cp -a -- "$SETUP_UNIT" "$MIGRATION_BACKUP/probe-panel-setup.service"
    cp -a -- "$FINALIZER_UNIT" "$MIGRATION_BACKUP/probe-panel-finalizer.service"
    cp -a -- "$FINALIZER_PATH_UNIT" "$MIGRATION_BACKUP/probe-panel-finalizer.path"
    cp -a -- "$SETUP_ENV_FILE" "$MIGRATION_BACKUP/setup.env"
    cp -a -- "$SETUP_STATE_FILE" "$MIGRATION_BACKUP/state.json"
    cp -a -- "$LEGACY_SETUP_CODE_FILE" "$MIGRATION_BACKUP/setup-code.json"

    MIGRATION_UI_STAGE="$(mktemp -d /srv/probe/.setup-ui-v1.1.0-stage.XXXXXX)"
    cp -a -- "$VERIFIED_BUNDLE_ROOT/artifacts/admin/." "$MIGRATION_UI_STAGE/"
    : > "$MIGRATION_UI_STAGE/$MANAGED_MARKER"
    chmod 0600 "$MIGRATION_UI_STAGE/$MANAGED_MARKER"
    chown -R root:root "$MIGRATION_UI_STAGE"
    find "$MIGRATION_UI_STAGE" -type d -exec chmod 0755 {} +
    find "$MIGRATION_UI_STAGE" -type f ! -name "$MANAGED_MARKER" -exec chmod 0644 {} +
    MIGRATION_UI_OLD="$(mktemp -d /srv/probe/.setup-ui-v1.0.0-migration.XXXXXX)"
    rmdir -- "$MIGRATION_UI_OLD"

    MIGRATION_STARTED=1
    systemctl disable "$SETUP_SERVICE" "$FINALIZER_PATH_SERVICE" >/dev/null

    : > "$VERIFIED_BUNDLE_ROOT/$MANAGED_MARKER"
    chmod 0600 "$VERIFIED_BUNDLE_ROOT/$MANAGED_MARKER"
    mv -T -- "$VERIFIED_BUNDLE_ROOT" "$INSTALLED_RELEASE"
    MIGRATION_NEW_RELEASE=1
    chown -R root:root "$INSTALLED_RELEASE"

    atomic_install_root_file "$INSTALLED_RELEASE/setup/probe-setup" "$SETUP_BINARY" 0755
    unit_source="$INSTALLED_RELEASE/source/probe-api/deploy/setup/probe-panel-setup.service"
    socket_unit_source="$INSTALLED_RELEASE/source/probe-api/deploy/setup/probe-panel-setup.socket"
    atomic_install_root_file "$unit_source" "$SETUP_UNIT" 0644
    atomic_install_root_file "$socket_unit_source" "$SETUP_SOCKET_UNIT" 0644
    MIGRATION_SOCKET_UNIT_INSTALLED=1
    atomic_install_root_file \
        "$INSTALLED_RELEASE/source/probe-api/deploy/setup/probe-panel-finalizer.service" \
        "$FINALIZER_UNIT" 0644
    atomic_install_root_file \
        "$INSTALLED_RELEASE/source/probe-api/deploy/setup/probe-panel-finalizer.path" \
        "$FINALIZER_PATH_UNIT" 0644
    write_setup_environment
    write_pending_setup_state

    mv -T -- "$SETUP_UI_ROOT" "$MIGRATION_UI_OLD"
    MIGRATION_UI_SWAPPED=1
    mv -T -- "$MIGRATION_UI_STAGE" "$SETUP_UI_ROOT"
    MIGRATION_UI_STAGE=''

    systemctl daemon-reload
    systemd-analyze verify "$SETUP_SOCKET_UNIT" "$SETUP_UNIT" "$FINALIZER_UNIT" "$FINALIZER_PATH_UNIT"
    systemctl enable --now "$SETUP_SOCKET_SERVICE"
    systemctl enable --now "$SETUP_SERVICE"
    systemctl enable --now "$FINALIZER_PATH_SERVICE"
    if ! wait_for_setup_service; then
        journalctl -u "$SETUP_SERVICE" -n 20 --no-pager >&2 || :
        die 'the migrated root-only setup service did not become ready'
    fi
    systemctl is-active --quiet "$FINALIZER_PATH_SERVICE" || die 'the migrated setup finalizer path watcher is not active'
    assert_setup_listener

    # The obsolete v1.0.0 code record remains recoverable until the new binary,
    # UI, units, environment, state and root-only socket have all passed their
    # readiness checks. Only then is the record durably destroyed.
    remove_legacy_setup_code
    MIGRATION_COMPLETED=1

    printf '\nProbe Panel v1.0.0 待初始化服务已安全迁移到 v1.1.0。\n'
    printf '旧安装码和旧浏览器 Session 已失效，不需要安装码。请在你的电脑执行 root SSH 隧道：\n'
    printf '  ssh -N -o ExitOnForwardFailure=yes -L 127.0.0.1:18080:/run/probe-panel-setup/setup.sock root@%s\n\n' "$SETUP_SERVER_IP"
    printf '然后在本机浏览器打开：\n'
    printf '  http://127.0.0.1:18080/install\n\n'
    printf '域名全部留空时使用 %s 的 18453/18454/18455 HTTPS 端口；全部填写时使用 ACME 域名模式。\n' "$SETUP_SERVER_IP"
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
    if [[ -f "$LEGACY_SETUP_CODE_FILE" && ! -L "$LEGACY_SETUP_CODE_FILE" &&
          ! -e "$SETUP_SOCKET_UNIT" && ! -L "$SETUP_SOCKET_UNIT" ]]; then
        printf '  legacy action: run this pinned v1.1.0 installer with migrate-bootstrap; do not reinstall or delete state files\n'
    fi
    printf 'Probe Panel v1.1 does not display or require an installation code; any legacy setup-code record is handled only by migrate-bootstrap and is never displayed.\n'
    printf 'Database and administrator credentials are never displayed.\n'
}

uninstall_action() {
    require_root
    require_command systemctl

    systemctl stop "$FINALIZER_PATH_SERVICE" "$FINALIZER_SERVICE" >/dev/null 2>&1 || :
    systemctl disable "$FINALIZER_PATH_SERVICE" >/dev/null 2>&1 || :
    systemctl stop "$SETUP_SERVICE" >/dev/null 2>&1 || :
    systemctl disable "$SETUP_SERVICE" >/dev/null 2>&1 || :
    systemctl stop "$SETUP_SOCKET_SERVICE" >/dev/null 2>&1 || :
    systemctl disable "$SETUP_SOCKET_SERVICE" >/dev/null 2>&1 || :
    rm -f -- "$SETUP_UNIT" "$SETUP_SOCKET_UNIT" "$FINALIZER_UNIT" "$FINALIZER_PATH_UNIT"
    systemctl daemon-reload

    remove_managed_tree "$SETUP_UI_ROOT" || :
    remove_managed_tree /srv/probe/releases/probe-panel-v1.0.0-linux-amd64 || :
    remove_managed_tree /srv/probe/releases/probe-panel-v1.0.0-linux-arm64 || :
    remove_managed_tree /srv/probe/releases/probe-panel-v1.1.0-linux-amd64 || :
    remove_managed_tree /srv/probe/releases/probe-panel-v1.1.0-linux-arm64 || :
    remove_managed_tree "$PROGRAM_ROOT" || :

    printf '%s\n' \
        'Probe Panel bootstrap programs were uninstalled.' \
        'Preserved: /etc/probe-panel, /var/lib/probe-panel, /var/backups/probe-panel,' \
        'and all PostgreSQL databases. Purge is intentionally unsupported.'
}

# Keep every external command and installation side effect behind this complete
# parse barrier. A truncated curl | bash stream cannot enter main().
main() {
    local action=install
    if (($# > 0)); then
        action="$1"
        shift
    fi
    (($# == 0)) || die 'only one command is accepted; credentials are never command-line arguments'

    case "$action" in
        install) install_action ;;
        migrate-bootstrap) migrate_bootstrap_action ;;
        status) status_action ;;
        uninstall) uninstall_action ;;
        purge) die 'purge is not implemented; preserve data and perform a separately reviewed, backup-verified removal' ;;
        -h|--help|help) usage ;;
        *) die "unknown command: $action" ;;
    esac
}

main "$@"
