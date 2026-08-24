#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=deploy-common.sh
source "${SCRIPT_DIR}/deploy-common.sh"

usage() {
    cat <<'EOF'
Usage: validate-production.sh <source|host|runtime|all> [--source-root PATH]

Read-only production checks:
  source    Validate the synchronized four-project source layout.
  host      Validate active configuration, release integrity, systemd, and Nginx.
  runtime   Validate service health and listening-address boundaries.
  all       Run source, host, and runtime checks.
EOF
}

MODE="${1:-}"
[[ -n "$MODE" ]] || { usage; exit 2; }
shift

SOURCE_ROOT="${SCRIPT_DIR}/../../.."
while (($# > 0)); do
    case "$1" in
        --source-root)
            (($# >= 2)) || die "--source-root requires a path"
            SOURCE_ROOT="$2"
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

case "$MODE" in
    source|host|runtime|all) ;;
    *) die "mode must be source, host, runtime, or all" ;;
esac

require_debian_13

validate_source() {
    local resolved
    resolved="$(validate_source_root "$SOURCE_ROOT")"
    validate_deployment_script_sources "$resolved"
    validate_nginx_template_contract "${resolved}/probe-api/deploy/nginx/nginx.conf" domain
    validate_nginx_template_contract "${resolved}/probe-api/deploy/nginx/nginx-ip.conf" ip
    log "source layout is valid: $resolved"
}

validate_host() {
    require_root
    require_commands bash sha256sum systemctl systemd-analyze nginx setpriv awk grep sed stat python3
    local resolved_source
    resolved_source="$(validate_source_root "$SOURCE_ROOT")"
    clear_exported_probe_environment
    load_probe_env
    validate_backup_credentials
    validate_switchable_release_paths
    validate_systemd_unit_source "$PROBE_SYSTEMD_UNIT"
    validate_backup_service_assets
    systemd-analyze verify "$PROBE_SYSTEMD_UNIT" "$PROBE_BACKUP_SERVICE_UNIT" "$PROBE_BACKUP_TIMER_UNIT"
    validate_nginx_runtime_config "$(selected_nginx_template "$resolved_source")"

    local api_real agent_real web_real admin_real migrations_real release_dir
    api_real="$(readlink -f -- "${PROBE_API_DIR}/probe-api")"
    agent_real="$(readlink -f -- "${PROBE_ROOT}/agent")"
    web_real="$(readlink -f -- "${PROBE_ROOT}/web")"
    admin_real="$(readlink -f -- "${PROBE_ROOT}/admin")"
    migrations_real="$(readlink -f -- "${PROBE_ROOT}/migrations")"
    release_dir="$(dirname -- "$(dirname -- "$api_real")")"

    [[ "$agent_real" == "$release_dir/agent" ]] || die "Agent downloads are not from the active API release"
    [[ "$web_real" == "$release_dir/web" ]] || die "probe-web is not from the active API release"
    [[ "$admin_real" == "$release_dir/admin" ]] || die "probe-admin is not from the active API release"
    [[ "$migrations_real" == "$release_dir/migrations" ]] || die "migrations are not from the active API release"
    [[ "$web_real" != "$admin_real" ]] || die "visitor and administrator static roots are not independent"
    [[ -f "$release_dir/SHA256SUMS" ]] || die "active release checksum manifest is missing"
    (
        cd "$release_dir"
        sha256sum --check --strict SHA256SUMS
    )
    validate_release_artifacts "$release_dir"
    validate_allowlist_with_binary "$api_real"
    validate_ingress_tls_with_binary "$api_real"
    validate_certbot_timer_state
    log "installed production host assets are valid"
}

validate_runtime() {
    require_root
    require_commands systemctl curl ss python3
    clear_exported_probe_environment
    load_probe_env
    verify_running_services
    systemctl is-active --quiet probe-postgres-backup.timer ||
        die "probe-postgres-backup.timer is not active"
    log "runtime service and listener checks passed"
}

case "$MODE" in
    source)
        validate_source
        ;;
    host)
        validate_host
        ;;
    runtime)
        validate_runtime
        ;;
    all)
        validate_source
        validate_host
        validate_runtime
        ;;
esac
