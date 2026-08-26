# shellcheck shell=bash
# shellcheck disable=SC2154

# Management-only runtime overrides appended to the reviewed common helpers by
# the release bundle builder.  Keep this file limited to prebuilt deployment;
# source compilation and unrelated release profiles do not belong here.

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
            /var/tmp/probe-prebuilt-verify.*)
                rm -rf -- "$PROBE_DEPLOY_WORK_ROOT" ||
                    warn "could not clean deployment work root: $PROBE_DEPLOY_WORK_ROOT"
                ;;
            *) warn "refusing to remove unexpected temporary path: $PROBE_DEPLOY_WORK_ROOT" ;;
        esac
    fi
    exit "$status"
}

rollback_pending_management_activation() {
    local rollback_state="${MANAGEMENT_ACTIVATION_ROLLBACK_STATE:-none}"
    local snapshot="${MANAGEMENT_ROLLBACK_SERVICE_ASSET_SNAPSHOT:-}"
    [[ "$rollback_state" != none ]] || return 0

    MANAGEMENT_ACTIVATION_ROLLBACK_STATE="none"
    MANAGEMENT_ROLLBACK_SERVICE_ASSET_SNAPSHOT=""
    warn "management release activation did not commit; restoring the prior host state"

    if [[ "$rollback_state" == links || "$rollback_state" == runtime ]]; then
        run_management_rollback_step \
            'could not completely restore the prior application links' \
            restore_release_links \
            "$MANAGEMENT_ROLLBACK_OLD_API" \
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
    [[ -n "${seen[PROBE_INSTALLATION_PROFILE]+x}" ]] ||
        die "PROBE_INSTALLATION_PROFILE must be set explicitly in $PROBE_ENV_FILE"
    validate_release_profile "${PROBE_INSTALLATION_PROFILE:-}"
    [[ -n "${seen[PROBE_PLATFORM_ID]+x}" ]] ||
        die "PROBE_PLATFORM_ID must be set explicitly for a management installation"
    validate_management_platform_id "${PROBE_PLATFORM_ID:-}"
    local runtime_platform
    runtime_platform="$(runtime_platform_id)"
    if [[ "$PROBE_PLATFORM_ID" != "$runtime_platform" ]]; then
        die "installed platform $PROBE_PLATFORM_ID does not match this host $runtime_platform"
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
            if [[ -n "${PROBE_AGENT_PUBLIC_URL:-}" ]]; then
                [[ "$PROBE_AGENT_INSTALL_CA_FILE" == "$PROBE_PRIVATE_CA_FILE" ]] ||
                    die "management IP Agent integration must use $PROBE_PRIVATE_CA_FILE"
            fi
            assert_public_root_file "$PROBE_PRIVATE_CA_FILE"
            ;;
    esac
}

selected_nginx_template() {
    local source_root="$1" profile="${2:-management}"
    validate_release_profile "$profile"
    local dialect
    dialect="$(management_platform_nginx_dialect "${PROBE_PLATFORM_ID:-}")"
    case "${PROBE_INGRESS_MODE:-}:${dialect}" in
        domain:modern) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx-management.conf" ;;
        ip:modern) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx-management-ip.conf" ;;
        domain:legacy) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx-management-legacy.conf" ;;
        ip:legacy) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx-management-ip-legacy.conf" ;;
        domain:classic) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx-management-classic.conf" ;;
        ip:classic) printf '%s\n' "$source_root/probe-api/deploy/nginx/nginx-management-ip-classic.conf" ;;
        *) die "load the explicit ingress mode before selecting the management Nginx template" ;;
    esac
}

