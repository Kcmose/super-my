#!/usr/bin/env bash

# Candidate SELinux policy manager for the management-only Nginx boundary.
# It is deliberately self-contained and is not wired into the product
# lifecycle yet. It never relabels an occupied port and records every owned
# mutation before applying it.

set -Eeuo pipefail
umask 077
export LC_ALL=C

readonly MODULE_NAME='probe_panel_nginx'
readonly MODULE_VERSION='1.1'
readonly INGRESS_PORT='18455'
readonly INGRESS_PORT_TYPE='probe_panel_ingress_port_t'
readonly API_PORT='8080'
readonly RELAY_BOOLEAN='httpd_can_network_relay'
readonly STATE_VERSION='2'
readonly STATE_PARENT='/var/lib/probe-panel'
readonly STATE_DIR="${STATE_PARENT}/selinux"
readonly STATE_FILE="${STATE_DIR}/nginx-management.state"
readonly LOCK_FILE='/run/lock/probe-panel-selinux.lock'
readonly LOCK_FD=8

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
readonly POLICY_SOURCE="${SCRIPT_DIR}/probe-panel-nginx.te"

WORK_ROOT=''
STATE_TEMP=''
INSTALL_TRANSACTION=false

readonly -a FCONTEXT_RULES=(
    '/srv/probe|httpd_sys_content_t'
    '/srv/probe/admin|httpd_sys_content_t'
    '/srv/probe/releases|httpd_sys_content_t'
    '/srv/probe/releases/[^/]+|httpd_sys_content_t'
    '/srv/probe/releases/[^/]+/admin(/.*)?|httpd_sys_content_t'
    '/srv/probe/config|httpd_config_t'
    '/srv/probe/config/nginx(/.*)?|httpd_config_t'
    '/etc/probe-panel/admin-allowlist[.]geo|httpd_config_t'
    '/etc/probe-panel/tls|cert_t'
    '/etc/probe-panel/tls/admin(/.*)?|cert_t'
    '/etc/probe-panel/tls/private-ca|cert_t'
    '/etc/probe-panel/tls/private-ca/(ca[.]pem|fullchain[.]pem|privkey[.]pem)|cert_t'
    '/srv/probe/releases/[^/]+/artifacts/api/probe-api|bin_t'
    '/srv/probe/releases/[^/]+/source/probe-api/deploy/scripts/install-release[.]sh|bin_t'
    '/srv/probe/releases/[^/]+/api/probe-api|bin_t'
    '/srv/probe/api/scripts/backup-postgres[.]sh|bin_t'
    '/srv/probe/api/scripts/restore-postgres[.]sh|bin_t'
    '/usr/local/lib/probe-panel/probe-setup|bin_t'
)

log() {
    printf '[probe-selinux] %s\n' "$*" >&2
}

die() {
    printf '[probe-selinux] ERROR: %s\n' "$*" >&2
    if [[ "${INSTALL_TRANSACTION:-false}" == true ]]; then
        on_install_error 1
    fi
    exit 1
}

usage() {
    cat <<'EOF'
Usage: probe-panel-selinux-nginx.sh COMMAND

Commands:
  preflight  verify policy, fixed ports, Boolean and local-rule ownership
  install    transactionally install only Probe-owned SELinux changes
  status     verify the recorded installation without changing labels
  refresh    verify the recorded installation and restore managed labels
  rollback   remove only unchanged resources recorded as Probe-owned

TCP 8080 must already be http_cache_port_t or http_port_t and is never
relabeled. TCP 18455 is assigned a Probe-only bind type only when unmapped.
EOF
}

require_root() {
    [[ "$(id -u)" -eq 0 ]] || die 'this command must run as root'
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "required command is missing: $1"
}

require_selinux_tools() {
    local command mode
    for command in awk checkmodule chmod chown dirname find flock getenforce \
        getsebool id install mktemp mv restorecon rm rmdir \
        semanage semodule semodule_package setsebool stat; do
        require_command "$command"
    done
    mode="$(getenforce)"
    case "$mode" in
        Enforcing|Permissive) ;;
        Disabled) die 'SELinux is Disabled; no persistent policy may be claimed' ;;
        *) die "getenforce returned an unexpected mode: $mode" ;;
    esac
}

cleanup_work_root() {
    if [[ -n "$WORK_ROOT" && -d "$WORK_ROOT" && ! -L "$WORK_ROOT" ]]; then
        case "$WORK_ROOT" in
            /var/tmp/probe-panel-selinux.*) rm -rf -- "$WORK_ROOT" ;;
            *) log "refusing to remove unexpected work directory: $WORK_ROOT" ;;
        esac
    fi
    WORK_ROOT=''
}

cleanup_state_temp() {
    if [[ -n "$STATE_TEMP" && -f "$STATE_TEMP" && ! -L "$STATE_TEMP" ]]; then
        case "$STATE_TEMP" in
            "$STATE_DIR"/.nginx-management.state.*) rm -f -- "$STATE_TEMP" ;;
            *) log "refusing to remove unexpected state temporary: $STATE_TEMP" ;;
        esac
    fi
    STATE_TEMP=''
}

