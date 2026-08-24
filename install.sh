#!/usr/bin/env bash

# Probe Panel server bootstrap for Debian 13. This script installs verified
# prebuilt application assets plus the runtime packages required by the local
# setup flow. It never receives database or administrator credentials.

set -Eeuo pipefail
umask 077

PROGRAM="${0##*/}"
PANEL_VERSION="${PROBE_PANEL_RELEASE_VERSION:-v1.0.0}"
RELEASE_BASE_URL="${PROBE_PANEL_RELEASE_BASE_URL:-https://github.com/Kcmose/super-my/releases/download/${PANEL_VERSION}}"
SUPER_MY_REF="refs/tags/v1.0.0"
WEB_REF="refs/tags/v1.0.0"
AGENT_REF="refs/tags/v1.0.1"

SETUP_LISTEN_ADDR="127.0.0.1:18080"
SETUP_STATE_ROOT="/var/lib/probe-panel/setup"
SETUP_STATE_FILE="${SETUP_STATE_ROOT}/state.json"
SETUP_CODE_FILE="${SETUP_STATE_ROOT}/setup-code.json"
SETUP_CONFIG_ROOT="/etc/probe-panel"
SETUP_ENV_FILE="${SETUP_CONFIG_ROOT}/setup.env"
SETUP_UI_ROOT="/srv/probe/setup-ui"
RELEASES_ROOT="/srv/probe/releases"
PROGRAM_ROOT="/usr/local/lib/probe-panel"
SETUP_BINARY="${PROGRAM_ROOT}/probe-setup"
SETUP_UNIT="/etc/systemd/system/probe-panel-setup.service"
SETUP_SERVICE="probe-panel-setup.service"
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

usage() {
    cat <<EOF
Usage: ${PROGRAM} [install|status|uninstall]

Commands:
  install      Install the loopback-only first-run setup service (default).
  status       Show bootstrap files and setup-service status without secrets.
  uninstall    Remove bootstrap programs while preserving configuration,
               setup state, PostgreSQL data, and backups.
  purge        Not supported. Data removal must be an explicit, separately
               reviewed operation with a verified final backup.
  -h, --help   Show this help.

The installer accepts no database or administrator credentials. After install,
open /install through the printed SSH tunnel and enter secrets only there.
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
    [[ "$PANEL_VERSION" == v1.0.0 ]] ||
        die 'this installer is pinned to the verified Probe Panel v1.0.0 release'
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
    for path in \
        "$SETUP_BINARY" "$SETUP_UNIT" "$FINALIZER_UNIT" "$FINALIZER_PATH_UNIT" \
        "$SETUP_ENV_FILE" "$SETUP_UI_ROOT" \
        "$SETUP_STATE_FILE" "$SETUP_CODE_FILE" \
        /etc/systemd/system/probe-api.service \
        /etc/nginx/conf.d/probe-panel.conf \
        /srv/probe/api/probe-api /srv/probe/admin /srv/probe/web; do
        if [[ -e "$path" || -L "$path" ]]; then
            die "existing or partial Probe Panel installation found at $path; refusing to overwrite it"
        fi
    done
    if unit_is_installed "$SETUP_SERVICE" || unit_is_installed "$FINALIZER_SERVICE" ||
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
    for dependency in curl python3 sha256sum nginx psql certbot ss runuser setpriv; do
        require_command "$dependency"
    done
}

assert_no_http_listener() {
    if ss -H -lnt | awk '$4 ~ /:(80|443)$/ { found=1 } END { exit !found }'; then
        die 'TCP 80/443 must remain closed until the setup finalizer activates verified Nginx configuration'
    fi
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
    assert_no_http_listener

    log 'starting local PostgreSQL for the setup wizard'
    systemctl enable --now postgresql.service >/dev/null
    systemctl is-active --quiet postgresql.service || die 'PostgreSQL did not start'
    assert_local_postgresql
}

