#!/usr/bin/env bash

set -Eeuo pipefail

PANEL_URL="${PANEL_URL:-}"
ADMIN_URL="${ADMIN_URL:-}"
AGENT_URL="${AGENT_URL:-}"
CONNECT_TIMEOUT="${CONNECT_TIMEOUT:-5}"
MAX_TIME="${MAX_TIME:-15}"
CA_CERT="${CA_CERT:-}"
INSECURE=0

usage() {
    cat <<'USAGE'
Usage:
  security-smoke.sh --panel-url URL --admin-url URL --agent-url URL [options]

Required URLs may also be supplied with PANEL_URL, ADMIN_URL, and AGENT_URL.

Options:
  --panel-url URL          Visitor-panel origin, without an API path
  --admin-url URL          Administrator-panel origin, without an API path
  --agent-url URL          Agent API origin, without an API path
  --connect-timeout SEC    curl connection timeout (default: 5)
  --max-time SEC           curl total timeout per request (default: 15)
  --cacert FILE            Trust this absolute PEM CA/certificate path
  --insecure               Allow an untrusted TLS certificate (preview only)
  -h, --help               Show this help

This check deliberately sends no cookie, CSRF token, administrator credential,
or valid Agent token. It is safe to run only from an IP/CIDR that is expected to
reach both browser entry points.
USAGE
}

die() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 2
}

is_positive_number() {
    [[ "$1" =~ ^[0-9]+([.][0-9]+)?$ ]] &&
        awk -v value="$1" 'BEGIN { exit !(value > 0) }'
}

validate_base_url() {
    local label="$1"
    local value="$2"
    local remainder authority

    [[ -n "$value" ]] || die "$label is required"
    [[ ! "$value" =~ [[:space:]] ]] || die "$label must not contain whitespace"
    case "$value" in
        http://* | https://*) ;;
        *) die "$label must start with http:// or https://" ;;
    esac
    case "$value" in
        *'?'* | *'#'*) die "$label must be an origin/base URL without a query or fragment" ;;
    esac

    remainder="${value#*://}"
    authority="${remainder%%/*}"
    [[ -n "$authority" && "$authority" != *'@'* ]] ||
        die "$label must not contain URL credentials"

    while [[ "$value" == */ ]]; do
        value="${value%/}"
    done
    printf '%s' "$value"
}

