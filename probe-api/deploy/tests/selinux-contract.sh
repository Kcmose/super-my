#!/usr/bin/env bash
# Static contracts below intentionally contain unexpanded shell expressions.
# shellcheck disable=SC2016

# Static and fixture-only contract for the candidate management SELinux helper.
# The helper is sourced below; no host SELinux state is read or changed.

set -Eeuo pipefail
export LC_ALL=C

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd)"
POLICY="$ROOT_DIR/selinux/probe-panel-nginx.te"
HELPER="$ROOT_DIR/selinux/probe-panel-selinux-nginx.sh"

fail() {
    printf 'selinux-contract: FAIL: %s\n' "$*" >&2
    exit 1
}

assert_contains() {
    local file="$1" literal="$2"
    grep -Fq -- "$literal" "$file" || fail "$file is missing: $literal"
}

assert_not_contains() {
    local file="$1" literal="$2"
    if grep -Fq -- "$literal" "$file"; then
        fail "$file contains forbidden text: $literal"
    fi
}

assert_fails() {
    local label="$1" status
    shift
    set +e
    ( set -e; "$@" ) >/dev/null 2>&1
    status=$?
    set -e
    if ((status == 0)); then
        fail "$label unexpectedly succeeded"
    fi
}

assert_equals() {
    local expected="$1" actual="$2" label="$3"
    [[ "$actual" == "$expected" ]] ||
        fail "$label: expected '$expected', got '$actual'"
}

[[ -f "$POLICY" ]] || fail "missing policy: $POLICY"
[[ -f "$HELPER" ]] || fail "missing helper: $HELPER"
bash -n "$HELPER"
bash -n "$0"

# The custom policy is bind-only and covers only the public management port.
assert_contains "$POLICY" 'type probe_panel_ingress_port_t;'
assert_contains "$POLICY" 'allow httpd_t probe_panel_ingress_port_t:tcp_socket name_bind;'
assert_not_contains "$POLICY" 'probe_panel_api_port_t'
assert_not_contains "$POLICY" 'name_connect'
assert_equals 1 "$(awk '$1 == "allow" { count++ } END { print count + 0 }' "$POLICY")" \
    'custom policy allow-rule count'

# The helper must preserve the vendor 8080 mapping and must never mutate an
# occupied port. It may add/delete only the exact owned 18455 mapping.
assert_contains "$HELPER" "readonly INGRESS_PORT='18455'"
assert_contains "$HELPER" "readonly API_PORT='8080'"
assert_contains "$HELPER" "readonly RELAY_BOOLEAN='httpd_can_network_relay'"
assert_contains "$HELPER" 'http_cache_port_t|http_port_t) PLAN_API_TYPE="$api_type"'
assert_contains "$HELPER" 'semanage port -a -t "$INGRESS_PORT_TYPE" -p tcp "$INGRESS_PORT"'
assert_contains "$HELPER" 'semanage port -d -p tcp "$INGRESS_PORT"'
assert_contains "$HELPER" 'setsebool -P "$RELAY_BOOLEAN" on'
assert_not_contains "$HELPER" 'httpd_can_network_connect'
assert_not_contains "$HELPER" 'semanage port -m'
assert_not_contains "$HELPER" 'semanage boolean -D'
assert_not_contains "$HELPER" 'probe_panel_api_port_t'
assert_not_contains "$HELPER" 'API_PORT_TYPE'
assert_equals 2 "$(awk '
    $1 == "semanage" && $2 == "port" && $3 ~ /^-[adm]$/ { count++ }
    $1 == "if" && $2 == "!" && $3 == "semanage" && $4 == "port" && $5 ~ /^-[adm]$/ { count++ }
    END { print count + 0 }
' "$HELPER")" \
    'owned port mutation count'

# State and locking are deliberately private and state replacement is atomic.
assert_contains "$HELPER" "readonly LOCK_FILE='/run/lock/probe-panel-selinux.lock'"
assert_contains "$HELPER" 'flock --exclusive "$LOCK_FD"'
assert_contains "$HELPER" 'mv -T -- "$temporary" "$STATE_FILE"'
assert_contains "$HELPER" 'install -d -o root -g root -m 0700 -- "$STATE_PARENT"'
assert_contains "$HELPER" 'install -d -o root -g root -m 0700 -- "$STATE_DIR"'
assert_contains "$HELPER" 'assert_private_state_directory "$STATE_PARENT"'
assert_contains "$HELPER" 'assert_private_state_directory "$STATE_DIR"'
assert_contains "$HELPER" 'chmod 0600 "$temporary"'
assert_contains "$HELPER" 'rm -f -- "$STATE_FILE" || failures=1'