prepare_probe_api_account() {
    require_command getent
    require_command addgroup
    require_command adduser
    getent group probe-api >/dev/null 2>&1 || addgroup --system probe-api
    if ! id probe-api >/dev/null 2>&1; then
        adduser --system --ingroup probe-api --no-create-home --home /nonexistent \
            --shell /usr/sbin/nologin probe-api
    fi
    [[ "$(id -u probe-api)" -ne 0 && "$(id -gn probe-api)" == probe-api ]] ||
        die 'probe-api service account is unsafe'
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
        artifacts/migrations \
        source/probe-api/config/probe-api.env.example \
        source/probe-api/deploy/scripts/deploy-common.sh \
        source/probe-api/deploy/scripts/install-release.sh \
        source/probe-api/deploy/setup/probe-panel-setup.service \
        source/probe-api/deploy/setup/probe-panel-finalizer.service \
        source/probe-api/deploy/setup/probe-panel-finalizer.path \
        source/probe-api/deploy/systemd/probe-api.service; do
        [[ -e "$root/$required" && ! -L "$root/$required" ]] ||
            die "release bundle is incomplete: $required"
    done
    [[ -d "$root/artifacts/migrations" ]] || die 'release migrations path is not a directory'

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
    grep -Fxq 'User=root' "$unit" || die 'setup service must use the root-owned setup state'
    grep -Fxq 'EnvironmentFile=/etc/probe-panel/setup.env' "$unit" || die 'setup service environment path changed'
    grep -Fxq 'ExecStart=/usr/local/lib/probe-panel/probe-setup serve' "$unit" || die 'setup service executable path changed'
    grep -Fxq 'CapabilityBoundingSet=' "$unit" || die 'HTTP setup service must have an empty capability set'
    grep -Fxq 'ReadWritePaths=/run/probe-panel-setup' "$unit" || die 'HTTP setup runtime request path changed'
    [[ "$(grep -Fc 'SocketBindAllow=' "$unit")" -eq 1 ]] || die 'HTTP setup service may allow exactly one bind rule'
    grep -Fxq 'SocketBindAllow=tcp:18080' "$unit" || die 'setup service listener contract changed'
    grep -Fxq 'SocketBindDeny=any' "$unit" || die 'setup service must deny every other bind operation'

    local finalizer_unit="$root/source/probe-api/deploy/setup/probe-panel-finalizer.service"
    local finalizer_path="$root/source/probe-api/deploy/setup/probe-panel-finalizer.path"
    grep -Fxq 'ExecStart=/usr/local/lib/probe-panel/probe-setup finalize' "$finalizer_unit" ||
        die 'setup finalizer executable path changed'
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
        /etc/probe-panel /etc/nginx/conf.d /etc/systemd/system /etc/letsencrypt \
        /var/lib/letsencrypt /var/log/letsencrypt /var/log/nginx /srv/probe \
        /var/backups/probe-panel /run/lock /run/probe-panel-setup; do
        grep -Fxq "ReadWritePaths=$writable_path" "$finalizer_unit" ||
            die "setup finalizer is missing its reviewed writable path: $writable_path"
    done
    [[ "$(grep -Fc 'SocketBindAllow=' "$finalizer_unit")" -eq 1 ]] ||
        die 'setup finalizer may allow exactly one temporary bind rule'
    grep -Fxq 'SocketBindAllow=tcp:80' "$finalizer_unit" ||
        die 'setup finalizer must allow the temporary Certbot HTTP-01 listener'
    grep -Fxq 'SocketBindDeny=any' "$finalizer_unit" || die 'setup finalizer bind policy changed'
    ! grep -Fxq 'SocketBindAllow=tcp:18080' "$finalizer_unit" ||
        die 'non-HTTP setup finalizer must not bind the setup listener'
    grep -Fxq 'PathExists=/run/probe-panel-setup/finalize.json' "$finalizer_path" ||
        die 'setup finalizer request path changed'
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
    {
        printf 'PROBE_SETUP_LISTEN_ADDR=%s\n' "$SETUP_LISTEN_ADDR"
        printf 'PROBE_SETUP_STATE_FILE=%s\n' "$SETUP_STATE_FILE"
        printf 'PROBE_SETUP_CODE_FILE=%s\n' "$SETUP_CODE_FILE"
        printf 'PROBE_SETUP_ADMIN_ROOT=%s\n' "$SETUP_UI_ROOT"
        printf 'PROBE_SETUP_FINALIZE_REQUEST_FILE=%s\n' "$FINALIZER_REQUEST_FILE"
        printf 'PROBE_SETUP_FINALIZE_RESULT_FILE=%s\n' "$FINALIZER_RESULT_FILE"
        printf 'PROBE_SETUP_BUNDLE_ROOT=%s\n' "$INSTALLED_RELEASE"
        printf 'PROBE_SETUP_RELEASE_ID=%s\n' "$PANEL_VERSION"
    } > "$temporary"
    chown root:root "$temporary"
    chmod 0600 "$temporary"
    mv -f -- "$temporary" "$SETUP_ENV_FILE"
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
        /srv/probe/setup-ui|/srv/probe/releases/probe-panel-v1.0.0-linux-amd64|/srv/probe/releases/probe-panel-v1.0.0-linux-arm64|/usr/local/lib/probe-panel)
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
            rm -f -- "$SETUP_UNIT" "$FINALIZER_UNIT" "$FINALIZER_PATH_UNIT"
            systemctl daemon-reload >/dev/null 2>&1 || :
        fi
        remove_managed_tree "$SETUP_UI_ROOT" >/dev/null 2>&1 || :
        if [[ -n "$INSTALLED_RELEASE" ]]; then
            remove_managed_tree "$INSTALLED_RELEASE" >/dev/null 2>&1 || :
        fi
        remove_managed_tree "$PROGRAM_ROOT" >/dev/null 2>&1 || :
        if [[ "$CREATED_STATE" -eq 1 ]]; then
            rm -f -- "$SETUP_STATE_FILE" "$SETUP_CODE_FILE"
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