validate_management_nginx_template_contract() {
    local template_file="$1" ingress_mode="$2" dialect="${3:-modern}"
    [[ -f "$template_file" && ! -L "$template_file" ]] ||
        die "management Nginx source template is missing: $template_file"
    [[ "$ingress_mode" == domain || "$ingress_mode" == ip ]] ||
        die "unknown management Nginx template mode: $ingress_mode"
    [[ "$dialect" == modern || "$dialect" == legacy || "$dialect" == classic ]] ||
        die "unknown management Nginx HTTP/2 dialect: $dialect"

    local expected_locations actual_locations static_directives
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

    static_directives="$(awk '$1 == "root" || $1 == "alias" { print $1 " " $2 }' "$template_file")"
    [[ "$(grep -Ec '^[[:space:]]*(root|alias)[[:space:]]+' "$template_file")" -eq 1 &&
       "$static_directives" == 'root /srv/probe/admin;' ]] ||
        die "management Nginx template must have one static directive rooted at /srv/probe/admin"
    [[ "$(grep -Ec '^[[:space:]]*proxy_pass[[:space:]]+http://probe_api;$' "$template_file")" -eq 6 ]] ||
        die "management Nginx template has an unexpected upstream route count"
    # Literal Nginx variable syntax is part of the reviewed template contract.
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
        local listener
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

validate_nginx_fragment_structure() {
    local active_file="$1" template_file="$2" profile="${3:-management}"
    validate_release_profile "$profile"
    validate_management_nginx_fragment_structure "$active_file" "$template_file"
}

validate_active_nginx_config() {
    local template_file="$1" profile="${2:-management}"
    validate_release_profile "$profile"
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
    local dump_file="$1" profile="${2:-management}"
    validate_release_profile "$profile"
    awk -v mode="$PROBE_INGRESS_MODE" '
        /^[ \t]*listen[ \t]+/ {
            value=$2
            sub(/;$/, "", value)
            if (value ~ /^unix:/) next
            port=value
            sub(/^.*:/, "", port)
            seen[port]++
        }
        END {
            if (mode == "domain" && (seen["80"] < 1 || seen["443"] < 1)) bad=1
            else if (mode == "ip" && seen["18455"] != 2) bad=1
            else if (mode != "domain" && mode != "ip") bad=1
            if (bad) exit 1
        }
    ' "$dump_file" || die "Nginx listeners do not satisfy the management ingress contract"
}

validate_no_duplicate_nginx_hosts() {
    local dump_file="$1" profile="${2:-management}" admin_domain count
    validate_release_profile "$profile"
    [[ "$PROBE_INGRESS_MODE" == domain ]] || return 0
    admin_domain="$(awk '$1 == "server_name" { value=$2; sub(/;$/, "", value); print value; exit }' "$PROBE_ACTIVE_NGINX_CONFIG")"
    [[ -n "$admin_domain" ]] || die "could not extract the management administrator hostname"
    count="$(awk -v expected="$admin_domain" '$1 == "server_name" {
        for (i=2; i<=NF; i++) {
            value=$i
            sub(/;$/, "", value)
            if (value == expected) count++
        }
    } END { print count + 0 }' "$dump_file")"
    [[ "$count" -eq 2 ]] ||
        die "Nginx runtime must declare management hostname $admin_domain exactly twice"
}