acquire_selinux_lock() {
    local lock_root_mode path_identity descriptor_identity
    [[ -d /run/lock && ! -L /run/lock ]] || die '/run/lock must be a real directory'
    [[ "$(stat -c '%U:%G' -- /run/lock)" == root:root ]] ||
        die '/run/lock must be owned by root:root'
    lock_root_mode="$(stat -c '%a' -- /run/lock)"
    [[ "$lock_root_mode" =~ ^[0-7]{3,4}$ ]] || die '/run/lock has an invalid mode'
    if [[ "$lock_root_mode" != 1777 ]]; then
        (( (8#$lock_root_mode & 8#022) == 0 )) ||
            die '/run/lock must use mode 1777 or must not be group/world-writable'
    fi

    if [[ -L "$LOCK_FILE" || ( -e "$LOCK_FILE" && ! -f "$LOCK_FILE" ) ]]; then
        die "$LOCK_FILE must be a real regular file"
    fi
    if [[ ! -e "$LOCK_FILE" ]]; then
        (umask 077; set -o noclobber; : > "$LOCK_FILE") 2>/dev/null || :
    fi
    [[ -f "$LOCK_FILE" && ! -L "$LOCK_FILE" ]] || die "unsafe lock file: $LOCK_FILE"
    [[ "$(stat -c '%U:%G:%a' -- "$LOCK_FILE")" == root:root:600 ]] ||
        die "$LOCK_FILE must be root:root mode 0600"

    exec 8>>"$LOCK_FILE"
    path_identity="$(stat -c '%d:%i' -- "$LOCK_FILE")"
    descriptor_identity="$(stat -Lc '%d:%i' -- "/proc/self/fd/$LOCK_FD")"
    [[ "$path_identity" == "$descriptor_identity" ]] || die "$LOCK_FILE changed while opening it"
    flock --exclusive "$LOCK_FD"
}

# Print every SELinux port type whose TCP range contains the requested port.
# Only POSIX/old-awk features are used for CentOS 7 compatibility.
port_types_for() {
    local wanted="$1" scope="${2:-all}"
    local -a command=(semanage port -l)
    [[ "$scope" == local ]] && command=(semanage port -C -l)
    "${command[@]}" | awk -v wanted="$wanted" '
        $2 == "tcp" {
            type=$1
            line=$0
            sub(/^[^ \t]+[ \t]+[^ \t]+[ \t]+/, "", line)
            count=split(line, entries, ",")
            for (i=1; i<=count; i++) {
                entry=entries[i]
                gsub(/^[ \t]+|[ \t]+$/, "", entry)
                if (entry ~ /^[0-9]+$/) {
                    if ((entry + 0) == wanted && !seen[type]++) print type
                } else if (entry ~ /^[0-9]+-[0-9]+$/) {
                    split(entry, limits, "-")
                    if (wanted >= (limits[1] + 0) && wanted <= (limits[2] + 0) && !seen[type]++) print type
                }
            }
        }
    '
}

single_port_type_for() {
    local port="$1" scope="${2:-all}" types type count=0 result=''
    types="$(port_types_for "$port" "$scope")" ||
        die "could not query SELinux TCP port mappings for $port"
    while IFS= read -r type; do
        [[ -n "$type" ]] || continue
        result="$type"
        count=$((count + 1))
    done <<< "$types"
    ((count <= 1)) || die "TCP port $port is covered by multiple SELinux port types: ${types//$'\n'/, }"
    printf '%s\n' "$result"
}

module_is_installed() {
    local version
    version="$(installed_module_version)" || die "could not query module $MODULE_NAME"
    [[ -n "$version" ]]
}

installed_module_version() {
    semodule -l | awk -v wanted="$MODULE_NAME" \
        '$1 == wanted && !found { print $2; found=1 }'
}

context_type_from_context() {
    local context="$1" remainder
    remainder="${context#*:}"
    remainder="${remainder#*:}"
    printf '%s\n' "${remainder%%:*}"
}

fcontext_types_for() {
    local expression="$1" scope="${2:-all}"
    local -a command=(semanage fcontext -l)
    [[ "$scope" == local ]] && command=(semanage fcontext -C -l)
    "${command[@]}" | awk -v wanted="$expression" '$1 == wanted { print $NF }' |
        while IFS= read -r context; do
            [[ -n "$context" ]] || continue
            context_type_from_context "$context"
        done
}

single_fcontext_type_for() {
    local expression="$1" scope="${2:-all}" types type count=0 result=''
    types="$(fcontext_types_for "$expression" "$scope")" ||
        die "could not query fcontext expression: $expression"
    while IFS= read -r type; do
        [[ -n "$type" ]] || continue
        result="$type"
        count=$((count + 1))
    done <<< "$types"
    ((count <= 1)) || die "fcontext expression is defined more than once: $expression"
    printf '%s\n' "$result"
}

local_fcontext_rule_matches() {
    local expression="$1" wanted="$2"
    semanage fcontext -C -l | awk -v expression="$expression" -v wanted=":${wanted}:" '
        $1 == expression {
            count++
            if ($2 == "all" && $3 == "files" && index($NF, wanted)) matched++
        }
        END { exit !(count == 1 && matched == 1) }
    '
}

is_known_fcontext_rule() {
    local expression="$1" wanted="$2" rule
    for rule in "${FCONTEXT_RULES[@]}"; do
        [[ "$rule" == "${expression}|${wanted}" ]] && return 0
    done
    return 1
}

assert_standard_file_types_exist() {
    local wanted
    for wanted in httpd_sys_content_t httpd_config_t cert_t bin_t; do
        semanage fcontext -l |
            awk -v wanted=":${wanted}:" 'index($0, wanted) { found=1 } END { exit !found }' ||
            die "the active SELinux policy does not define required file type $wanted"
    done
}

boolean_pair_from_listing() {
    local name="$1" scope="${2:-all}"
    local -a command=(semanage boolean -l)
    [[ "$scope" == local ]] && command=(semanage boolean -C -l)
    "${command[@]}" | awk -v wanted="$name" '
        $1 == wanted {
            count++
            line=$0
            sub(/^[^(]*[(]/, "", line)
            close_pos=index(line, ")")
            if (!close_pos) {
                invalid=1
                next
            }
            inside=substr(line, 1, close_pos - 1)
            if (split(inside, values, ",") != 2) {
                invalid=1
                next
            }
            state=values[1]
            default_value=values[2]
            gsub(/^[ \t]+|[ \t]+$/, "", state)
            gsub(/^[ \t]+|[ \t]+$/, "", default_value)
            if ((state != "on" && state != "off") ||
                (default_value != "on" && default_value != "off")) invalid=1
            result=state "|" default_value
        }
        END {
            if (invalid || count > 1) exit 2
            if (count == 1) print result
        }
    '
}

active_boolean_value() {
    local output name arrow value extra
    output="$(getsebool "$RELAY_BOOLEAN")" || die "could not read $RELAY_BOOLEAN"
    read -r name arrow value extra <<< "$output"
    [[ "$name" == "$RELAY_BOOLEAN" && "$arrow" == '-->' && -z "${extra:-}" &&
       ( "$value" == on || "$value" == off ) ]] ||
        die "getsebool returned malformed state for $RELAY_BOOLEAN"
    printf '%s\n' "$value"
}

persistent_boolean_value() {
    local pair value
    # A local semanage record is the persistent override. Without one, the
    # policy Default (the second column), not the possibly transient State,
    # is the value that will survive a reboot.
    pair="$(boolean_pair_from_listing "$RELAY_BOOLEAN" local)" ||
        die "could not parse local persistent state for $RELAY_BOOLEAN"
    if [[ -n "$pair" ]]; then
        value="${pair%%|*}"
    else
        pair="$(boolean_pair_from_listing "$RELAY_BOOLEAN")" ||
            die "could not parse policy default for $RELAY_BOOLEAN"
        [[ -n "$pair" ]] || die "the active policy does not define $RELAY_BOOLEAN"
        value="${pair#*|}"
    fi
    printf '%s\n' "$value"
}

local_boolean_value() {
    local pair value
    pair="$(boolean_pair_from_listing "$RELAY_BOOLEAN" local)" ||
        die "could not parse local state for $RELAY_BOOLEAN"
    value="${pair%%|*}"
    case "$value" in
        '') printf '%s\n' none ;;
        on|off) printf '%s\n' "$value" ;;
        *) die "invalid local state for $RELAY_BOOLEAN" ;;
    esac
}

PLAN_INGRESS_ACTION=''
PLAN_API_TYPE=''
PLAN_BOOLEAN_CHANGED=''
PLAN_BOOLEAN_OLD_ACTIVE=''
PLAN_BOOLEAN_OLD_PERSISTENT=''
PLAN_BOOLEAN_OLD_LOCAL=''

evaluate_port_plan() {
    local ingress_type api_type
    ingress_type="$(single_port_type_for "$INGRESS_PORT")"
    case "$ingress_type" in
        '') PLAN_INGRESS_ACTION=owned ;;
        http_port_t) PLAN_INGRESS_ACTION=preexisting ;;
        *) die "refusing to replace TCP $INGRESS_PORT SELinux type $ingress_type" ;;
    esac

    api_type="$(single_port_type_for "$API_PORT")"
    case "$api_type" in
        http_cache_port_t|http_port_t) PLAN_API_TYPE="$api_type" ;;
        '') die "TCP $API_PORT has no SELinux port type; refusing to relabel it" ;;
        *) die "TCP $API_PORT has unsupported SELinux type $api_type; refusing to relabel it" ;;
    esac
}