wait_for_setup_service() {
    local remaining=30
    while (( remaining > 0 )); do
        if systemctl is-active --quiet "$SETUP_SERVICE" &&
           curl -q --fail --silent --show-error --max-time 2 \
               http://127.0.0.1:18080/install >/dev/null; then
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
    local count
    count="$(ss -H -lnt | awk '$4 ~ /:18080$/ { count++; if ($4 != "127.0.0.1:18080") unsafe=1 } END { if (unsafe) exit 2; print count+0 }')" ||
        die 'setup port 18080 is listening beyond IPv4 loopback'
    [[ "$count" == 1 ]] || die 'setup service must have exactly one 127.0.0.1:18080 listener'
    assert_no_http_listener
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
    local one_time_code unit_source system_directory
    architecture="$(detect_architecture)"
    asset_name="probe-panel-${PANEL_VERSION}-linux-${architecture}.tar.gz"
    release_url="${RELEASE_BASE_URL}/${asset_name}"
    manifest_url="${RELEASE_BASE_URL}/SHA256SUMS"

    install_runtime_dependencies
    prepare_runtime_services
    prepare_probe_api_account
    require_command awk
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
    ensure_secure_directory "$SETUP_CONFIG_ROOT" 0750
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
    install -o root -g root -m 0644 "$unit_source" "$SETUP_UNIT"
    install -o root -g root -m 0644 \
        "$INSTALLED_RELEASE/source/probe-api/deploy/setup/probe-panel-finalizer.service" \
        "$FINALIZER_UNIT"
    install -o root -g root -m 0644 \
        "$INSTALLED_RELEASE/source/probe-api/deploy/setup/probe-panel-finalizer.path" \
        "$FINALIZER_PATH_UNIT"
    INSTALLED_UNIT=1
    write_setup_environment

    one_time_code="$(
        PROBE_SETUP_LISTEN_ADDR="$SETUP_LISTEN_ADDR" \
        PROBE_SETUP_STATE_FILE="$SETUP_STATE_FILE" \
        PROBE_SETUP_CODE_FILE="$SETUP_CODE_FILE" \
        PROBE_SETUP_ADMIN_ROOT="$SETUP_UI_ROOT" \
        "$SETUP_BINARY" init
    )"
    [[ "$one_time_code" =~ ^[0-9A-Fa-f]{64}$ ]] ||
        die 'setup helper did not return one 256-bit installation code'
    one_time_code="${one_time_code,,}"
    CREATED_STATE=1

    systemctl daemon-reload
    systemd-analyze verify "$SETUP_UNIT" "$FINALIZER_UNIT" "$FINALIZER_PATH_UNIT"
    systemctl enable --now "$SETUP_SERVICE"
    systemctl enable --now "$FINALIZER_PATH_SERVICE"
    if ! wait_for_setup_service; then
        journalctl -u "$SETUP_SERVICE" -n 20 --no-pager >&2 || :
        die 'loopback setup service did not become ready'
    fi
    systemctl is-active --quiet "$FINALIZER_PATH_SERVICE" || die 'setup finalizer path watcher is not active'
    assert_setup_listener

    INSTALL_COMPLETED=1
    printf '\nProbe Panel 初始化服务已启动。\n'
    printf '安装码（256-bit，仅本次显示，30 分钟有效）：\n%s\n\n' "$one_time_code"
    printf '请在你的电脑执行 SSH 隧道（把 <服务器IP> 替换为实际地址）：\n'
    printf '  ssh -L 18080:127.0.0.1:18080 root@<服务器IP>\n\n'
    printf '然后在本机浏览器打开：\n'
    printf '  http://127.0.0.1:18080/install\n\n'
    printf 'setup 只监听服务器回环地址；Nginx 保持停止，PostgreSQL 仅本机监听。\n'
    printf '向导最终提交后，无 HTTP 的一次性 Finalizer 才会临时使用 80 端口申请证书并激活正式服务。\n'
    one_time_code=''
}