validate_nginx_runtime_config() {
    local template_file="$1" profile="${2:-management}"
    validate_release_profile "$profile"
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

validate_ingress_tls_with_binary() {
    local api_binary="$1" profile="${2:-management}"
    validate_release_profile "$profile"
    [[ -x "$api_binary" ]] || die "invalid API binary for ingress TLS validation: $api_binary"

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
}

validate_certbot_timer_state() {
    local profile="${1:-management}"
    validate_release_profile "$profile"
    if [[ "$PROBE_INGRESS_MODE" == ip ]]; then
        return 0
    fi

    local enabled_state active_state timer_unit
    timer_unit="$(runtime_certbot_timer)"
    enabled_state="$(systemctl is-enabled "$timer_unit" 2>/dev/null || true)"
    active_state="$(systemctl is-active "$timer_unit" 2>/dev/null || true)"
    [[ "$PROBE_INGRESS_MODE" == domain ]] ||
        die "load the explicit ingress mode before validating the Certbot timer"
    [[ "$enabled_state" == enabled ]] ||
        die "domain mode requires $timer_unit to be enabled"
    [[ "$active_state" == active ]] ||
        die "domain mode requires $timer_unit to be active"
}

validate_release_profile() {
    [[ "${1:-}" == management ]] ||
        die "the packaged runtime accepts the management release profile only"
}

release_bundle_profile() {
    local bundle_root="$1" manifest="$1/RELEASE-MANIFEST" count profile
    [[ -f "$manifest" && ! -L "$manifest" ]] ||
        die "the management release manifest is missing or unsafe"
    count="$(awk '/^profile=/ { count++ } END { print count + 0 }' "$manifest")"
    [[ "$count" == 1 ]] || die "release manifest must contain exactly one profile"
    profile="$(sed -n 's/^profile=//p' "$manifest")"
    validate_release_profile "$profile"
    printf '%s\n' "$profile"
}

validate_release_artifacts() {
    local artifact_root="$1" profile="${2:-management}" layout="${3:-bundle}"
    validate_release_profile "$profile"
    [[ -d "$artifact_root" && ! -L "$artifact_root" ]] ||
        die "management artifact root is missing or unsafe"

    local actual_entries expected_entries
    actual_entries="$(
        cd "$artifact_root" || die "could not enter management artifact root: $artifact_root"
        find . -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort
    )"
    case "$layout" in
        bundle)
            expected_entries="$(printf '%s\n' admin api migrations | LC_ALL=C sort)"
            ;;
        staged)
            expected_entries="$(printf '%s\n' SHA256SUMS admin api migrations | LC_ALL=C sort)"
            ;;
        *) die "unknown management artifact layout: $layout" ;;
    esac
    [[ "$actual_entries" == "$expected_entries" ]] ||
        die "management artifact root contains an unexpected top-level entry"

    [[ -x "$artifact_root/api/probe-api" && ! -L "$artifact_root/api/probe-api" ]] ||
        die "staged probe-api binary is missing or unsafe"
    validate_static_artifact probe-admin "$artifact_root/admin"
    [[ -d "$artifact_root/migrations" && ! -L "$artifact_root/migrations" ]] ||
        die "staged migrations are missing or unsafe"
    "$artifact_root/api/probe-api" version >/dev/null
}