# Representative file-context boundaries must remain explicit.
assert_contains "$HELPER" "'/srv/probe/releases/[^/]+/admin(/.*)?|httpd_sys_content_t'"
assert_contains "$HELPER" "'/srv/probe/config/nginx(/.*)?|httpd_config_t'"
assert_contains "$HELPER" "'/etc/probe-panel/tls/private-ca/(ca[.]pem|fullchain[.]pem|privkey[.]pem)|cert_t'"
assert_contains "$HELPER" "'/srv/probe/releases/[^/]+/artifacts/api/probe-api|bin_t'"

# The runtime-computed path was checked as a regular file above.
# shellcheck source=../selinux/probe-panel-selinux-nginx.sh
# shellcheck disable=SC1091
source "$HELPER"

PORT_ALL_FIXTURE=''
PORT_LOCAL_FIXTURE=''
BOOLEAN_ALL_FIXTURE=''
BOOLEAN_LOCAL_FIXTURE=''
BOOLEAN_ACTIVE_FIXTURE=''
SEMANAGE_FIXTURE_FAIL=false

# Fixture commands replace the host tools used by the pure planning/parsing
# functions. Any unexpected invocation fails the contract.
semanage() {
    if [[ "$SEMANAGE_FIXTURE_FAIL" == true ]]; then
        return 42
    fi
    case "$1:${2:-}:${3:-}" in
        port:-l:) printf '%s\n' "$PORT_ALL_FIXTURE" ;;
        port:-C:-l) printf '%s\n' "$PORT_LOCAL_FIXTURE" ;;
        boolean:-l:) printf '%s\n' "$BOOLEAN_ALL_FIXTURE" ;;
        boolean:-C:-l) printf '%s\n' "$BOOLEAN_LOCAL_FIXTURE" ;;
        *) fail "unexpected fixture semanage invocation: $*" ;;
    esac
}

getsebool() {
    [[ "$#" -eq 1 && "$1" == "$RELAY_BOOLEAN" ]] ||
        fail "unexpected fixture getsebool invocation: $*"
    printf '%s\n' "$BOOLEAN_ACTIVE_FIXTURE"
}

run_port_plan() {
    PLAN_INGRESS_ACTION=''
    PLAN_API_TYPE=''
    evaluate_port_plan
    printf '%s|%s\n' "$PLAN_INGRESS_ACTION" "$PLAN_API_TYPE"
}

PORT_ALL_FIXTURE=$(printf '%s\n' \
    'http_cache_port_t              tcp      8080, 8118, 8123, 10001-10010')
assert_equals 'owned|http_cache_port_t' "$(run_port_plan)" \
    'unmapped 18455 and stock CentOS 7 API mapping'

PORT_ALL_FIXTURE=$(printf '%s\n' \
    'http_port_t                    tcp      80, 443, 8080, 18455')
assert_equals 'preexisting|http_port_t' "$(run_port_plan)" \
    'pre-existing vendor http_port_t mappings'

PORT_ALL_FIXTURE=$(printf '%s\n' \
    'http_cache_port_t              tcp      8080' \
    'ssh_port_t                     tcp      22, 18455')
assert_fails 'foreign 18455 mapping' run_port_plan

PORT_ALL_FIXTURE=$(printf '%s\n' \
    'postgresql_port_t              tcp      5432, 8080' \
    'http_port_t                    tcp      18455')
assert_fails 'foreign 8080 mapping' run_port_plan

PORT_ALL_FIXTURE=$(printf '%s\n' \
    'http_port_t                    tcp      80, 443, 18455')
assert_fails 'unmapped 8080' run_port_plan

PORT_ALL_FIXTURE=$(printf '%s\n' \
    'http_cache_port_t              tcp      8080' \
    'http_port_t                    tcp      80, 443, 8080, 18455')
assert_fails 'overlapping 8080 mappings' run_port_plan

SEMANAGE_FIXTURE_FAIL=true
assert_fails 'failed semanage port query' run_port_plan
SEMANAGE_FIXTURE_FAIL=false

run_boolean_plan() {
    PLAN_BOOLEAN_CHANGED=''
    PLAN_BOOLEAN_OLD_ACTIVE=''
    PLAN_BOOLEAN_OLD_PERSISTENT=''
    PLAN_BOOLEAN_OLD_LOCAL=''
    capture_boolean_plan
    printf '%s|%s|%s|%s\n' "$PLAN_BOOLEAN_CHANGED" \
        "$PLAN_BOOLEAN_OLD_ACTIVE" "$PLAN_BOOLEAN_OLD_PERSISTENT" \
        "$PLAN_BOOLEAN_OLD_LOCAL"
}

BOOLEAN_ALL_FIXTURE='httpd_can_network_relay          (off  ,  off)  Determine whether httpd may act as a relay'
BOOLEAN_LOCAL_FIXTURE=''
BOOLEAN_ACTIVE_FIXTURE='httpd_can_network_relay --> off'
assert_equals '1|off|off|none' "$(run_boolean_plan)" 'disabled relay Boolean'

