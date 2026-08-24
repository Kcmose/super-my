#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=deploy-common.sh
source "${SCRIPT_DIR}/deploy-common.sh"

usage() {
    cat <<'EOF'
Usage: install.sh [options]

Prepare or install the Probe Panel production stack on Debian 13.

Options:
  --source-root PATH       Synchronized source root containing all four projects.
                           Defaults to the repository root above this script.
  --prepare-only           Install packages, service account, directories, and
                           configuration examples; do not build or deploy.
  --skip-packages          Do not run apt-get (required packages must exist).
  --disable-default-site   Remove only the stock Debian Nginx default-site symlink.
  --skip-tests             Build without running project tests (not recommended).
  -h, --help               Show this help.

The script never creates database credentials, TLS keys, or an administrator
password. Active configuration and the database are never overwritten.
EOF
}

SOURCE_ROOT="${SCRIPT_DIR}/../../.."
PREPARE_ONLY=false
SKIP_PACKAGES=false
DISABLE_DEFAULT_SITE=false
RUN_TESTS=true

while (($# > 0)); do
    case "$1" in
        --source-root)
            (($# >= 2)) || die "--source-root requires a path"
            SOURCE_ROOT="$2"
            shift 2
            ;;
        --prepare-only)
            PREPARE_ONLY=true
            shift
            ;;
        --skip-packages)
            SKIP_PACKAGES=true
            shift
            ;;
        --disable-default-site)
            DISABLE_DEFAULT_SITE=true
            shift
            ;;
        --skip-tests)
            RUN_TESTS=false
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            die "unknown option: $1"
            ;;
    esac
done

require_root
require_debian_13
SOURCE_ROOT="$(validate_source_root "$SOURCE_ROOT")"

require_commands install flock
install -d -o root -g root -m 0755 /run/lock
exec 9>"$PROBE_DEPLOY_LOCK"
flock -n 9 || die "another Probe deployment is in progress"

if [[ "$SKIP_PACKAGES" != true ]]; then
    log "installing Debian 13 build and runtime dependencies"
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y --no-install-recommends \
        ca-certificates curl nginx postgresql postgresql-client \
        golang-go nodejs npm util-linux iproute2
fi

require_commands getent addgroup adduser
prepare_system_layout "$SOURCE_ROOT"

if [[ "$DISABLE_DEFAULT_SITE" == true ]]; then
    disable_default_nginx_site
fi

if [[ "$PREPARE_ONLY" == true ]]; then
    log "preparation complete"
    log "next: create the PostgreSQL role/database, active environment file, Nginx config, allowlist, and TLS files"
    log "then rerun install.sh without --prepare-only"
    exit 0
fi

[[ -f "$PROBE_ENV_FILE" ]] ||
    die "create $PROBE_ENV_FILE from ${PROBE_CONFIG_DIR}/probe-api.env.example before full installation"
[[ -f "$PROBE_ACTIVE_NGINX_CONFIG" ]] ||
    die "create $PROBE_ACTIVE_NGINX_CONFIG from ${PROBE_NGINX_CONFIG_DIR}/nginx.conf.example before full installation"

deploy_release "$SOURCE_ROOT" "$RUN_TESTS" false

log "production installation completed"
log "create the first administrator interactively as documented; no password was generated or stored by this script"