validate_prebuilt_bundle() {
    local bundle_root="$1" profile="${2:-management}"
    validate_release_profile "$profile"
    [[ -d "$bundle_root" && ! -L "$bundle_root" ]] ||
        die "prebuilt bundle root is missing or unsafe: $bundle_root"
    [[ "$(release_bundle_profile "$bundle_root")" == "$profile" ]] ||
        die "prebuilt bundle profile changed during validation"
    validate_management_release_platform "$bundle_root"

    local required
    for required in \
        BUNDLE-SHA256SUMS \
        RELEASE-MANIFEST \
        artifacts/api/probe-api \
        artifacts/admin/index.html \
        setup/probe-setup \
        source/probe-api/config/probe-api.env.example \
        source/probe-api/config/probe-postgres-backup.env.example \
        source/probe-api/deploy/nginx/nginx-management.conf \
        source/probe-api/deploy/nginx/nginx-management-ip.conf \
        source/probe-api/deploy/nginx/nginx-management-legacy.conf \
        source/probe-api/deploy/nginx/nginx-management-ip-legacy.conf \
        source/probe-api/deploy/nginx/nginx-management-classic.conf \
        source/probe-api/deploy/nginx/nginx-management-ip-classic.conf \
        source/probe-api/deploy/scripts/deploy-common.sh \
        source/probe-api/deploy/scripts/install-release.sh \
        source/probe-api/deploy/scripts/restore-management.sh \
        source/probe-api/deploy/scripts/validate-management.sh \
        source/probe-api/deploy/scripts/uninstall-management.sh \
        source/probe-api/deploy/scripts/backup-postgres.sh \
        source/probe-api/deploy/scripts/restore-postgres.sh \
        source/probe-api/deploy/setup/probe-panel-setup.env.example \
        source/probe-api/deploy/setup/probe-panel-setup.service \
        source/probe-api/deploy/setup/probe-panel-setup.socket \
        source/probe-api/deploy/setup/probe-panel-setup-legacy.service \
        source/probe-api/deploy/setup/probe-panel-setup-legacy.socket \
        source/probe-api/deploy/setup/probe-panel-finalizer-management.service \
        source/probe-api/deploy/setup/probe-panel-finalizer-management-legacy.service \
        source/probe-api/deploy/setup/probe-panel-finalizer.path \
        source/probe-api/deploy/systemd/probe-api.service \
        source/probe-api/deploy/systemd/probe-api-legacy.service \
        source/probe-api/deploy/systemd/probe-postgres-backup.service \
        source/probe-api/deploy/systemd/probe-postgres-backup.timer \
        source/probe-api/deploy/systemd/probe-postgres-backup-legacy.service \
        source/probe-api/deploy/systemd/probe-postgres-backup-legacy.timer; do
        [[ -f "$bundle_root/$required" && ! -L "$bundle_root/$required" ]] ||
            die "prebuilt management bundle is missing a required regular file: $required"
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

    local actual_root_entries expected_root_entries
    actual_root_entries="$(
        cd "$bundle_root" || die "could not enter management bundle root: $bundle_root"
        find . -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort
    )"
    expected_root_entries="$(printf '%s\n' BUNDLE-SHA256SUMS RELEASE-MANIFEST artifacts setup source | LC_ALL=C sort)"
    [[ "$actual_root_entries" == "$expected_root_entries" ]] ||
        die "prebuilt management bundle contains an unexpected top-level entry"

    local actual_setup_entries expected_setup_entries
    actual_setup_entries="$(
        cd "$bundle_root/setup" || die "could not enter management setup root: $bundle_root/setup"
        find . -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort
    )"
    expected_setup_entries="probe-setup"
    [[ "$actual_setup_entries" == "$expected_setup_entries" ]] ||
        die "prebuilt management setup directory contains an unexpected entry"

    local actual_source_entries expected_source_entries
    actual_source_entries="$(
        cd "$bundle_root/source" || die "could not enter management source root: $bundle_root/source"
        find . -mindepth 1 -printf '%y %P\n' | LC_ALL=C sort
    )"
    expected_source_entries="$(cat <<'EOF'