capture_boolean_plan() {
    PLAN_BOOLEAN_OLD_ACTIVE="$(active_boolean_value)"
    PLAN_BOOLEAN_OLD_PERSISTENT="$(persistent_boolean_value)"
    PLAN_BOOLEAN_OLD_LOCAL="$(local_boolean_value)"
    if [[ "$PLAN_BOOLEAN_OLD_ACTIVE" == on && "$PLAN_BOOLEAN_OLD_PERSISTENT" == on ]]; then
        PLAN_BOOLEAN_CHANGED=0
    else
        PLAN_BOOLEAN_CHANGED=1
    fi
}

check_fcontext_plan() {
    local rule expression wanted current
    for rule in "${FCONTEXT_RULES[@]}"; do
        expression="${rule%%|*}"
        wanted="${rule#*|}"
        current="$(single_fcontext_type_for "$expression")"
        [[ -z "$current" || "$current" == "$wanted" ]] ||
            die "refusing to replace fcontext $expression ($current, required $wanted)"
    done
}

assert_policy_source_safe() {
    local policy_owner policy_mode
    [[ -f "$POLICY_SOURCE" && ! -L "$POLICY_SOURCE" ]] ||
        die "policy source is missing or unsafe: $POLICY_SOURCE"
    policy_owner="$(stat -c '%U' -- "$POLICY_SOURCE")"
    policy_mode="$(stat -c '%a' -- "$POLICY_SOURCE")"
    [[ "$policy_owner" == root && "$policy_mode" =~ ^[0-7]{3,4}$ &&
       $((8#$policy_mode & 8#022)) -eq 0 ]] ||
        die "$POLICY_SOURCE must be root-owned and not group/world-writable"
}

compile_policy() {
    WORK_ROOT="$(mktemp -d /var/tmp/probe-panel-selinux.XXXXXX)"
    checkmodule -M -m -o "$WORK_ROOT/${MODULE_NAME}.mod" "$POLICY_SOURCE"
    semodule_package -o "$WORK_ROOT/${MODULE_NAME}.pp" -m "$WORK_ROOT/${MODULE_NAME}.mod"
}

check_fresh_install_preconditions() {
    [[ ! -e "$STATE_FILE" && ! -L "$STATE_FILE" ]] ||
        die "state already exists; use status or rollback: $STATE_FILE"
    module_is_installed && die "SELinux module $MODULE_NAME already exists without Probe-owned state"
    assert_policy_source_safe
    evaluate_port_plan
    capture_boolean_plan
    assert_standard_file_types_exist
    check_fcontext_plan
    compile_policy
    cleanup_work_root
}

assert_private_state_directory() {
    local path="$1"
    [[ -d "$path" && ! -L "$path" ]] || die "$path must be a real directory"
    [[ "$(stat -c '%U:%G:%a' -- "$path")" == root:root:700 ]] ||
        die "$path must be root:root mode 0700"
}

prepare_state_directory() {
    if [[ -e "$STATE_PARENT" || -L "$STATE_PARENT" ]]; then
        assert_private_state_directory "$STATE_PARENT"
    else
        install -d -o root -g root -m 0700 -- "$STATE_PARENT"
    fi
    if [[ -e "$STATE_DIR" || -L "$STATE_DIR" ]]; then
        assert_private_state_directory "$STATE_DIR"
    else
        install -d -o root -g root -m 0700 -- "$STATE_DIR"
    fi
}

STATE_STATUS=''
STATE_MODULE_ACTION=''
STATE_INGRESS_ACTION=''
STATE_API_TYPE=''
STATE_BOOLEAN_CHANGED=''
STATE_BOOLEAN_OLD_ACTIVE=''
STATE_BOOLEAN_OLD_PERSISTENT=''
STATE_BOOLEAN_OLD_LOCAL=''
STATE_BOOLEAN_INSTALLED_LOCAL=''
STATE_FCONTEXTS=()

reset_loaded_state() {
    STATE_STATUS=''
    STATE_MODULE_ACTION=''
    STATE_INGRESS_ACTION=''
    STATE_API_TYPE=''
    STATE_BOOLEAN_CHANGED=''
    STATE_BOOLEAN_OLD_ACTIVE=''
    STATE_BOOLEAN_OLD_PERSISTENT=''
    STATE_BOOLEAN_OLD_LOCAL=''
    STATE_BOOLEAN_INSTALLED_LOCAL=''
    STATE_FCONTEXTS=()
}

state_has_fcontext_rule() {
    local expression="$1" wanted="$2" entry remainder
    for entry in "${STATE_FCONTEXTS[@]}"; do
        remainder="${entry#*|}"
        [[ "$remainder" == "${expression}|${wanted}" ]] && return 0
    done
    return 1
}

parse_state_file() {
    local file="$1" line key value action remainder expression wanted
    local version_count=0 status_count=0 module_count=0 ingress_count=0 api_count=0 boolean_count=0
    reset_loaded_state
    while IFS= read -r line || [[ -n "$line" ]]; do
        [[ "$line" == *=* ]] || die "invalid state line: $line"
        key="${line%%=*}"
        value="${line#*=}"
        case "$key" in
            STATE_VERSION)
                version_count=$((version_count + 1))
                [[ "$value" == "$STATE_VERSION" ]] || die "unsupported state version: $value"
                ;;
            STATUS)
                status_count=$((status_count + 1))
                [[ "$value" == installing || "$value" == complete ]] || die "invalid state status: $value"
                STATE_STATUS="$value"
                ;;
            MODULE)
                module_count=$((module_count + 1))
                case "$value" in
                    "owned|${MODULE_NAME}|${MODULE_VERSION}") STATE_MODULE_ACTION=owned ;;
                    "none|${MODULE_NAME}|${MODULE_VERSION}") STATE_MODULE_ACTION=none ;;
                    *) die "invalid module state: $value" ;;
                esac
                ;;
            INGRESS)
                ingress_count=$((ingress_count + 1))
                case "$value" in
                    "owned|tcp|${INGRESS_PORT}|${INGRESS_PORT_TYPE}") STATE_INGRESS_ACTION=owned ;;
                    "preexisting|tcp|${INGRESS_PORT}|http_port_t") STATE_INGRESS_ACTION=preexisting ;;
                    *) die "invalid ingress state: $value" ;;
                esac
                ;;
            API)
                api_count=$((api_count + 1))
                case "$value" in
                    "preexisting|tcp|${API_PORT}|http_cache_port_t") STATE_API_TYPE=http_cache_port_t ;;
                    "preexisting|tcp|${API_PORT}|http_port_t") STATE_API_TYPE=http_port_t ;;
                    *) die "invalid API port state: $value" ;;
                esac
                ;;
            BOOLEAN)
                boolean_count=$((boolean_count + 1))
                IFS='|' read -r action STATE_BOOLEAN_CHANGED STATE_BOOLEAN_OLD_ACTIVE \
                    STATE_BOOLEAN_OLD_PERSISTENT STATE_BOOLEAN_OLD_LOCAL \
                    STATE_BOOLEAN_INSTALLED_LOCAL remainder <<< "$value"
                [[ "$action" == "$RELAY_BOOLEAN" && -z "${remainder:-}" ]] ||
                    die "invalid Boolean state: $value"
                [[ "$STATE_BOOLEAN_CHANGED" == 0 || "$STATE_BOOLEAN_CHANGED" == 1 ]] ||
                    die "invalid Boolean ownership state: $value"
                [[ "$STATE_BOOLEAN_OLD_ACTIVE" == on || "$STATE_BOOLEAN_OLD_ACTIVE" == off ]] ||
                    die "invalid original active Boolean state: $value"
                [[ "$STATE_BOOLEAN_OLD_PERSISTENT" == on || "$STATE_BOOLEAN_OLD_PERSISTENT" == off ]] ||
                    die "invalid original persistent Boolean state: $value"
                [[ "$STATE_BOOLEAN_OLD_LOCAL" == none || "$STATE_BOOLEAN_OLD_LOCAL" == on ||
                   "$STATE_BOOLEAN_OLD_LOCAL" == off ]] || die "invalid original local Boolean state: $value"
                [[ "$STATE_BOOLEAN_INSTALLED_LOCAL" == pending ||
                   "$STATE_BOOLEAN_INSTALLED_LOCAL" == none ||
                   "$STATE_BOOLEAN_INSTALLED_LOCAL" == on ||
                   "$STATE_BOOLEAN_INSTALLED_LOCAL" == off ]] ||
                    die "invalid installed local Boolean state: $value"
                ;;
            FCONTEXT)
                # The reviewed expressions may contain regex alternation (`|`).
                # Split ownership at the first delimiter and the SELinux type at
                # the last delimiter; the exact known-rule check below validates
                # the complete expression rather than treating it as fields.
                action="${value%%|*}"
                remainder="${value#*|}"
                [[ "$action" != "$value" && "$remainder" == *'|'* ]] ||
                    die "invalid fcontext state: $value"
                expression="${remainder%|*}"
                wanted="${remainder##*|}"
                [[ "$action" == owned || "$action" == preexisting ]] || die "invalid fcontext ownership: $value"
                [[ -n "$expression" && -n "$wanted" ]] || die "invalid fcontext state: $value"
                is_known_fcontext_rule "$expression" "$wanted" || die "unknown fcontext state: $value"
                state_has_fcontext_rule "$expression" "$wanted" && die "duplicate fcontext state: $value"
                STATE_FCONTEXTS+=("$value")
                ;;
            *) die "unknown state key: $key" ;;
        esac
    done < "$file"

    [[ "$version_count" -eq 1 && "$status_count" -eq 1 && "$module_count" -eq 1 &&
       "$ingress_count" -eq 1 && "$api_count" -eq 1 && "$boolean_count" -eq 1 ]] ||
        die 'state singleton fields are missing or duplicated'
    [[ "${#STATE_FCONTEXTS[@]}" -eq "${#FCONTEXT_RULES[@]}" ]] ||
        die 'state does not describe every managed fcontext exactly once'
    if [[ "$STATE_INGRESS_ACTION" == owned ]]; then
        [[ "$STATE_MODULE_ACTION" == owned ]] || die 'owned ingress requires an owned module'
    else
        [[ "$STATE_MODULE_ACTION" == none ]] || die 'pre-existing ingress must not own a module'
    fi
    if [[ "$STATE_BOOLEAN_CHANGED" == 0 ]]; then
        [[ "$STATE_BOOLEAN_OLD_ACTIVE" == on && "$STATE_BOOLEAN_OLD_PERSISTENT" == on ]] ||
            die 'unchanged Boolean state must already be on persistently and actively'
    else
        [[ "$STATE_BOOLEAN_OLD_ACTIVE" == off || "$STATE_BOOLEAN_OLD_PERSISTENT" == off ]] ||
            die 'changed Boolean state must record an original off value'
    fi
    if [[ "$STATE_STATUS" == installing ]]; then
        [[ "$STATE_BOOLEAN_INSTALLED_LOCAL" == pending ]] ||
            die 'installing state must use a pending installed Boolean value'
    else
        [[ "$STATE_BOOLEAN_INSTALLED_LOCAL" != pending ]] ||
            die 'complete state must record the installed local Boolean value'
    fi
}