BOOLEAN_ALL_FIXTURE='httpd_can_network_relay          (on   ,   off) Determine whether httpd may act as a relay'
BOOLEAN_LOCAL_FIXTURE=''
BOOLEAN_ACTIVE_FIXTURE='httpd_can_network_relay --> on'
assert_equals '1|on|off|none' "$(run_boolean_plan)" \
    'temporarily enabled but non-persistent relay Boolean'

BOOLEAN_ALL_FIXTURE='httpd_can_network_relay          (on   ,   on)  Determine whether httpd may act as a relay'
BOOLEAN_LOCAL_FIXTURE='httpd_can_network_relay          (on   ,   on)  Determine whether httpd may act as a relay'
BOOLEAN_ACTIVE_FIXTURE='httpd_can_network_relay --> on'
assert_equals '0|on|on|on' "$(run_boolean_plan)" 'enabled relay Boolean'

BOOLEAN_ALL_FIXTURE='httpd_can_network_relay          (off  ,   off) Determine whether httpd may act as a relay'
BOOLEAN_LOCAL_FIXTURE='httpd_can_network_relay          (on   ,   off) Determine whether httpd may act as a relay'
BOOLEAN_ACTIVE_FIXTURE='httpd_can_network_relay --> off'
assert_equals '1|off|on|on' "$(run_boolean_plan)" 'persistent-on active-off relay Boolean'

BOOLEAN_ALL_FIXTURE=''
BOOLEAN_LOCAL_FIXTURE=''
BOOLEAN_ACTIVE_FIXTURE='httpd_can_network_relay --> off'
assert_fails 'missing relay Boolean' run_boolean_plan

FIXTURE_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/probe-selinux-contract.XXXXXX")"
trap 'rm -rf -- "$FIXTURE_ROOT"' EXIT

write_valid_state_fixture() {
    local target="$1" rule
    {
        printf 'STATE_VERSION=%s\n' "$STATE_VERSION"
        printf 'STATUS=complete\n'
        printf 'MODULE=owned|%s|%s\n' "$MODULE_NAME" "$MODULE_VERSION"
        printf 'INGRESS=owned|tcp|%s|%s\n' "$INGRESS_PORT" "$INGRESS_PORT_TYPE"
        printf 'API=preexisting|tcp|%s|http_cache_port_t\n' "$API_PORT"
        printf 'BOOLEAN=%s|1|off|off|none|on\n' "$RELAY_BOOLEAN"
        for rule in "${FCONTEXT_RULES[@]}"; do
            printf 'FCONTEXT=owned|%s\n' "$rule"
        done
    } > "$target"
}

VALID_STATE="$FIXTURE_ROOT/valid.state"
write_valid_state_fixture "$VALID_STATE"
parse_state_file "$VALID_STATE"
assert_equals complete "$STATE_STATUS" 'valid state status'
assert_equals owned "$STATE_MODULE_ACTION" 'valid state module ownership'
assert_equals "${#FCONTEXT_RULES[@]}" "${#STATE_FCONTEXTS[@]}" \
    'valid state fcontext count'

DUPLICATE_STATE="$FIXTURE_ROOT/duplicate.state"
cp -- "$VALID_STATE" "$DUPLICATE_STATE"
printf 'STATUS=complete\n' >> "$DUPLICATE_STATE"
assert_fails 'duplicate singleton state field' parse_state_file "$DUPLICATE_STATE"

UNKNOWN_STATE="$FIXTURE_ROOT/unknown.state"
cp -- "$VALID_STATE" "$UNKNOWN_STATE"
printf 'SURPRISE=value\n' >> "$UNKNOWN_STATE"
assert_fails 'unknown state field' parse_state_file "$UNKNOWN_STATE"

PENDING_COMPLETE_STATE="$FIXTURE_ROOT/pending-complete.state"
awk '/^BOOLEAN=/ { sub(/[|]on$/, "|pending") } { print }' \
    "$VALID_STATE" > "$PENDING_COMPLETE_STATE"
assert_fails 'pending Boolean value in complete state' \
    parse_state_file "$PENDING_COMPLETE_STATE"

MISSING_FCONTEXT_STATE="$FIXTURE_ROOT/missing-fcontext.state"
grep -vF -- "FCONTEXT=owned|${FCONTEXT_RULES[${#FCONTEXT_RULES[@]} - 1]}" \
    "$VALID_STATE" > "$MISSING_FCONTEXT_STATE"
assert_fails 'missing managed fcontext state' parse_state_file "$MISSING_FCONTEXT_STATE"

printf 'selinux-contract: PASS\n'