d probe-api
d probe-api/config
d probe-api/deploy
d probe-api/deploy/nginx
d probe-api/deploy/scripts
d probe-api/deploy/setup
d probe-api/deploy/systemd
f probe-api/config/probe-api.env.example
f probe-api/config/probe-postgres-backup.env.example
f probe-api/deploy/nginx/nginx-management-classic.conf
f probe-api/deploy/nginx/nginx-management-ip-classic.conf
f probe-api/deploy/nginx/nginx-management-ip-legacy.conf
f probe-api/deploy/nginx/nginx-management-ip.conf
f probe-api/deploy/nginx/nginx-management-legacy.conf
f probe-api/deploy/nginx/nginx-management.conf
f probe-api/deploy/scripts/backup-postgres.sh
f probe-api/deploy/scripts/deploy-common.sh
f probe-api/deploy/scripts/install-release.sh
f probe-api/deploy/scripts/restore-management.sh
f probe-api/deploy/scripts/restore-postgres.sh
f probe-api/deploy/scripts/uninstall-management.sh
f probe-api/deploy/scripts/validate-management.sh
f probe-api/deploy/setup/probe-panel-finalizer-management-legacy.service
f probe-api/deploy/setup/probe-panel-finalizer-management.service
f probe-api/deploy/setup/probe-panel-finalizer.path
f probe-api/deploy/setup/probe-panel-setup-legacy.service
f probe-api/deploy/setup/probe-panel-setup-legacy.socket
f probe-api/deploy/setup/probe-panel-setup.env.example
f probe-api/deploy/setup/probe-panel-setup.service
f probe-api/deploy/setup/probe-panel-setup.socket
f probe-api/deploy/systemd/probe-api-legacy.service
f probe-api/deploy/systemd/probe-api.service
f probe-api/deploy/systemd/probe-postgres-backup-legacy.service
f probe-api/deploy/systemd/probe-postgres-backup-legacy.timer
f probe-api/deploy/systemd/probe-postgres-backup.service
f probe-api/deploy/systemd/probe-postgres-backup.timer
EOF
)"
    [[ "$actual_source_entries" == "$expected_source_entries" ]] ||
        die "prebuilt management source directory differs from its exact allowlist"

    local helper_file forbidden_compile_function forbidden_deploy_function forbidden_marker
    local forbidden_npm_command forbidden_agent_command
    helper_file="$bundle_root/source/probe-api/deploy/scripts/deploy-common.sh"
    forbidden_compile_function="build_release""_artifacts"
    forbidden_deploy_function="deploy_""release()"
    forbidden_marker="MANAGEMENT_BUNDLE_""EXCLUDE"
    forbidden_npm_command="npm[[:space:]]+run[[:space:]]+""build"
    forbidden_agent_command="[.]/cmd/probe-""agent"
    if grep -Fq "$forbidden_compile_function" "$helper_file" ||
       grep -Fq "$forbidden_deploy_function" "$helper_file" ||
       grep -Fq "$forbidden_marker" "$helper_file" ||
       grep -Eq "$forbidden_npm_command|$forbidden_agent_command" "$helper_file"; then
        die "prebuilt management deploy helper contains forbidden source compilation logic"
    fi

    if find "$bundle_root" -type l -print -quit | grep -q .; then
        die "prebuilt bundle contains a symbolic link"
    fi
    (
        cd "$bundle_root" || die "could not enter management bundle root: $bundle_root"
        sha256sum --check --strict BUNDLE-SHA256SUMS
    ) || die "prebuilt bundle checksum verification failed"

    local expected_paths manifest_paths
    expected_paths="$(
        cd "$bundle_root" || die "could not enter management bundle root: $bundle_root"
        find artifacts setup source -type f -print | LC_ALL=C sort
    )"
    manifest_paths="$(awk '{ print $2 }' "$bundle_root/BUNDLE-SHA256SUMS" | LC_ALL=C sort)"
    [[ -n "$expected_paths" && "$manifest_paths" == "$expected_paths" ]] ||
        die "BUNDLE-SHA256SUMS must cover every packaged file exactly once"

    validate_release_artifacts "$bundle_root/artifacts" "$profile" bundle
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
    validate_systemd_unit_source "$bundle_root/source/probe-api/deploy/systemd/probe-api.service"
    validate_backup_unit_source \
        "$bundle_root/source/probe-api/deploy/systemd/probe-postgres-backup.service" \
        "$bundle_root/source/probe-api/deploy/systemd/probe-postgres-backup.timer"
    validate_systemd_unit_source "$bundle_root/source/probe-api/deploy/systemd/probe-api-legacy.service"
    validate_backup_unit_source \
        "$bundle_root/source/probe-api/deploy/systemd/probe-postgres-backup-legacy.service" \
        "$bundle_root/source/probe-api/deploy/systemd/probe-postgres-backup-legacy.timer"
}

