#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=deploy-common.sh
source "${SCRIPT_DIR}/deploy-common.sh"

usage() {
    cat <<'EOF'
Usage: upgrade.sh [options]

Build, validate, back up, migrate, and atomically activate a synchronized Probe
Panel source tree on an existing Debian 13 production host.

Options:
  --source-root PATH   Synchronized source root containing all four projects.
                       Defaults to the repository root above this script.
  --validate-only      Run source, build, configuration, Nginx, and unit checks;
                       do not back up, migrate, switch releases, or restart.
  --skip-tests         Build without running project tests (not recommended).
  -h, --help           Show this help.

Active configuration, TLS material, the allowlist, and PostgreSQL data are not
replaced. A verified custom-format database backup is created before migration.
EOF
}

SOURCE_ROOT="${SCRIPT_DIR}/../../.."
VALIDATE_ONLY=false
RUN_TESTS=true

while (($# > 0)); do
    case "$1" in
        --source-root)
            (($# >= 2)) || die "--source-root requires a path"
            SOURCE_ROOT="$2"
            shift 2
            ;;
        --validate-only)
            VALIDATE_ONLY=true
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
require_commands flock
SOURCE_ROOT="$(validate_source_root "$SOURCE_ROOT")"

[[ -d "$PROBE_ROOT" && -d "$PROBE_CONFIG_DIR" && -d "$PROBE_RELEASES_DIR" ]] ||
    die "production layout is missing; run install.sh --prepare-only first"
id probe-api >/dev/null 2>&1 || die "probe-api service account is missing"

exec 9>"$PROBE_DEPLOY_LOCK"
flock -n 9 || die "another Probe deployment is in progress"

deploy_release "$SOURCE_ROOT" "$RUN_TESTS" "$VALIDATE_ONLY"