load_state() {
    assert_private_state_directory "$STATE_PARENT"
    assert_private_state_directory "$STATE_DIR"
    [[ -f "$STATE_FILE" && ! -L "$STATE_FILE" ]] || die "missing safe state file: $STATE_FILE"
    [[ "$(stat -c '%U:%G:%a' -- "$STATE_FILE")" == root:root:600 ]] ||
        die "$STATE_FILE must be root:root mode 0600"
    parse_state_file "$STATE_FILE"
}

write_state_file() {
    local status="$1" rule expression wanted current action entry temporary
    [[ "$status" == installing || "$status" == complete ]] || die "invalid state write status: $status"
    prepare_state_directory
    STATE_TEMP="$(mktemp "${STATE_DIR}/.nginx-management.state.XXXXXX")"
    temporary="$STATE_TEMP"
    {
        printf 'STATE_VERSION=%s\n' "$STATE_VERSION"
        printf 'STATUS=%s\n' "$status"
        if [[ "$status" == installing ]]; then
            if [[ "$PLAN_INGRESS_ACTION" == owned ]]; then
                printf 'MODULE=owned|%s|%s\n' "$MODULE_NAME" "$MODULE_VERSION"
                printf 'INGRESS=owned|tcp|%s|%s\n' "$INGRESS_PORT" "$INGRESS_PORT_TYPE"
            else
                printf 'MODULE=none|%s|%s\n' "$MODULE_NAME" "$MODULE_VERSION"
                printf 'INGRESS=preexisting|tcp|%s|http_port_t\n' "$INGRESS_PORT"
            fi
            printf 'API=preexisting|tcp|%s|%s\n' "$API_PORT" "$PLAN_API_TYPE"
            printf 'BOOLEAN=%s|%s|%s|%s|%s|pending\n' "$RELAY_BOOLEAN" "$PLAN_BOOLEAN_CHANGED" \
                "$PLAN_BOOLEAN_OLD_ACTIVE" "$PLAN_BOOLEAN_OLD_PERSISTENT" "$PLAN_BOOLEAN_OLD_LOCAL"
            for rule in "${FCONTEXT_RULES[@]}"; do
                expression="${rule%%|*}"
                wanted="${rule#*|}"
                current="$(single_fcontext_type_for "$expression")"
                case "$current" in
                    '') action=owned ;;
                    "$wanted") action=preexisting ;;
                    *) die "fcontext became occupied before state creation: $expression ($current)" ;;
                esac
                printf 'FCONTEXT=%s|%s|%s\n' "$action" "$expression" "$wanted"
            done
        else
            printf 'MODULE=%s|%s|%s\n' "$STATE_MODULE_ACTION" "$MODULE_NAME" "$MODULE_VERSION"
            if [[ "$STATE_INGRESS_ACTION" == owned ]]; then
                printf 'INGRESS=owned|tcp|%s|%s\n' "$INGRESS_PORT" "$INGRESS_PORT_TYPE"
            else
                printf 'INGRESS=preexisting|tcp|%s|http_port_t\n' "$INGRESS_PORT"
            fi
            printf 'API=preexisting|tcp|%s|%s\n' "$API_PORT" "$STATE_API_TYPE"
            printf 'BOOLEAN=%s|%s|%s|%s|%s|%s\n' "$RELAY_BOOLEAN" "$STATE_BOOLEAN_CHANGED" \
                "$STATE_BOOLEAN_OLD_ACTIVE" "$STATE_BOOLEAN_OLD_PERSISTENT" \
                "$STATE_BOOLEAN_OLD_LOCAL" "$STATE_BOOLEAN_INSTALLED_LOCAL"
            for entry in "${STATE_FCONTEXTS[@]}"; do
                printf 'FCONTEXT=%s\n' "$entry"
            done
        fi
    } > "$temporary"
    chown root:root "$temporary"
    chmod 0600 "$temporary"
    if [[ "$status" == installing ]]; then
        [[ ! -e "$STATE_FILE" && ! -L "$STATE_FILE" ]] || {
            cleanup_state_temp
            die "refusing to replace pre-existing state: $STATE_FILE"
        }
    else
        [[ -f "$STATE_FILE" && ! -L "$STATE_FILE" && "$STATE_STATUS" == installing ]] || {
            cleanup_state_temp
            die "cannot complete missing or non-installing state: $STATE_FILE"
        }
    fi
    mv -T -- "$temporary" "$STATE_FILE"
    STATE_TEMP=''
}