status_action() {
    local service_status='not-installed'
    if command -v systemctl >/dev/null 2>&1 && unit_is_installed "$SETUP_SERVICE"; then
        service_status="$(systemctl is-active "$SETUP_SERVICE" 2>/dev/null || :)"
        [[ -n "$service_status" ]] || service_status=inactive
    fi
    printf 'Probe Panel bootstrap status\n'
    printf '  setup service: %s\n' "$service_status"
    if command -v systemctl >/dev/null 2>&1; then
        printf '  finalizer path: %s\n' "$(systemctl is-active "$FINALIZER_PATH_SERVICE" 2>/dev/null || :)"
    fi
    printf '  setup binary:  %s\n' "$([[ -x "$SETUP_BINARY" ]] && printf present || printf absent)"
    printf '  setup state:   %s\n' "$([[ -f "$SETUP_STATE_FILE" && ! -L "$SETUP_STATE_FILE" ]] && printf present || printf absent)"
    printf '  configuration: %s\n' "$([[ -f "$SETUP_ENV_FILE" && ! -L "$SETUP_ENV_FILE" ]] && printf present || printf absent)"
    printf 'No installation code, hash, database credential, or administrator credential is displayed.\n'
}

uninstall_action() {
    require_root
    require_command systemctl

    systemctl stop "$FINALIZER_PATH_SERVICE" "$FINALIZER_SERVICE" >/dev/null 2>&1 || :
    systemctl disable "$FINALIZER_PATH_SERVICE" >/dev/null 2>&1 || :
    systemctl stop "$SETUP_SERVICE" >/dev/null 2>&1 || :
    systemctl disable "$SETUP_SERVICE" >/dev/null 2>&1 || :
    rm -f -- "$SETUP_UNIT" "$FINALIZER_UNIT" "$FINALIZER_PATH_UNIT"
    systemctl daemon-reload

    remove_managed_tree "$SETUP_UI_ROOT" || :
    remove_managed_tree /srv/probe/releases/probe-panel-v1.0.0-linux-amd64 || :
    remove_managed_tree /srv/probe/releases/probe-panel-v1.0.0-linux-arm64 || :
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
        status) status_action ;;
        uninstall) uninstall_action ;;
        purge) die 'purge is not implemented; preserve data and perform a separately reviewed, backup-verified removal' ;;
        -h|--help|help) usage ;;
        *) die "unknown command: $action" ;;
    esac
}

main "$@"
