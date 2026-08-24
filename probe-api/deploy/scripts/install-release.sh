#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=deploy-common.sh
source "${SCRIPT_DIR}/deploy-common.sh"

usage() {
    cat <<'EOF'
Usage: install-release.sh --bundle-root PATH --release-id ID [options]

Validate, migrate, and atomically activate a prebuilt Probe Panel release.

Options:
  --bundle-root PATH      Extracted, checksum-verified release bundle.
  --release-id ID         Immutable release label, for example v1.0.0.
  --disable-default-site  Remove only Debian's stock Nginx default-site symlink.
  -h, --help              Show this help.

Database credentials, TLS files, the allowlist, and active Nginx/API
configuration must already have been finalized by the local setup service.
EOF
}

BUNDLE_ROOT=""
RELEASE_ID=""
DISABLE_DEFAULT_SITE=false

while (($# > 0)); do
    case "$1" in
        --bundle-root)
            (($# >= 2)) || die "--bundle-root requires a path"
            BUNDLE_ROOT="$2"
            shift 2
            ;;
        --release-id)
            (($# >= 2)) || die "--release-id requires a value"
            RELEASE_ID="$2"
            shift 2
            ;;
        --disable-default-site)
            DISABLE_DEFAULT_SITE=true
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
[[ -n "$BUNDLE_ROOT" ]] || die "--bundle-root is required"
[[ -n "$RELEASE_ID" ]] || die "--release-id is required"

require_commands install flock
install -d -o root -g root -m 0755 /run/lock
exec 9>"$PROBE_DEPLOY_LOCK"
flock -n 9 || die "another Probe deployment is in progress"

if [[ "$DISABLE_DEFAULT_SITE" == true ]]; then
    disable_default_nginx_site
fi

deploy_prebuilt_release "$BUNDLE_ROOT" "$RELEASE_ID"