install_recorded_module() {
    local installed_version
    [[ "$STATE_MODULE_ACTION" == owned ]] || return 0
    compile_policy
    installed_version="$(installed_module_version)" || die "could not query module $MODULE_NAME"
    [[ -z "$installed_version" ]] || die "SELinux module $MODULE_NAME became occupied"
    semodule -i "$WORK_ROOT/${MODULE_NAME}.pp"
    cleanup_work_root
}

install_recorded_ingress() {
    local current
    current="$(single_port_type_for "$INGRESS_PORT")"
    if [[ "$STATE_INGRESS_ACTION" == owned ]]; then
        [[ -z "$current" ]] || die "TCP $INGRESS_PORT became occupied by $current"
        semanage port -a -t "$INGRESS_PORT_TYPE" -p tcp "$INGRESS_PORT"
    else
        [[ "$current" == http_port_t ]] || die "pre-existing TCP $INGRESS_PORT changed to ${current:-unmapped}"
    fi
}

verify_recorded_api_port() {
    local current
    current="$(single_port_type_for "$API_PORT")"
    [[ "$current" == "$STATE_API_TYPE" ]] ||
        die "TCP $API_PORT changed to ${current:-unmapped}; expected $STATE_API_TYPE"
}

install_recorded_fcontexts() {
    local entry action remainder expression wanted current
    for entry in "${STATE_FCONTEXTS[@]}"; do
        action="${entry%%|*}"
        remainder="${entry#*|}"
        expression="${remainder%%|*}"
        wanted="${remainder#*|}"
        current="$(single_fcontext_type_for "$expression")"
        if [[ "$action" == owned ]]; then
            [[ -z "$current" ]] || die "fcontext became occupied during installation: $expression"
            semanage fcontext -a -t "$wanted" "$expression"
        else
            [[ "$current" == "$wanted" ]] || die "pre-existing fcontext changed: $expression"
        fi
    done
}

