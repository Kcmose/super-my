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
       install-release.sh --bundle-root PATH --check-platform PLATFORM_ID

Validate, migrate, and atomically activate a prebuilt Probe Panel release.

Options:
  --bundle-root PATH      Bootstrap-staged, checksum-verified release bundle.
  --release-id ID         Immutable release label, for example v1.0.0.
  --profile PROFILE       Expected profile; v1.2 accepts management only.
  --check-platform ID     Read-only host/runtime and complete bundle preflight.
  -h, --help              Show this help.

Database credentials, TLS files, the allowlist, and active Nginx/API
configuration must already have been finalized by the local setup service.
This command validates and preserves the explicit domain/IP ingress mode; it
does not issue certificates, rewrite the active fragment, or switch modes.
EOF
}

BUNDLE_ROOT=""
RELEASE_ID=""
RELEASE_PROFILE="management"
CHECK_PLATFORM_ID=""

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
        --profile)
            (($# >= 2)) || die "--profile requires a value"
            RELEASE_PROFILE="$2"
            shift 2
            ;;
        --check-platform)
            (($# >= 2)) || die "--check-platform requires a platform ID"
            CHECK_PLATFORM_ID="$2"
            shift 2
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
require_supported_runtime_platform
if [[ -n "$CHECK_PLATFORM_ID" ]]; then
    [[ -n "$BUNDLE_ROOT" && -z "$RELEASE_ID" && "$RELEASE_PROFILE" == management ]] ||
        die "--check-platform requires --bundle-root and cannot activate a release"
    validate_management_platform_id "$CHECK_PLATFORM_ID"
    [[ "$RUNTIME_PLATFORM_ID" == "$CHECK_PLATFORM_ID" ]] ||
        die "setup platform $CHECK_PLATFORM_ID does not match this host $RUNTIME_PLATFORM_ID"
    validate_prebuilt_bundle "$BUNDLE_ROOT" management
    log "host and bundle match management runtime platform $RUNTIME_PLATFORM_ID"
    exit 0
fi
[[ -n "$BUNDLE_ROOT" ]] || die "--bundle-root is required"
[[ -n "$RELEASE_ID" ]] || die "--release-id is required"
[[ "$RELEASE_PROFILE" == management ]] ||
    die "this v1.2 release installer accepts management only"

require_commands flock stat
acquire_deployment_lock

deploy_prebuilt_release "$BUNDLE_ROOT" "$RELEASE_ID" "$RELEASE_PROFILE"