prepare_system_layout() {
    local source_root="$1" profile="${2:-management}"
    validate_release_profile "$profile"
    prepare_probe_api_service_account

    install -d -o root -g root -m 0755 "$PROBE_ROOT" "$PROBE_API_DIR" "$PROBE_RELEASES_DIR"
    install -d -o root -g probe-api -m 0750 "$PROBE_BACKUP_SCRIPT_DIR"
    install -d -o root -g probe-api -m 0750 "$PROBE_CONFIG_DIR"
    install -d -o root -g root -m 0755 "$PROBE_NGINX_CONFIG_DIR"
    install -d -o root -g root -m 0700 "$PROBE_BACKUPS_DIR"
    install -d -o probe-api -g probe-api -m 0700 "$PROBE_POSTGRES_BACKUP_DIR"
    [[ ! -L /etc/probe-panel && ( ! -e /etc/probe-panel || -d /etc/probe-panel ) ]] ||
        die "/etc/probe-panel must be a real directory"
    install -d -o root -g root -m 0755 /etc/probe-panel
    [[ ! -L /etc/probe-panel && "$(stat -c '%u:%g:%a' /etc/probe-panel)" == 0:0:755 ]] ||
        die "/etc/probe-panel must be a root:root directory with mode 0755"
    install -d -o root -g root -m 0755 \
        /etc/probe-panel/tls /etc/probe-panel/tls/admin

    if [[ ! -e "$PROBE_ALLOWLIST_FILE" ]]; then
        install -o root -g probe-api -m 0640 /dev/null "$PROBE_ALLOWLIST_FILE"
    fi

    install_example_file "$source_root/probe-api/config/probe-api.env.example" \
        "$PROBE_CONFIG_DIR/probe-api.env.example" 0640 probe-api
    install_example_file "$source_root/probe-api/config/probe-postgres-backup.env.example" \
        "$PROBE_CONFIG_DIR/probe-postgres-backup.env.example" 0600 root
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
    install_example_file "$source_root/probe-api/deploy/nginx/$domain_template" \
        "$PROBE_NGINX_CONFIG_DIR/nginx-management.conf.example" 0644 root
    install_example_file "$source_root/probe-api/deploy/nginx/$ip_template" \
        "$PROBE_NGINX_CONFIG_DIR/nginx-management-ip.conf.example" 0644 root
}

stage_release() {
    local artifact_root="$1" release_id="$2" profile="${3:-management}"
    local incoming="$PROBE_RELEASES_DIR/.incoming-$release_id"
    local final="$PROBE_RELEASES_DIR/$release_id"
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
    find "${release_paths[@]}" -type d -exec chmod 0755 {} +
    find "${release_paths[@]}" -type f -exec chmod 0644 {} +
    chown -R root:root "$incoming"

    (
        cd "$incoming" || die "could not enter staged management release: $incoming"
        find . -mindepth 2 -type f -print0 | sed -z 's#^[.]/##' | sort -z | xargs -0 sha256sum > SHA256SUMS
    )
    chmod 0644 "$incoming/SHA256SUMS"
    mv -T -- "$incoming" "$final"
    STAGED_RELEASE_INCOMING_PENDING=""
    STAGED_RELEASE_DIR="$final"
}

activate_release() {
    local release_dir="$1" profile="${2:-management}"
    validate_release_profile "$profile"
    assert_switchable_path "$PROBE_API_DIR/probe-api"
    assert_switchable_path "$PROBE_ROOT/admin"
    assert_switchable_path "$PROBE_ROOT/migrations"

    atomic_release_link "$release_dir/api/probe-api" "$PROBE_API_DIR/probe-api" || return 1
    atomic_release_link "$release_dir/admin" "$PROBE_ROOT/admin" || return 1
    atomic_release_link "$release_dir/migrations" "$PROBE_ROOT/migrations" || return 1
}

validate_switchable_release_paths() {
    local profile="${1:-management}"
    validate_release_profile "$profile"
    assert_switchable_path "$PROBE_API_DIR/probe-api"
    assert_switchable_path "$PROBE_ROOT/admin"
    assert_switchable_path "$PROBE_ROOT/migrations"
}