enable_recorded_boolean() {
    local active persistent local_value
    active="$(active_boolean_value)"
    persistent="$(persistent_boolean_value)"
    local_value="$(local_boolean_value)"
    [[ "$active" == "$STATE_BOOLEAN_OLD_ACTIVE" &&
       "$persistent" == "$STATE_BOOLEAN_OLD_PERSISTENT" &&
       "$local_value" == "$STATE_BOOLEAN_OLD_LOCAL" ]] ||
        die "$RELAY_BOOLEAN changed after preflight"
    if [[ "$STATE_BOOLEAN_CHANGED" == 1 ]]; then
        setsebool -P "$RELAY_BOOLEAN" on
    fi
    [[ "$(active_boolean_value)" == on && "$(persistent_boolean_value)" == on ]] ||
        die "$RELAY_BOOLEAN did not become persistently active"
    STATE_BOOLEAN_INSTALLED_LOCAL="$(local_boolean_value)"
}

assert_real_directory_if_present() {
    local path="$1"
    if [[ -e "$path" || -L "$path" ]]; then
        [[ -d "$path" && ! -L "$path" ]] || die "$path must be a real directory when present"
    fi
}

restore_if_present() {
    local path="$1"
    if [[ -e "$path" || -L "$path" ]]; then
        restorecon "$path"
    fi
}

restore_tree_if_present() {
    local path="$1"
    if [[ -d "$path" && ! -L "$path" ]]; then
        restorecon -R "$path"
    fi
}

restore_probe_contexts() {
    local release_dir find_pid failures=0
    assert_real_directory_if_present /srv/probe || failures=1
    assert_real_directory_if_present /srv/probe/releases || failures=1
    assert_real_directory_if_present /srv/probe/config || failures=1
    assert_real_directory_if_present /srv/probe/config/nginx || failures=1
    assert_real_directory_if_present /srv/probe/api || failures=1
    assert_real_directory_if_present /srv/probe/api/scripts || failures=1
    assert_real_directory_if_present /etc/probe-panel || failures=1
    assert_real_directory_if_present /etc/probe-panel/tls || failures=1
    assert_real_directory_if_present /etc/probe-panel/tls/admin || failures=1
    assert_real_directory_if_present /etc/probe-panel/tls/private-ca || failures=1

    ((failures == 0)) || return 1
    restore_if_present /srv/probe || failures=1
    restore_if_present /srv/probe/admin || failures=1
    restore_if_present /srv/probe/releases || failures=1
    if [[ -d /srv/probe/releases && ! -L /srv/probe/releases ]]; then
        while IFS= read -r -d '' release_dir; do
            restore_if_present "$release_dir" || failures=1
            restore_tree_if_present "$release_dir/admin" || failures=1
            restore_if_present "$release_dir/artifacts/api/probe-api" || failures=1
            restore_if_present "$release_dir/source/probe-api/deploy/scripts/install-release.sh" || failures=1
            restore_if_present "$release_dir/api/probe-api" || failures=1
        done < <(find /srv/probe/releases -mindepth 1 -maxdepth 1 -type d -print0)
        find_pid=$!
        wait "$find_pid" || failures=1
    fi
    restore_if_present /srv/probe/config || failures=1
    restore_tree_if_present /srv/probe/config/nginx || failures=1
    restore_if_present /srv/probe/api/scripts/backup-postgres.sh || failures=1
    restore_if_present /srv/probe/api/scripts/restore-postgres.sh || failures=1
    restore_if_present /usr/local/lib/probe-panel/probe-setup || failures=1
    restore_if_present /etc/probe-panel/admin-allowlist.geo || failures=1
    restore_if_present /etc/probe-panel/tls || failures=1
    restore_tree_if_present /etc/probe-panel/tls/admin || failures=1
    restore_tree_if_present /etc/probe-panel/tls/private-ca || failures=1
    restore_if_present /etc/nginx/conf.d/probe-panel.conf || failures=1
    restore_if_present /etc/systemd/system/probe-api.service || failures=1
    restore_if_present /etc/systemd/system/probe-postgres-backup.service || failures=1
    restore_if_present /etc/systemd/system/probe-postgres-backup.timer || failures=1
    ((failures == 0))
}