while (($# > 0)); do
    case "$1" in
        --panel-url)
            (($# >= 2)) || die "--panel-url requires a value"
            PANEL_URL="$2"
            shift 2
            ;;
        --admin-url)
            (($# >= 2)) || die "--admin-url requires a value"
            ADMIN_URL="$2"
            shift 2
            ;;
        --agent-url)
            (($# >= 2)) || die "--agent-url requires a value"
            AGENT_URL="$2"
            shift 2
            ;;
        --connect-timeout)
            (($# >= 2)) || die "--connect-timeout requires a value"
            CONNECT_TIMEOUT="$2"
            shift 2
            ;;
        --max-time)
            (($# >= 2)) || die "--max-time requires a value"
            MAX_TIME="$2"
            shift 2
            ;;
        --cacert)
            (($# >= 2)) || die "--cacert requires a value"
            CA_CERT="$2"
            shift 2
            ;;
        --insecure)
            INSECURE=1
            shift
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        *)
            die "unknown option: $1"
            ;;
    esac
done

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v awk >/dev/null 2>&1 || die "awk is required"
command -v mktemp >/dev/null 2>&1 || die "mktemp is required"
command -v grep >/dev/null 2>&1 || die "grep is required"
command -v head >/dev/null 2>&1 || die "head is required"
command -v tr >/dev/null 2>&1 || die "tr is required"
command -v date >/dev/null 2>&1 || die "date is required"

is_positive_number "$CONNECT_TIMEOUT" || die "connect timeout must be greater than zero"
is_positive_number "$MAX_TIME" || die "max time must be greater than zero"
if [[ -n "$CA_CERT" ]]; then
    [[ "$CA_CERT" == /* && -f "$CA_CERT" && ! -L "$CA_CERT" && -r "$CA_CERT" ]] ||
        die "CA certificate must be a readable absolute regular file, not a symlink"
fi
((INSECURE == 0)) || [[ -z "$CA_CERT" ]] || die "--cacert and --insecure are mutually exclusive"

PANEL_URL="$(validate_base_url PANEL_URL "$PANEL_URL")"
ADMIN_URL="$(validate_base_url ADMIN_URL "$ADMIN_URL")"
AGENT_URL="$(validate_base_url AGENT_URL "$AGENT_URL")"

TMP_DIR="$(mktemp -d -t probe-security-smoke.XXXXXX)"
trap 'rm -rf -- "$TMP_DIR"' EXIT

CURL_ARGS=(
    --disable
    --silent
    --show-error
    --connect-timeout "$CONNECT_TIMEOUT"
    --max-time "$MAX_TIME"
    --header 'Accept: application/json'
    --header 'Cookie:'
    --header 'X-CSRF-Token:'
    --user-agent 'probe-security-smoke/1'
)
if ((INSECURE == 1)); then
    CURL_ARGS+=(--insecure)
elif [[ -n "$CA_CERT" ]]; then
    CURL_ARGS+=(--cacert "$CA_CERT")
fi

TOTAL=0
PASSED=0
FAILED=0
LAST_HEADERS=""

check_status() {
    local name="$1"
    local expected="$2"
    local url="$3"
    shift 3

    local number headers body status curl_rc snippet
    TOTAL=$((TOTAL + 1))
    number="$TOTAL"
    headers="$TMP_DIR/${number}.headers"
    body="$TMP_DIR/${number}.body"
    LAST_HEADERS="$headers"

    if status="$(curl "${CURL_ARGS[@]}" \
        --dump-header "$headers" \
        --output "$body" \
        --write-out '%{http_code}' \
        "$@" \
        --url "$url")"; then
        curl_rc=0
    else
        curl_rc=$?
    fi

    if ((curl_rc == 0)) && [[ "$status" == "$expected" ]]; then
        PASSED=$((PASSED + 1))
        printf 'PASS  %-58s HTTP %s\n' "$name" "$status"
        return 0
    fi

    FAILED=$((FAILED + 1))
    snippet=""
    if [[ -s "$body" ]]; then
        snippet="$(head -c 240 "$body" | tr '\r\n' '  ')"
    fi
    printf 'FAIL  %-58s expected=%s actual=%s curl_rc=%s\n' \
        "$name" "$expected" "${status:-000}" "$curl_rc" >&2
    if [[ -n "$snippet" ]]; then
        printf '      response: %s\n' "$snippet" >&2
    fi
    return 1
}

check_header_absent() {
    local name="$1"
    local header_name="$2"

    TOTAL=$((TOTAL + 1))
    if [[ -f "$LAST_HEADERS" ]] && ! grep -iq "^${header_name}:" "$LAST_HEADERS"; then
        PASSED=$((PASSED + 1))
        printf 'PASS  %-58s absent\n' "$name"
        return 0
    fi

    FAILED=$((FAILED + 1))
    printf 'FAIL  %-58s unexpected %s header\n' "$name" "$header_name" >&2
    return 1
}

printf 'Probe Panel security smoke\n'
printf '  panel: %s\n  admin: %s\n  agent: %s\n\n' \
    "$PANEL_URL" "$ADMIN_URL" "$AGENT_URL"

# Visitor entry: static UI and panel data are anonymous; all other API
# namespaces remain dark on this host.
check_status "visitor entry serves its static root" 200 "$PANEL_URL/" || true
check_status "visitor reads panel nodes without credentials" 200 \
    "$PANEL_URL/api/v1/panel/nodes?limit=1" || true
check_header_absent "visitor read does not create a session" "Set-Cookie" || true
check_status "visitor entry hides administrator authentication" 404 \
    "$PANEL_URL/api/v1/auth/me" || true
check_status "visitor entry hides administrator API" 404 \
    "$PANEL_URL/api/v1/admin/users" || true
check_status "visitor entry hides Agent API" 404 \
    "$PANEL_URL/api/v1/agent/config?version=0" || true
check_status "visitor entry hides Agent downloads" 404 \
    "$PANEL_URL/downloads/probe-agent/install.sh" || true

# Administrator entry: its own static UI and panel reads are reachable from an
# allowlisted source, but auth/admin API calls require an administrator session.
check_status "administrator entry serves its static root" 200 "$ADMIN_URL/" || true
check_status "administrator entry exposes read-only panel data" 200 \
    "$ADMIN_URL/api/v1/panel/nodes?limit=1" || true
check_status "administrator identity rejects a missing session" 401 \
    "$ADMIN_URL/api/v1/auth/me" || true
check_status "administrator API rejects a missing session" 401 \
    "$ADMIN_URL/api/v1/admin/users" || true
check_status "administrator entry hides Agent API" 404 \
    "$ADMIN_URL/api/v1/agent/config?version=0" || true
check_status "administrator entry hides Agent downloads" 404 \
    "$ADMIN_URL/downloads/probe-agent/install.sh" || true

# Agent entry: only Agent paths are exposed. The generated value is guaranteed
# to be a deliberately invalid, non-secret smoke value rather than a deployed
# credential.
INVALID_AGENT_TOKEN="security-smoke-invalid-$(date -u +%s)-$$"
check_status "Agent API rejects an invalid bearer token" 401 \
    "$AGENT_URL/api/v1/agent/config?version=0" \
    --header "Authorization: Bearer ${INVALID_AGENT_TOKEN}" || true
check_status "Agent entry hides visitor panel API" 404 \
    "$AGENT_URL/api/v1/panel/nodes?limit=1" || true
check_status "Agent entry hides administrator authentication" 404 \
    "$AGENT_URL/api/v1/auth/me" || true
check_status "Agent entry hides administrator API" 404 \
    "$AGENT_URL/api/v1/admin/users" || true
check_status "Agent entry serves the reviewed bootstrap installer" 200 \
    "$AGENT_URL/downloads/probe-agent/install.sh" || true
check_header_absent "Agent download does not create a session" "Set-Cookie" || true
check_status "Agent entry serves the checksum manifest" 200 \
    "$AGENT_URL/downloads/probe-agent/SHA256SUMS" || true
check_status "Agent entry rejects unknown download files" 404 \
    "$AGENT_URL/downloads/probe-agent/unknown" || true
check_status "Agent download route rejects writes" 403 \
    "$AGENT_URL/downloads/probe-agent/install.sh" --request POST || true
check_status "Agent entry has no static-site fallback" 404 "$AGENT_URL/" || true

printf '\nSummary: total=%d passed=%d failed=%d\n' "$TOTAL" "$PASSED" "$FAILED"
if ((FAILED > 0)); then
    exit 1
fi