restore_release_links() {
    local old_api="$1" old_admin="$2" old_migrations="$3"
    local link_path target failed=0
    local -a links=("$PROBE_API_DIR/probe-api" "$PROBE_ROOT/admin" "$PROBE_ROOT/migrations")
    local -a targets=("$old_api" "$old_admin" "$old_migrations")

    local index
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

validate_runtime_listeners() {
    local profile="${1:-management}"
    validate_release_profile "$profile"
    require_commands ss
    local listeners
    listeners="$(ss -H -lnt)"

    awk -v mode="$PROBE_INGRESS_MODE" '
        BEGIN {
            if (mode != "domain" && mode != "ip") bad=1
        }
        {
            endpoint=$4
            port=endpoint
            sub(/^.*:/, "", port)
            host=endpoint
            sub(/:[^:]*$/, "", host)
            sub(/^\[/, "", host)
            sub(/\]$/, "", host)
            if (mode == "domain" && (port == "80" || port == "443")) ingress[port]=1
            if (mode == "ip" && port == "18455") ingress[port]=1
            if (port == "8080") {
                api_count++
                if (host != "127.0.0.1") bad=1
            }
            if (port == "5432") {
                postgres_count++
                if (host != "127.0.0.1" && host != "::1") bad=1
            }
        }
        END {
            if (mode == "domain" && (!ingress["80"] || !ingress["443"])) bad=1
            if (mode == "ip" && !ingress["18455"]) bad=1
            if (api_count < 1 || postgres_count < 1) bad=1
            if (bad) exit 1
        }
    ' <<<"$listeners" || die "runtime listeners violate the management ingress or loopback-only service contract"
}

verify_running_services() {
    local profile="${1:-management}"
    validate_release_profile "$profile"
    assert_probe_api_service_account
    systemctl is-active --quiet "$(runtime_postgres_service)" || die "PostgreSQL is not active"
    systemctl is-active --quiet probe-api.service || die "probe-api is not active"
    systemctl is-active --quiet nginx.service || die "Nginx is not active"
    systemctl is-active --quiet probe-postgres-backup.timer ||
        die "probe-postgres-backup.timer is not active"
    validate_ingress_tls_with_binary "$PROBE_API_DIR/probe-api" "$profile"
    validate_certbot_timer_state "$profile"
    curl --fail --silent --show-error --max-time 10 \
        http://127.0.0.1:8080/internal/health/ready >/dev/null ||
        die "probe-api readiness check failed"
    validate_runtime_listeners "$profile"
}

deploy_prebuilt_release() {
    local bundle_root="$1" release_id="$2" expected_profile="${3:-management}"
    validate_release_profile "$expected_profile"
    require_supported_runtime_platform
    require_commands bash cp env find install sha256sum sort xargs nginx \
        systemctl systemd-analyze setpriv curl ss flock awk grep sed stat readlink python3
    require_runtime_postgres_commands

    bundle_root="$(canonical_directory "$bundle_root")"
    [[ "$release_id" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$ ]] ||
        die "prebuilt release identifier is invalid"

    local release_profile
    release_profile="$(release_bundle_profile "$bundle_root")"
    [[ "$release_profile" == "$expected_profile" ]] ||
        die "release profile does not match the management installer"
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
    unique_release_id="$release_id-$(date -u +%Y%m%dT%H%M%SZ)-$$"
    stage_release "$artifact_root" "$unique_release_id" "$release_profile"
    release_dir="$STAGED_RELEASE_DIR"
    [[ "$release_dir" == "$PROBE_RELEASES_DIR/$unique_release_id" ]] ||
        die 'staged management release did not publish its exact final directory'
    validate_release_artifacts "$release_dir" "$release_profile" staged
    (
        cd "$release_dir" || die "could not enter verified management release: $release_dir"
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

    local old_api old_admin old_migrations
    old_api="$(current_release_target "$PROBE_API_DIR/probe-api")"
    old_admin="$(current_release_target "$PROBE_ROOT/admin")"
    old_migrations="$(current_release_target "$PROBE_ROOT/migrations")"
    MANAGEMENT_ROLLBACK_OLD_API="$old_api"
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
    log "prebuilt release $unique_release_id is active"
    log "previous release directories and $backup_path were retained"
}