verify_recorded_installation() {
    local entry action remainder expression wanted current installed_version
    load_state
    [[ "$STATE_STATUS" == complete ]] || die "SELinux installation is not complete: $STATE_STATUS"

    installed_version="$(installed_module_version)" || die "could not query module $MODULE_NAME"
    if [[ "$STATE_MODULE_ACTION" == owned ]]; then
        [[ "$installed_version" == "$MODULE_VERSION" ]] ||
            die "recorded module has version ${installed_version:-missing}, expected $MODULE_VERSION"
    else
        [[ -z "$installed_version" ]] || die "unexpected module $MODULE_NAME is installed"
    fi

    current="$(single_port_type_for "$INGRESS_PORT")"
    if [[ "$STATE_INGRESS_ACTION" == owned ]]; then
        [[ "$(single_port_type_for "$INGRESS_PORT" local)" == "$INGRESS_PORT_TYPE" &&
           "$current" == "$INGRESS_PORT_TYPE" ]] || die "owned TCP $INGRESS_PORT mapping changed"
    else
        [[ "$current" == http_port_t ]] || die "pre-existing TCP $INGRESS_PORT mapping changed"
    fi
    verify_recorded_api_port

    for entry in "${STATE_FCONTEXTS[@]}"; do
        action="${entry%%|*}"
        remainder="${entry#*|}"
        expression="${remainder%%|*}"
        wanted="${remainder#*|}"
        current="$(single_fcontext_type_for "$expression")"
        [[ "$current" == "$wanted" ]] || die "recorded fcontext changed: $expression"
        if [[ "$action" == owned ]]; then
            local_fcontext_rule_matches "$expression" "$wanted" ||
                die "owned local fcontext changed: $expression"
        fi
    done
    [[ "$(active_boolean_value)" == on && "$(persistent_boolean_value)" == on ]] ||
        die "$RELAY_BOOLEAN is no longer persistently active"
    if [[ "$STATE_BOOLEAN_CHANGED" == 1 ]]; then
        [[ "$(local_boolean_value)" == "$STATE_BOOLEAN_INSTALLED_LOCAL" ]] ||
            die "$RELAY_BOOLEAN local persistent customization changed"
    fi
}

restore_recorded_boolean() {
    local active persistent local_value
    [[ "$STATE_BOOLEAN_CHANGED" == 1 ]] || return 0
    active="$(active_boolean_value 2>/dev/null)" || return 1
    persistent="$(persistent_boolean_value 2>/dev/null)" || return 1
    local_value="$(local_boolean_value 2>/dev/null)" || return 1
    if [[ "$active" == "$STATE_BOOLEAN_OLD_ACTIVE" &&
          "$persistent" == "$STATE_BOOLEAN_OLD_PERSISTENT" ]]; then
        if [[ "$STATE_BOOLEAN_OLD_LOCAL" != none &&
              "$local_value" != "$STATE_BOOLEAN_OLD_LOCAL" ]]; then
            log "leaving changed local Boolean $RELAY_BOOLEAN ($local_value)"
            return 1
        fi
        return 0
    fi
    if [[ "$STATE_BOOLEAN_INSTALLED_LOCAL" != pending &&
          "$local_value" != "$STATE_BOOLEAN_INSTALLED_LOCAL" ]]; then
        log "leaving changed local Boolean $RELAY_BOOLEAN ($local_value)"
        return 1
    fi
    if [[ "$active" == on && "$persistent" == on ]]; then
        setsebool -P "$RELAY_BOOLEAN" "$STATE_BOOLEAN_OLD_PERSISTENT" || return 1
        if [[ "$STATE_BOOLEAN_OLD_ACTIVE" != "$STATE_BOOLEAN_OLD_PERSISTENT" ]]; then
            setsebool "$RELAY_BOOLEAN" "$STATE_BOOLEAN_OLD_ACTIVE" || return 1
        fi
        [[ "$(active_boolean_value 2>/dev/null)" == "$STATE_BOOLEAN_OLD_ACTIVE" &&
           "$(persistent_boolean_value 2>/dev/null)" == "$STATE_BOOLEAN_OLD_PERSISTENT" ]] || return 1
        if [[ "$STATE_BOOLEAN_OLD_LOCAL" == none ]]; then
            local_value="$(local_boolean_value 2>/dev/null)" || return 1
            if [[ "$local_value" != none ]]; then
                log "$RELAY_BOOLEAN behavior was restored; its per-Boolean persistent override is retained because deleting all local Boolean customizations is unsafe"
            fi
        elif [[ "$(local_boolean_value 2>/dev/null)" != "$STATE_BOOLEAN_OLD_LOCAL" ]]; then
            return 1
        fi
        return 0
    fi
    log "leaving changed Boolean $RELAY_BOOLEAN (active=$active persistent=$persistent)"
    return 1
}

rollback_recorded_changes() {
    local tolerate_missing="${1:-false}" entry action remainder expression wanted current
    local index failures=0 installed_version ingress_clear=true
    load_state
    set +e

    restore_recorded_boolean || failures=1

    for ((index=${#STATE_FCONTEXTS[@]} - 1; index>=0; index--)); do
        entry="${STATE_FCONTEXTS[index]}"
        action="${entry%%|*}"
        [[ "$action" == owned ]] || continue
        remainder="${entry#*|}"
        expression="${remainder%%|*}"
        wanted="${remainder#*|}"
        if ! current="$(single_fcontext_type_for "$expression" local 2>/dev/null)"; then
            log "could not query local fcontext during rollback: $expression"
            failures=1
            continue
        fi
        if local_fcontext_rule_matches "$expression" "$wanted" 2>/dev/null; then
            semanage fcontext -d "$expression" || failures=1
        elif [[ -n "$current" ]]; then
            log "leaving changed fcontext $expression ($current)"
            failures=1
        elif [[ "$tolerate_missing" != true ]]; then
            log "recorded fcontext is already absent: $expression"
        fi
    done
    restore_probe_contexts || failures=1

    if [[ "$STATE_INGRESS_ACTION" == owned ]]; then
        if ! current="$(single_port_type_for "$INGRESS_PORT" local 2>/dev/null)"; then
            log "could not query local TCP port mapping during rollback: $INGRESS_PORT"
            failures=1
            ingress_clear=false
        elif [[ "$current" == "$INGRESS_PORT_TYPE" ]]; then
            if ! semanage port -d -p tcp "$INGRESS_PORT"; then
                failures=1
                ingress_clear=false
            fi
        elif [[ -n "$current" ]]; then
            log "leaving changed TCP port mapping $INGRESS_PORT ($current)"
            failures=1
            ingress_clear=false
        elif [[ "$tolerate_missing" != true ]]; then
            log "recorded TCP port mapping is already absent: $INGRESS_PORT"
        fi
    fi

    if [[ "$STATE_MODULE_ACTION" == owned ]]; then
        if [[ "$ingress_clear" != true ]]; then
            log "leaving module $MODULE_NAME because its owned port mapping was not cleared"
            failures=1
        elif ! installed_version="$(installed_module_version)"; then
            log "could not query module $MODULE_NAME during rollback"
            failures=1
        elif [[ "$installed_version" == "$MODULE_VERSION" ]]; then
            semodule -r "$MODULE_NAME" || failures=1
        elif [[ -n "$installed_version" ]]; then
            log "leaving changed SELinux module $MODULE_NAME (version $installed_version)"
            failures=1
        elif [[ "$tolerate_missing" != true ]]; then
            log "recorded SELinux module is already absent: $MODULE_NAME"
        fi
    fi

    if ((failures == 0)); then
        rm -f -- "$STATE_FILE" || failures=1
    fi
    if ((failures == 0)); then
        rmdir --ignore-fail-on-non-empty "$STATE_DIR" 2>/dev/null || true
    fi
    set -e
    ((failures == 0)) || die 'rollback was incomplete; state was retained for manual review'
}

on_install_error() {
    # ERR traps enter this function with the failed command status in `$?`;
    # expanding it in the first command preserves that status for rollback.
    # shellcheck disable=SC2319
    local caught_status=$? status was_transaction="${INSTALL_TRANSACTION:-false}"
    status="${1:-$caught_status}"
    trap - ERR
    cleanup_work_root
    cleanup_state_temp
    INSTALL_TRANSACTION=false
    if [[ "$was_transaction" == true && -f "$STATE_FILE" && ! -L "$STATE_FILE" ]]; then
        log 'installation failed; rolling back recorded changes'
        rollback_recorded_changes true || true
    fi
    exit "$status"
}

run_preflight() {
    require_root
    require_selinux_tools
    acquire_selinux_lock
    check_fresh_install_preconditions
    log 'preflight passed without claiming or relabeling TCP 8080'
}

run_install() {
    require_root
    require_selinux_tools
    acquire_selinux_lock
    if [[ -f "$STATE_FILE" && ! -L "$STATE_FILE" ]]; then
        verify_recorded_installation
        restore_probe_contexts
        log 'recorded SELinux installation is complete; labels were refreshed'
        return
    fi

    check_fresh_install_preconditions
    INSTALL_TRANSACTION=true
    # The function name is the ERR trap action and is invoked by Bash.
    # shellcheck disable=SC2319
    trap on_install_error ERR
    write_state_file installing
    load_state
    install_recorded_module
    install_recorded_ingress
    verify_recorded_api_port
    install_recorded_fcontexts
    enable_recorded_boolean
    restore_probe_contexts
    write_state_file complete
    verify_recorded_installation
    INSTALL_TRANSACTION=false
    trap - ERR
    log 'minimal management Nginx SELinux candidate installed'
}

run_status() {
    require_root
    require_selinux_tools
    acquire_selinux_lock
    verify_recorded_installation
    log 'recorded SELinux installation is complete and consistent'
}

run_refresh() {
    require_root
    require_selinux_tools
    acquire_selinux_lock
    verify_recorded_installation
    restore_probe_contexts
    log 'recorded SELinux installation is consistent; managed labels were refreshed'
}

run_rollback() {
    require_root
    require_selinux_tools
    acquire_selinux_lock
    rollback_recorded_changes false
    log 'unchanged Probe-owned SELinux rules were removed; pre-existing rules were preserved'
}

main() {
    (($# == 1)) || { usage >&2; exit 2; }
    trap 'cleanup_state_temp; cleanup_work_root' EXIT
    trap 'exit 130' HUP INT TERM
    case "$1" in
        preflight) run_preflight ;;
        install) run_install ;;
        status) run_status ;;
        refresh) run_refresh ;;
        rollback) run_rollback ;;
        -h|--help|help) usage ;;
        *) usage >&2; die "unknown command: $1" ;;
    esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
