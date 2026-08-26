#!/usr/bin/env bash

# Build the immutable Probe Panel server release on Debian 13. This script only
# creates local release assets; it never creates, edits, or uploads a GitHub
# release.

set -Eeuo pipefail
umask 077
export LC_ALL=C
export TZ=UTC
export SOURCE_DATE_EPOCH=1577836800

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
SUPER_MY_ROOT="$(readlink -f -- "${SCRIPT_DIR}/../../..")"
readonly SUPER_MY_ROOT
readonly MANAGEMENT_VERSION="v1.2.0"
# Historical immutable-tag proof retained as a source contract; v1.2 builds management only.
# shellcheck disable=SC2034
readonly FULL_VERSION="v1.1.0"
readonly MANAGEMENT_SUPER_MY_REF="refs/tags/v1.2.0"
readonly MANAGEMENT_RUNTIME_ABI="probe-linux-systemd-v2"
readonly MANAGEMENT_PLATFORM_IDS="debian-9-systemd,debian-10-systemd,debian-11-systemd,debian-12-systemd,debian-13-systemd,ubuntu-18.04-systemd,ubuntu-20.04-systemd,ubuntu-22.04-systemd,ubuntu-24.04-systemd,ubuntu-26.04-systemd,centos-linux-7-systemd,centos-linux-8-systemd,centos-stream-8-systemd,centos-stream-9-systemd,centos-stream-10-systemd"
# Historical immutable-tag proof retained as a source contract; v1.2 builds management only.
# shellcheck disable=SC2034
readonly FULL_SUPER_MY_REF="refs/tags/v1.1.0"
readonly WEB_REF="refs/tags/v1.0.0"
readonly AGENT_REF="refs/tags/v1.0.2"
readonly SOURCE_REPOSITORY="Kcmose/super-my"
readonly SUPER_MY_REMOTE="https://github.com/${SOURCE_REPOSITORY}.git"
readonly WEB_REMOTE="https://github.com/Kcmose/my.git"
readonly AGENT_REMOTE="https://github.com/Kcmose/my-agent.git"

ADMIN_ROOT="$SUPER_MY_ROOT"
WEB_ROOT="${SUPER_MY_ROOT}/../my"
AGENT_ROOT="${SUPER_MY_ROOT}/../my-agent"
OUTPUT_DIR=""
VERSION=""
PROFILE="management"
SUPER_MY_REF=""
SOURCE_COMMIT=""
SOURCE_TAG_OBJECT=""
WEB_SOURCE_COMMIT=""
WEB_SOURCE_TAG_OBJECT=""
AGENT_SOURCE_COMMIT=""
AGENT_SOURCE_TAG_OBJECT=""
OUTPUT_DIR_SET=false
WORK_ROOT=""
SOURCE_INPUT_DIGEST=""
PRISTINE_SOURCE_DIGEST=""
WEB_INPUT_DIGEST=""
AGENT_INPUT_DIGEST=""

usage() {
    cat <<'EOF'
Usage: build-release-bundles.sh [options]

Build both Linux architecture bundles on Debian 13 without uploading anything.

Options:
  --output-dir DIR   new directory that receives both tarballs and SHA256SUMS
  --profile PROFILE  release profile; v1.2 accepts management only
  --version VERSION  pinned management version; currently v1.2.0
  -h, --help         show this help

Sources are fixed to the Kcmose/super-my repository containing probe-admin and
probe-api. The repository must be clean and exactly at the pinned v1.2.0 tag,
and the remote tag must resolve to the same HEAD before any release manifest is
written. Historical full releases are built only by their immutable old tags.
EOF
}

log() {
    printf '[probe-release] %s\n' "$*" >&2
}

die() {
    printf '[probe-release] ERROR: %s\n' "$*" >&2
    exit 1
}

cleanup() {
    local status=$?
    trap - EXIT HUP INT TERM
    if [[ -n "$WORK_ROOT" && -d "$WORK_ROOT" && ! -L "$WORK_ROOT" ]]; then
        case "$WORK_ROOT" in
            */.probe-release-build.*) rm -rf -- "$WORK_ROOT" ;;
            *) printf '[probe-release] WARNING: refusing to clean unexpected path: %s\n' "$WORK_ROOT" >&2 ;;
        esac
    fi
    exit "$status"
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "required command is missing: $1"
}

trusted_git() (
    unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_OBJECT_DIRECTORY
    unset GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_REPLACE_REF_BASE GIT_NAMESPACE
    unset GIT_CONFIG GIT_CONFIG_SYSTEM GIT_CONFIG_GLOBAL GIT_CONFIG_NOSYSTEM
    unset GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS GIT_ATTR_NOSYSTEM
    export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_ATTR_NOSYSTEM=1
    export GIT_NO_REPLACE_OBJECTS=1
    command git --no-replace-objects "$@"
)

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
        die "release bundles must be built on Debian 13 (found ${os_id:-unknown} ${version_id:-unknown})"
}

take_value() {
    local option="$1" value="${2-}"
    [[ -n "$value" ]] || die "$option requires a non-empty value"
    printf '%s\n' "$value"
}

parse_arguments() {
    while (($# > 0)); do
        case "$1" in
            --output-dir)
                OUTPUT_DIR="$(take_value "$1" "${2-}")"
                OUTPUT_DIR_SET=true
                shift 2
                ;;
            --output-dir=*) OUTPUT_DIR="$(take_value --output-dir "${1#*=}")"; OUTPUT_DIR_SET=true; shift ;;
            --profile)
                PROFILE="$(take_value "$1" "${2-}")"
                shift 2
                ;;
            --profile=*) PROFILE="$(take_value --profile "${1#*=}")"; shift ;;
            --version)
                VERSION="$(take_value "$1" "${2-}")"
                shift 2
                ;;
            --version=*) VERSION="$(take_value --version "${1#*=}")"; shift ;;
            -h|--help)
                usage
                exit 0
                ;;
            --) shift; (($# == 0)) || die 'positional arguments are not accepted' ;;
            -*) die "unknown option: $1" ;;
            *) die "unexpected positional argument: $1" ;;
        esac
    done
}

validate_profile() {
    [[ "$PROFILE" == management ]] ||
        die 'the v1.2 release builder accepts management only; use an immutable historical tag to rebuild full'
    [[ -z "$VERSION" || "$VERSION" == "$MANAGEMENT_VERSION" ]] ||
        die "the management profile is pinned to the unreleased $MANAGEMENT_VERSION source contract"
    VERSION="$MANAGEMENT_VERSION"
    SUPER_MY_REF="$MANAGEMENT_SUPER_MY_REF"
    if [[ "$OUTPUT_DIR_SET" == false ]]; then
        OUTPUT_DIR="${SUPER_MY_ROOT}/../probe-panel-management-release-${VERSION}"
    fi
}

canonical_source_directory() {
    local label="$1" candidate="$2" resolved
    [[ -d "$candidate" && ! -L "$candidate" ]] || die "$label is not a real directory: $candidate"
    resolved="$(readlink -f -- "$candidate")"
    [[ -n "$resolved" && "$resolved" == /* ]] || die "could not resolve $label: $candidate"
    printf '%s\n' "$resolved"
}

assert_fixed_repository() {
    local label="$1" root="$2" expected_remote="$3" ref="$4" commit_output="$5" tag_output="$6"
    local top_level origin_urls status_output head tag_object tag_commit

    [[ "$commit_output" =~ ^[A-Z][A-Z0-9_]*$ ]] ||
        die "$label verified commit output variable is invalid"
    [[ "$tag_output" =~ ^[A-Z][A-Z0-9_]*$ ]] ||
        die "$label verified tag output variable is invalid"

    top_level="$(trusted_git -C "$root" rev-parse --show-toplevel 2>/dev/null)" ||
        die "$label is not a Git repository: $root"
    top_level="$(readlink -f -- "$top_level")"
    [[ "$top_level" == "$root" ]] ||
        die "$label must be the fixed repository root: $root"

    origin_urls="$(trusted_git -C "$root" remote get-url --all origin 2>/dev/null)" ||
        die "$label does not have the required origin remote"
    [[ "$origin_urls" == "$expected_remote" ]] ||
        die "$label origin must be exactly $expected_remote"

    status_output="$(
        trusted_git -c core.fsmonitor=false -c core.untrackedCache=false \
            -C "$root" status --porcelain=v1 --untracked-files=all
    )" ||
        die "$label working tree status could not be verified"
    [[ -z "$status_output" ]] ||
        die "$label working tree is not clean, including untracked files"

    head="$(trusted_git -C "$root" rev-parse --verify 'HEAD^{commit}' 2>/dev/null)" ||
        die "$label HEAD commit could not be verified"
    tag_object="$(trusted_git -C "$root" rev-parse --verify "$ref" 2>/dev/null)" ||
        die "$label is missing the pinned local tag: $ref"
    tag_commit="$(trusted_git -C "$root" rev-parse --verify "${ref}^{commit}" 2>/dev/null)" ||
        die "$label is missing the pinned local tag: $ref"
    [[ "$head" == "$tag_commit" ]] ||
        die "$label HEAD does not exactly equal the pinned local tag commit: $ref"

    assert_remote_tag_binding "$label" "$expected_remote" "$ref" "$tag_object" "$head"
    printf -v "$commit_output" '%s' "$head"
    printf -v "$tag_output" '%s' "$tag_object"
}

assert_remote_tag_binding() {
    local label="$1" expected_remote="$2" ref="$3" expected_tag_object="$4" expected_commit="$5"
    local remote_tags remote_oid remote_ref remote_direct='' remote_peeled='' remote_commit

    [[ "$expected_tag_object" =~ ^[0-9a-f]{40}$ ]] ||
        die "$label expected tag object is malformed"
    [[ "$expected_commit" =~ ^[0-9a-f]{40}$ ]] ||
        die "$label expected tag commit is malformed"
    remote_tags="$(
        trusted_git -C / ls-remote --exit-code "$expected_remote" "$ref" "${ref}^{}" 2>/dev/null
    )" || die "$label pinned tag could not be revalidated at $expected_remote"
    while IFS=$'\t' read -r remote_oid remote_ref; do
        [[ "$remote_oid" =~ ^[0-9a-f]{40}$ ]] ||
            die "$label remote tag returned a malformed object ID: $ref"
        case "$remote_ref" in
            "$ref")
                [[ -z "$remote_direct" ]] || die "$label remote tag is ambiguous: $ref"
                remote_direct="$remote_oid"
                ;;
            "${ref}^{}")
                [[ -z "$remote_peeled" ]] || die "$label remote peeled tag is ambiguous: $ref"
                remote_peeled="$remote_oid"
                ;;
            *) die "$label remote tag lookup returned an unexpected ref: $remote_ref" ;;
        esac
    done <<< "$remote_tags"
    [[ -n "$remote_direct" && "$remote_direct" == "$expected_tag_object" ]] ||
        die "$label remote tag object does not exactly equal the pinned local tag: $ref"
    remote_commit="$remote_direct"
    [[ -z "$remote_peeled" ]] || remote_commit="$remote_peeled"
    [[ "$remote_commit" == "$expected_commit" ]] ||
        die "$label remote tag does not exactly equal the verified commit: $ref"
}

revalidate_remote_source_refs() {
    assert_remote_tag_binding super-my "$SUPER_MY_REMOTE" "$SUPER_MY_REF" \
        "$SOURCE_TAG_OBJECT" "$SOURCE_COMMIT"
    if [[ "$PROFILE" == full ]]; then
        assert_remote_tag_binding my "$WEB_REMOTE" "$WEB_REF" \
            "$WEB_SOURCE_TAG_OBJECT" "$WEB_SOURCE_COMMIT"
        assert_remote_tag_binding my-agent "$AGENT_REMOTE" "$AGENT_REF" \
            "$AGENT_SOURCE_TAG_OBJECT" "$AGENT_SOURCE_COMMIT"
    fi
}

validate_fixed_repository_sources() {
    [[ "$ADMIN_ROOT" == "$SUPER_MY_ROOT" ]] ||
        die 'probe-admin and probe-api must come from the same fixed super-my repository'
    assert_fixed_repository super-my "$SUPER_MY_ROOT" "$SUPER_MY_REMOTE" "$SUPER_MY_REF" \
        SOURCE_COMMIT SOURCE_TAG_OBJECT
    if [[ "$PROFILE" == full ]]; then
        local expected_web expected_agent
        expected_web="$(readlink -f -- "${SUPER_MY_ROOT}/../my")"
        expected_agent="$(readlink -f -- "${SUPER_MY_ROOT}/../my-agent")"
        [[ "$WEB_ROOT" == "$expected_web" ]] || die "probe-web must use the fixed repository root: $expected_web"
        [[ "$AGENT_ROOT" == "$expected_agent" ]] || die "probe-agent must use the fixed repository root: $expected_agent"
        assert_fixed_repository my "$WEB_ROOT" "$WEB_REMOTE" "$WEB_REF" \
            WEB_SOURCE_COMMIT WEB_SOURCE_TAG_OBJECT
        assert_fixed_repository my-agent "$AGENT_ROOT" "$AGENT_REMOTE" "$AGENT_REF" \
            AGENT_SOURCE_COMMIT AGENT_SOURCE_TAG_OBJECT
    fi
}

assert_regular_file() {
    local path="$1"
    [[ -f "$path" && ! -L "$path" ]] || die "required regular file is missing: $path"
}

assert_clean_source_tree() {
    local label="$1" root="$2" found generated
    found="$(find "$root" -path "$root/.git" -prune -o -type l -print -quit)"
    [[ -z "$found" ]] || die "$label contains a symbolic link: $found"
    found="$(find "$root" -path "$root/.git" -prune -o -type f -name '*.map' -print -quit)"
    [[ -z "$found" ]] || die "$label contains a forbidden source map: $found"
    for generated in node_modules dist build coverage; do
        [[ ! -e "$root/$generated" && ! -L "$root/$generated" ]] ||
            die "$label contains generated source pollution: $root/$generated"
    done
}

source_input_digest() (
    local root="$1" path mode content_hash hash_line kind
    cd "$root"
    find . \
        \( -path ./node_modules -o -path ./dist -o -path ./build -o -path ./coverage \) -prune -o \
        \( -type d -o -type f \) -print0 |
        LC_ALL=C sort -z |
        while IFS= read -r -d '' path; do
            mode="$(stat -c '%a' -- "$path")" || exit 1
            if [[ -d "$path" ]]; then
                kind=directory
                content_hash=-
            else
                hash_line="$(sha256sum -- "$path")" || exit 1
                kind='file'
                content_hash="${hash_line%% *}"
                content_hash="${content_hash#\\}"
            fi
            printf '%s\0%s\0%s\0%s\0' "$kind" "$mode" "$content_hash" "$path"
        done |
        sha256sum |
        awk '{ print $1 }'
)

assert_source_inputs_unchanged() {
    local label="$1" root="$2" expected_digest="$3" actual_digest found
    [[ "$expected_digest" =~ ^[0-9a-f]{64}$ ]] ||
        die "$label expected source input digest is malformed"
    found="$(
        find "$root" \
            \( -path "$root/node_modules" -o -path "$root/dist" -o \
               -path "$root/build" -o -path "$root/coverage" \) -prune -o \
            \( -type l -o -type f -name '*.map' \) -print -quit
    )"
    [[ -z "$found" ]] || die "$label source inputs gained a forbidden link or source map: $found"
    actual_digest="$(source_input_digest "$root")" ||
        die "$label source input digest could not be recomputed"
    [[ "$actual_digest" == "$expected_digest" ]] ||
        die "$label source inputs changed after verified snapshot validation"
}

revalidate_all_source_inputs() {
    assert_source_inputs_unchanged super-my-work "$1" "$SOURCE_INPUT_DIGEST"
    assert_source_inputs_unchanged super-my-pristine "$2" "$PRISTINE_SOURCE_DIGEST"
    if [[ "$PROFILE" == full ]]; then
        assert_source_inputs_unchanged my-work "$3" "$WEB_INPUT_DIGEST"
        assert_source_inputs_unchanged my-agent-work "$4" "$AGENT_INPUT_DIGEST"
    fi
}

validate_sources() {
    local super_root="$1" api_root="$2" admin_root="$3"
    local web_root="$4" agent_root="$5" profile="$6" required
    for required in \
        go.mod go.sum cmd/probe-api/main.go cmd/probe-setup/main.go \
        cmd/probe-support-gate/main.go \
        internal/supportevidence/ledger.go internal/supportevidence/ledger_test.go \
        config/probe-api.env.example config/probe-postgres-backup.env.example \
        deploy/support/policy-v1.json deploy/support/releases/v1.2.0.json \
        deploy/scripts/deploy-common.sh deploy/scripts/deploy-common-management-runtime.sh \
        deploy/scripts/build-release-bundles.sh \
        deploy/scripts/install-release.sh deploy/scripts/validate-management.sh \
        deploy/scripts/restore-management.sh deploy/scripts/uninstall-management.sh \
        deploy/scripts/backup-postgres.sh \
        deploy/scripts/restore-postgres.sh \
        deploy/systemd/probe-api.service deploy/systemd/probe-postgres-backup.service \
        deploy/systemd/probe-postgres-backup.timer deploy/systemd/probe-api-legacy.service \
        deploy/systemd/probe-postgres-backup-legacy.service deploy/systemd/probe-postgres-backup-legacy.timer \
        deploy/setup/probe-panel-setup.service deploy/setup/probe-panel-setup.socket \
        deploy/setup/probe-panel-setup-legacy.service deploy/setup/probe-panel-setup-legacy.socket \
        deploy/setup/probe-panel-finalizer.path migrations/000001_initial.up.sql; do
        assert_regular_file "$api_root/$required"
    done
    for required in \
        install.sh install/common.sh install/build-standalone.sh \
        install/platforms/debian.sh install/platforms/ubuntu.sh install/platforms/centos.sh; do
        assert_regular_file "$super_root/$required"
    done
    bash "$super_root/install/build-standalone.sh" --check
    assert_regular_file "$super_root/.github/workflows/release.yml"

    for required in package.json package-lock.json index.html vite.config.js; do
        assert_regular_file "$admin_root/$required"
    done

    assert_clean_source_tree probe-api "$api_root"
    assert_clean_source_tree probe-admin "$admin_root"
    [[ ! -e "$api_root/probe-api" && ! -L "$api_root/probe-api" ]] ||
        die "$api_root/probe-api is a generated API binary"
    if [[ "$profile" == full ]]; then
        for required in deploy/nginx/nginx.conf deploy/nginx/nginx-ip.conf \
            deploy/setup/probe-panel-finalizer.service; do
            assert_regular_file "$api_root/$required"
        done
        for required in package.json package-lock.json index.html vite.config.js; do
            assert_regular_file "$web_root/$required"
        done
        for required in go.mod cmd/probe-agent/main.go deploy/install.sh \
            deploy/tests/install-contract.sh deploy/systemd/probe-agent.service; do
            assert_regular_file "$agent_root/$required"
        done
        assert_clean_source_tree probe-web "$web_root"
        assert_clean_source_tree probe-agent "$agent_root"
        [[ ! -e "$agent_root/probe-agent" && ! -L "$agent_root/probe-agent" ]] ||
            die "$agent_root/probe-agent is a generated Agent binary"
    else
        for required in deploy/nginx/nginx-management.conf deploy/nginx/nginx-management-ip.conf \
            deploy/nginx/nginx-management-legacy.conf deploy/nginx/nginx-management-ip-legacy.conf \
            deploy/nginx/nginx-management-classic.conf deploy/nginx/nginx-management-ip-classic.conf \
            deploy/setup/probe-panel-finalizer-management.service \
            deploy/setup/probe-panel-finalizer-management-legacy.service; do
            assert_regular_file "$api_root/$required"
        done
    fi
}

assert_output_outside_sources() {
    local output="$1" source_root
    for source_root in "$SUPER_MY_ROOT" "$ADMIN_ROOT" "$WEB_ROOT" "$AGENT_ROOT"; do
        [[ -n "$source_root" ]] || continue
        case "$output" in
            "$source_root"|"$source_root"/*) die "output directory must be outside source trees: $output" ;;
        esac
    done
}

publish_output_directory() {
    local source="$1" destination="$2"
    [[ -d "$source" && ! -L "$source" ]] ||
        die "release publish source is not a real directory: $source"
    # renameat2 with RENAME_NOREPLACE makes the existence check and directory
    # rename one kernel operation.  Never fall back to mv(1): older coreutils
    # implements --no-clobber as a check followed by rename, which can overwrite
    # a destination created inside that race window.
    if ! python3 -I -S - "$source" "$destination" <<'PY'; then
import ctypes
import os
import sys

AT_FDCWD = -100
RENAME_NOREPLACE = 1

libc = ctypes.CDLL(None, use_errno=True)
renameat2 = getattr(libc, "renameat2", None)
if renameat2 is None:
    print("[probe-release] renameat2 is unavailable; refusing a non-atomic release publish", file=sys.stderr)
    raise SystemExit(1)
renameat2.argtypes = (
    ctypes.c_int,
    ctypes.c_char_p,
    ctypes.c_int,
    ctypes.c_char_p,
    ctypes.c_uint,
)
renameat2.restype = ctypes.c_int
result = renameat2(
    AT_FDCWD,
    os.fsencode(sys.argv[1]),
    AT_FDCWD,
    os.fsencode(sys.argv[2]),
    RENAME_NOREPLACE,
)
if result != 0:
    error_number = ctypes.get_errno()
    print(
        f"[probe-release] atomic no-replace publish failed: {os.strerror(error_number)}",
        file=sys.stderr,
    )
    raise SystemExit(1)
PY
        die "release output could not be atomically published without overwriting: $destination"
    fi
    [[ ! -e "$source" && ! -L "$source" ]] ||
        die "atomic release publication did not consume its staged directory: $source"
    [[ -d "$destination" && ! -L "$destination" ]] ||
        die "atomic release publication did not produce a real output directory: $destination"
}

stage_repository_commit() {
    local label="$1" root="$2" commit="$3" destination="$4"
    local snapshot_work archive_path snapshot_tree isolated_git source_object_dir

    [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "$label verified source commit is malformed"
    [[ ! -e "$destination" && ! -L "$destination" ]] ||
        die "$label snapshot destination already exists: $destination"
    snapshot_work="$(mktemp -d "${WORK_ROOT}/.probe-source-snapshot.XXXXXX")"
    archive_path="${snapshot_work}/source.tar"
    snapshot_tree="${snapshot_work}/tree"
    isolated_git="${snapshot_work}/repository.git"

    (
        # Invoked indirectly by this subshell's EXIT trap.
        # shellcheck disable=SC2329
        cleanup_source_snapshot() {
            local status=$?
            trap - EXIT HUP INT TERM
            if [[ -n "$snapshot_work" && -d "$snapshot_work" && ! -L "$snapshot_work" &&
                "$snapshot_work" == "$WORK_ROOT"/.probe-source-snapshot.* ]]; then
                rm -rf -- "$snapshot_work"
            else
                printf '[probe-release] WARNING: refusing to clean unexpected source snapshot path: %s\n' \
                    "$snapshot_work" >&2
            fi
            exit "$status"
        }
        trap cleanup_source_snapshot EXIT
        trap 'exit 129' HUP
        trap 'exit 130' INT
        trap 'exit 143' TERM

        install -d -m 0700 "$snapshot_tree"
        source_object_dir="$(
            trusted_git -C "$root" rev-parse --git-path objects
        )" || die "$label Git object database could not be located"
        case "$source_object_dir" in
            /*) ;;
            *) source_object_dir="$root/$source_object_dir" ;;
        esac
        source_object_dir="$(canonical_source_directory "$label Git object database" "$source_object_dir")"
        [[ "$source_object_dir" != *$'\n'* && "$source_object_dir" != *$'\r'* ]] ||
            die "$label Git object database path cannot be represented as an alternate"
        trusted_git -c core.attributesFile=/dev/null \
            init --bare --template= \
            "$isolated_git" >/dev/null ||
            die "$label isolated Git object view could not be initialized"
        printf '%s\n' "$source_object_dir" > "$isolated_git/objects/info/alternates"
        chmod 0600 "$isolated_git/objects/info/alternates"
        trusted_git -c core.attributesFile=/dev/null --git-dir="$isolated_git" \
            cat-file -e "${commit}^{commit}" ||
            die "$label verified source commit is no longer available: $commit"
        trusted_git -c core.attributesFile=/dev/null --git-dir="$isolated_git" \
            archive --format=tar --output="$archive_path" "$commit" ||
            die "$label could not be archived from the verified source commit: $commit"
        [[ -f "$archive_path" && ! -L "$archive_path" ]] ||
            die "$label Git archive was not created as a regular file"
        tar -xf "$archive_path" -C "$snapshot_tree" ||
            die "$label verified source archive could not be extracted"
        mv -T -- "$snapshot_tree" "$destination" ||
            die "$label verified source snapshot could not be committed"
    )
}

copy_management_runtime_script() {
    local common_source="$1" management_source="$2" destination="$3"
    local runtime_functions legacy_hardening_count scan_source scan_status
    runtime_functions='log warn die require_root validate_management_platform_id parse_management_os_release_token parse_management_os_release_name management_platform_id_from_release management_platform_nginx_dialect management_platform_systemd_profile management_platform_systemd_minimum management_platform_package_family management_platform_postgres_service management_platform_certbot_timer management_platform_postgres_bin_dir assert_runtime_platform_contract initialize_runtime_platform_contract runtime_platform_id runtime_systemd_profile runtime_postgres_service runtime_certbot_timer runtime_account_family runtime_postgres_command require_runtime_postgres_commands management_platform_id_from_os_release require_supported_runtime_platform require_commands acquire_root_lock acquire_deployment_lock login_defs_number assert_probe_api_service_account prepare_probe_api_service_account run_as_probe_api_no_environment canonical_directory clear_exported_probe_environment assert_secure_file assert_private_file assert_public_root_file require_integer_between canonical_ip_from_origin validate_closed_install_routes validate_management_nginx_fragment_structure validate_management_release_platform validate_allowlist_with_binary validate_static_artifact install_example_file selected_systemd_asset_name management_service_asset_paths validate_management_service_asset_kind management_service_asset_kind_matches validate_management_service_asset_snapshot_root remove_management_service_asset_temporaries cleanup_failed_service_asset_snapshot snapshot_management_service_assets restore_management_service_assets discard_management_service_asset_snapshot management_systemd_property capture_management_unit_activity capture_management_service_activity stop_management_unit_to_inactive restore_management_service_activity restore_management_postgres_activity run_management_rollback_step install_service_assets validate_systemd_unit_source validate_backup_unit_source verify_source_systemd_units validate_backup_service_assets validate_backup_credentials acquire_database_maintenance_lock assert_switchable_path atomic_release_link current_release_target create_database_backup persist_native_nginx_service run_migrations management_installed_nginx_template management_lifecycle_asset_names validate_management_lifecycle_manifest validate_management_lifecycle_assets validate_installed_management_host'

    {
        printf '%s\n' \
            '#!/usr/bin/env bash' \
            '' \
            '# Management-only prebuilt release runtime for Probe Panel v1.2.' \
            '# Generated from reviewed source functions; never edit the bundled copy.' \
            ''
        awk '
            /^set -Eeuo pipefail$/ {
                starts++
                if (starts == 1 && boundaries == 0) copying=1
                else invalid=1
            }
            /^cleanup_deploy_work_root\(\) \{$/ {
                boundaries++
                if (starts != 1 || !copying || boundaries != 1) invalid=1
                copying=0
                next
            }
            copying &&
                $0 !~ /^#/ &&
                $0 !~ /^MANAGEMENT_ROLLBACK_(RELEASE_PROFILE|OLD_AGENT|OLD_WEB)=/ { print }
            END {
                if (starts != 1 || boundaries != 1 || invalid || copying) exit 30
            }
        ' "$common_source"
    } > "$destination" || {
        rm -f -- "$destination"
        die "could not extract the reviewed management runtime header"
    }
    awk -v wanted="$runtime_functions" '
        BEGIN {
            count=split(wanted, names, / +/)
            for (position=1; position<=count; position++) required[names[position]]=1
        }
        /^[A-Za-z_][A-Za-z0-9_]*\(\) [({]$/ {
            name=$0
            sub(/\(\).*/, "", name)
            copying=(name in required)
            if (copying) seen[name]++
        }
        copying { print }
        copying && ($0 == "}" || $0 == ")") { copying=0 }
        END {
            for (name in required) {
                if (seen[name] != 1) exit 30
            }
        }
    ' "$common_source" >> "$destination" || {
        rm -f -- "$destination"
        die "could not extract the reviewed management runtime function set"
    }
    # These appends deliberately surround the separately checked extractor and
    # keep its failure cleanup distinct from the management-only source append.
    # shellcheck disable=SC2129
    printf '\n' >> "$destination"
    cat "$management_source" >> "$destination"
    printf '%s\n' \
        '' \
        'trap cleanup_deploy_work_root EXIT' \
        "trap 'exit 129' HUP" \
        "trap 'exit 130' INT" \
        "trap 'exit 143' TERM" \
        >> "$destination"

    bash -n "$destination" || {
        rm -f -- "$destination"
        die "generated management runtime deploy helper has invalid syntax"
    }
    shellcheck "$destination" || {
        rm -f -- "$destination"
        die "generated management runtime deploy helper failed shellcheck"
    }

    local selftest_root="${destination}.platform-selftest"
    install -d -m 0700 "$selftest_root/canonical" "$selftest_root/reordered" "$selftest_root/os-release"
    printf '%s\n' \
        "runtime_abi=$MANAGEMENT_RUNTIME_ABI" \
        "platform_ids=$MANAGEMENT_PLATFORM_IDS" \
        > "$selftest_root/canonical/RELEASE-MANIFEST"
    printf '%s\n' \
        "runtime_abi=$MANAGEMENT_RUNTIME_ABI" \
        'platform_ids=centos-stream-10-systemd,debian-9-systemd,debian-10-systemd,debian-11-systemd,debian-12-systemd,debian-13-systemd,ubuntu-18.04-systemd,ubuntu-20.04-systemd,ubuntu-22.04-systemd,ubuntu-24.04-systemd,ubuntu-26.04-systemd,centos-linux-7-systemd,centos-linux-8-systemd,centos-stream-8-systemd,centos-stream-9-systemd' \
        > "$selftest_root/reordered/RELEASE-MANIFEST"
    printf '%s\n' 'ID=ubuntu' 'VERSION_ID="22.04"' \
        > "$selftest_root/os-release/ubuntu-22.04"
    printf '%s\n' 'NAME="CentOS Stream"' 'ID=centos' 'VERSION_ID="9"' \
        > "$selftest_root/os-release/centos-stream-9"
    if ! (
        # Source the actual generated file so a missing extracted helper fails
        # here, before any release bundle can be assembled.
        # shellcheck disable=SC1090
        source "$destination"
        trap - EXIT
        for function_name in \
            parse_management_os_release_token \
            parse_management_os_release_name \
            management_platform_id_from_release \
            management_platform_nginx_dialect \
            management_platform_systemd_profile \
            management_platform_systemd_minimum \
            management_platform_package_family \
            management_platform_postgres_service \
            management_platform_certbot_timer \
            management_platform_postgres_bin_dir \
            assert_runtime_platform_contract \
            initialize_runtime_platform_contract \
            runtime_platform_id \
            runtime_systemd_profile \
            runtime_postgres_service \
            runtime_certbot_timer \
            runtime_account_family \
            runtime_postgres_command \
            management_platform_id_from_os_release \
            validate_management_release_platform; do
            declare -F "$function_name" >/dev/null
        done
        [[ "$(parse_management_os_release_token ID ubuntu)" == ubuntu ]]
        [[ "$(management_platform_id_from_release debian 9)" == debian-9-systemd ]]
        [[ "$(management_platform_id_from_release debian 10)" == debian-10-systemd ]]
        [[ "$(management_platform_id_from_release debian 11)" == debian-11-systemd ]]
        [[ "$(management_platform_id_from_release debian 12)" == debian-12-systemd ]]
        [[ "$(management_platform_id_from_release debian 13)" == debian-13-systemd ]]
        [[ "$(management_platform_id_from_release ubuntu 18.04)" == ubuntu-18.04-systemd ]]
        [[ "$(management_platform_id_from_release ubuntu 20.04)" == ubuntu-20.04-systemd ]]
        [[ "$(management_platform_id_from_release ubuntu 22.04)" == ubuntu-22.04-systemd ]]
        [[ "$(management_platform_id_from_release ubuntu 24.04)" == ubuntu-24.04-systemd ]]
        [[ "$(management_platform_id_from_release ubuntu 26.04)" == ubuntu-26.04-systemd ]]
        [[ "$(management_platform_id_from_release centos 9 'CentOS Stream')" == centos-stream-9-systemd ]]
        [[ "$(management_platform_systemd_profile centos-linux-7-systemd)" == legacy ]]
        [[ "$(management_platform_systemd_minimum centos-linux-7-systemd)" == 219 ]]
        [[ "$(management_platform_postgres_service centos-stream-9-systemd)" == postgresql-14.service ]]
        [[ "$(management_platform_certbot_timer centos-stream-9-systemd)" == certbot-renew.timer ]]
        [[ "$(management_platform_postgres_bin_dir centos-stream-9-systemd)" == /usr/pgsql-14/bin ]]
        [[ "$(management_platform_nginx_dialect debian-13-systemd)" == modern ]]
        [[ "$(management_platform_nginx_dialect ubuntu-24.04-systemd)" == legacy ]]
        if uninitialized_pg_dump="$(runtime_postgres_command pg_dump 2>/dev/null)"; then
            exit 35
        fi
        [[ -z "$uninitialized_pg_dump" ]]
        initialize_runtime_platform_contract centos-stream-9-systemd
        [[ "$(runtime_platform_id)" == centos-stream-9-systemd ]]
        [[ "$(runtime_systemd_profile)" == modern ]]
        [[ "$(runtime_postgres_service)" == postgresql-14.service ]]
        [[ "$(runtime_certbot_timer)" == certbot-renew.timer ]]
        [[ "$(runtime_account_family)" == rpm ]]
        [[ "$(runtime_postgres_command pg_dump)" == /usr/pgsql-14/bin/pg_dump ]]
        [[ "$(runtime_postgres_command pg_restore)" == /usr/pgsql-14/bin/pg_restore ]]
        [[ "$(runtime_postgres_command psql)" == /usr/pgsql-14/bin/psql ]]
        [[ "$(management_platform_id_from_os_release "$selftest_root/os-release/ubuntu-22.04")" == ubuntu-22.04-systemd ]]
        [[ "$(management_platform_id_from_os_release "$selftest_root/os-release/centos-stream-9")" == centos-stream-9-systemd ]]
        validate_management_release_platform "$selftest_root/canonical" debian-12-systemd
        if ( validate_management_release_platform "$selftest_root/reordered" debian-12-systemd \
            >/dev/null 2>&1 ); then
            exit 31
        fi
        if ( management_platform_id_from_release Ubuntu 24.04 >/dev/null 2>&1 ); then
            exit 32
        fi
        if ( parse_management_os_release_token ID '"ubuntu' >/dev/null 2>&1 ); then
            exit 33
        fi
    ); then
        rm -rf -- "$selftest_root"
        rm -f -- "$destination"
        die "generated management runtime deploy helper failed its platform contract self-test"
    fi
    rm -rf -- "$selftest_root"

    # CentOS 7's systemd 219 supports ProtectSystem=full but not strict.  Keep
    # the two reviewed legacy hardening assertions without allowing that
    # systemd value to become a blanket exception to the management-only scan.
    grep -Fxq \
        "        grep -Fxq 'ProtectSystem=full' \"\$unit_file\" || die \"legacy probe-api unit must protect the system\"" \
        "$destination" || {
        rm -f -- "$destination"
        die "generated management runtime is missing the reviewed legacy API hardening assertion"
    }
    grep -Fxq \
        "        grep -Fxq 'ProtectSystem=full' \"\$service_file\" ||" \
        "$destination" || {
        rm -f -- "$destination"
        die "generated management runtime is missing the reviewed legacy backup hardening assertion"
    }
    legacy_hardening_count="$(grep -Foc 'ProtectSystem=full' "$destination")"
    [[ "$legacy_hardening_count" == 2 ]] || {
        rm -f -- "$destination"
        die "generated management runtime contains an unreviewed ProtectSystem=full occurrence"
    }

    scan_source="${destination}.management-only-scan"
    sed 's/ProtectSystem=full/ProtectSystem=reviewed-legacy-hardening/g' \
        "$destination" > "$scan_source" || {
        rm -f -- "$scan_source" "$destination"
        die "could not prepare the generated management runtime content scan"
    }
    scan_status=0
    grep -Eiq 'MANAGEMENT_BUNDLE_EXCLUDE|build_release_artifacts|deploy_release\(\)|npm[[:space:]]+run[[:space:]]+build|[.]/cmd/probe-agent|artifacts/(agent|web)|PROBE_(AGENT|WEB)_DIR|old_(agent|web)|probe-web|/srv/probe/(agent|web)|(^|[^[:alnum:]_])full([^[:alnum:]_]|$)' \
        "$scan_source" || scan_status=$?
    case "$scan_status" in
        0)
            rm -f -- "$scan_source" "$destination"
            die "management runtime deploy helper still contains forbidden full, Agent-artifact, visitor, or build logic"
            ;;
        1) rm -f -- "$scan_source" ;;
        *)
            rm -f -- "$scan_source" "$destination"
            die "could not scan the generated management runtime for forbidden release logic"
            ;;
    esac
}

assert_no_links_or_maps() {
    local label="$1" root="$2" found
    found="$(find "$root" -type l -print -quit)"
    [[ -z "$found" ]] || die "$label contains a symbolic link: $found"
    found="$(find "$root" -type f -name '*.map' -print -quit)"
    [[ -z "$found" ]] || die "$label contains a forbidden source map: $found"
}

assert_binary_architecture() {
    local path="$1" architecture="$2" description
    description="$(file -b -- "$path")"
    [[ "$description" == *ELF* && "$description" == *'statically linked'* ]] ||
        die "binary is not a static Linux ELF: $path ($description)"
    case "$architecture" in
        amd64) [[ "$description" == *'x86-64'* ]] || die "binary architecture mismatch: $path" ;;
        arm64) [[ "$description" == *'ARM aarch64'* ]] || die "binary architecture mismatch: $path" ;;
        *) die "unsupported release architecture: $architecture" ;;
    esac
}

build_frontend() {
    local label="$1" source_root="$2" artifact_root="$3"
    log "building $label from the verified commit snapshot"
    (
        cd "$source_root"
        npm ci --no-audit --no-fund
        npm run build
    )
    [[ -f "$source_root/dist/index.html" && ! -L "$source_root/dist/index.html" ]] ||
        die "$label build did not create dist/index.html"
    assert_no_links_or_maps "$label production output" "$source_root/dist"
    install -d -m 0755 "$artifact_root"
    cp -a -- "$source_root/dist/." "$artifact_root/"
    log "testing and auditing $label after capturing its build artifact"
    (
        cd "$source_root"
        npm test
        npm audit --omit=dev --audit-level=high
    )
}

assert_management_setup_artifact() {
    local artifact_root="$1" forbidden
    for forbidden in panel-domain agent-domain '游客面板域名' 'Agent API 域名' '三个域名'; do
        if grep -R -I -F -q -- "$forbidden" "$artifact_root"; then
            die "management administrator artifact contains a historical multi-product setup control: $forbidden"
        fi
    done
}

assert_manifest_safe_paths() {
    local root="$1" path relative
    while IFS= read -r -d '' path; do
        relative="${path#"$root"/}"
        [[ -n "$relative" && "$relative" != *[[:space:]\\]* ]] ||
            die "bundle path cannot be represented safely in SHA256SUMS: $relative"
    done < <(find "$root/artifacts" "$root/setup" "$root/source" -type f -print0)
}

assemble_bundle() {
    local architecture="$1" api_binary="$2" setup_binary="$3"
    local common_root="$4" api_source_root="$5" publish_root="$6" profile="$7"
    local bundle_prefix="probe-panel"
    [[ "$profile" == full ]] || bundle_prefix="probe-panel-management"
    local bundle_name="${bundle_prefix}-${VERSION}-linux-${architecture}"
    local bundle_root="${WORK_ROOT}/${bundle_name}"

    install -d -m 0755 \
        "$bundle_root/artifacts/api" "$bundle_root/artifacts/admin" \
        "$bundle_root/artifacts/migrations" "$bundle_root/setup" \
        "$bundle_root/source/probe-api"
    install -m 0755 "$api_binary" "$bundle_root/artifacts/api/probe-api"
    install -m 0755 "$setup_binary" "$bundle_root/setup/probe-setup"
    cp -a -- "$common_root/admin/." "$bundle_root/artifacts/admin/"
    if [[ "$profile" == management ]]; then
        assert_management_setup_artifact "$bundle_root/artifacts/admin"
    fi
    if [[ "$profile" == full ]]; then
        install -d -m 0755 "$bundle_root/artifacts/web" "$bundle_root/artifacts/agent"
        cp -a -- "$common_root/web/." "$bundle_root/artifacts/web/"
        cp -a -- "$common_root/agent/." "$bundle_root/artifacts/agent/"
    fi
    cp -a -- "$api_source_root/migrations/." "$bundle_root/artifacts/migrations/"
    cp -a -- "$api_source_root/config" "$bundle_root/source/probe-api/config"
    if [[ "$profile" == management ]]; then
        local relative
        install -d -m 0755 \
            "$bundle_root/source/probe-api/deploy/nginx" \
            "$bundle_root/source/probe-api/deploy/scripts" \
            "$bundle_root/source/probe-api/deploy/setup" \
            "$bundle_root/source/probe-api/deploy/systemd"
        for relative in \
            nginx/nginx-management.conf \
            nginx/nginx-management-ip.conf \
            nginx/nginx-management-legacy.conf \
            nginx/nginx-management-ip-legacy.conf \
            nginx/nginx-management-classic.conf \
            nginx/nginx-management-ip-classic.conf \
            scripts/install-release.sh \
            scripts/validate-management.sh \
            scripts/restore-management.sh \
            scripts/uninstall-management.sh \
            scripts/backup-postgres.sh \
            scripts/restore-postgres.sh \
            setup/probe-panel-setup.env.example \
            setup/probe-panel-setup.service \
            setup/probe-panel-setup.socket \
            setup/probe-panel-setup-legacy.service \
            setup/probe-panel-setup-legacy.socket \
            setup/probe-panel-finalizer-management.service \
            setup/probe-panel-finalizer-management-legacy.service \
            setup/probe-panel-finalizer.path \
            systemd/probe-api.service \
            systemd/probe-api-legacy.service \
            systemd/probe-postgres-backup.service \
            systemd/probe-postgres-backup.timer \
            systemd/probe-postgres-backup-legacy.service \
            systemd/probe-postgres-backup-legacy.timer; do
            cp -a -- \
                "$api_source_root/deploy/$relative" \
                "$bundle_root/source/probe-api/deploy/$relative"
        done
        copy_management_runtime_script \
            "$api_source_root/deploy/scripts/deploy-common.sh" \
            "$api_source_root/deploy/scripts/deploy-common-management-runtime.sh" \
            "$bundle_root/source/probe-api/deploy/scripts/deploy-common.sh"
        for forbidden in \
            "$bundle_root/source/probe-api/deploy/nginx/nginx.conf" \
            "$bundle_root/source/probe-api/deploy/nginx/nginx-ip.conf" \
            "$bundle_root/source/probe-api/deploy/setup/probe-panel-finalizer.service"; do
            [[ ! -e "$forbidden" && ! -L "$forbidden" ]] ||
                die "management bundle contains a historical full-profile deployment file: $forbidden"
        done
    else
        cp -a -- "$api_source_root/deploy" "$bundle_root/source/probe-api/deploy"
        rm -f -- \
            "$bundle_root/source/probe-api/deploy/nginx/nginx-management.conf" \
            "$bundle_root/source/probe-api/deploy/nginx/nginx-management-ip.conf" \
            "$bundle_root/source/probe-api/deploy/nginx/nginx-management-legacy.conf" \
            "$bundle_root/source/probe-api/deploy/nginx/nginx-management-ip-legacy.conf" \
            "$bundle_root/source/probe-api/deploy/nginx/nginx-management-classic.conf" \
            "$bundle_root/source/probe-api/deploy/nginx/nginx-management-ip-classic.conf" \
            "$bundle_root/source/probe-api/deploy/setup/probe-panel-finalizer-management.service" \
            "$bundle_root/source/probe-api/deploy/setup/probe-panel-finalizer-management-legacy.service"
    fi

    cat > "$bundle_root/RELEASE-MANIFEST" <<EOF
format=probe-panel-release-v1
version=${VERSION}
architecture=linux-${architecture}
profile=${profile}
runtime_abi=${MANAGEMENT_RUNTIME_ABI}
platform_ids=${MANAGEMENT_PLATFORM_IDS}
source_repository=${SOURCE_REPOSITORY}
source_commit=${SOURCE_COMMIT}
source_tag_object=${SOURCE_TAG_OBJECT}
super_my_ref=${SUPER_MY_REF}
EOF
    if [[ "$profile" == full ]]; then
        cat >> "$bundle_root/RELEASE-MANIFEST" <<EOF
my_ref=${WEB_REF}
my_agent_ref=${AGENT_REF}
EOF
    fi

    assert_no_links_or_maps "$bundle_name" "$bundle_root"
    assert_manifest_safe_paths "$bundle_root"
    find "$bundle_root" -type d -exec chmod 0755 {} +
    find "$bundle_root" -type f -exec chmod 0644 {} +
    chmod 0755 \
        "$bundle_root/artifacts/api/probe-api" \
        "$bundle_root/setup/probe-setup" \
        "$bundle_root/source/probe-api/deploy/scripts/install-release.sh" \
        "$bundle_root/source/probe-api/deploy/scripts/validate-management.sh" \
        "$bundle_root/source/probe-api/deploy/scripts/restore-management.sh" \
        "$bundle_root/source/probe-api/deploy/scripts/uninstall-management.sh"
    if [[ "$profile" == full ]]; then
        chmod 0755 \
            "$bundle_root/artifacts/agent/downloads/probe-agent/install.sh" \
            "$bundle_root/artifacts/agent/downloads/probe-agent/linux-amd64/probe-agent" \
            "$bundle_root/artifacts/agent/downloads/probe-agent/linux-arm64/probe-agent"
    fi
    (
        cd "$bundle_root"
        find artifacts setup source -type f -print0 | LC_ALL=C sort -z |
            xargs -0 sha256sum > BUNDLE-SHA256SUMS
        sha256sum --check --strict BUNDLE-SHA256SUMS >/dev/null
    )
    tar --sort=name --mtime='UTC 2020-01-01' --owner=0 --group=0 --numeric-owner \
        -C "$WORK_ROOT" -czf "$publish_root/${bundle_name}.tar.gz" "$bundle_name"
    gzip -t "$publish_root/${bundle_name}.tar.gz"
}

main() {
    parse_arguments "$@"
    validate_profile
    require_debian_13
    local command_name
    for command_name in awk basename bash cat chmod cmp cp dirname file find git go grep gzip install \
        mkdir mktemp mv npm python3 readlink rm sh sha256sum shellcheck sort stat tar xargs; do
        require_command "$command_name"
    done

    ADMIN_ROOT="$(canonical_source_directory probe-admin "$ADMIN_ROOT")"
    if [[ "$PROFILE" == full ]]; then
        WEB_ROOT="$(canonical_source_directory probe-web "$WEB_ROOT")"
        AGENT_ROOT="$(canonical_source_directory probe-agent "$AGENT_ROOT")"
    else
        WEB_ROOT=""
        AGENT_ROOT=""
    fi
    readonly ADMIN_ROOT WEB_ROOT AGENT_ROOT
    validate_fixed_repository_sources
    [[ "$SOURCE_COMMIT" =~ ^[0-9a-f]{40}$ ]] ||
        die 'verified super-my source commit is malformed'
    if [[ "$PROFILE" == full ]]; then
        [[ "$WEB_SOURCE_COMMIT" =~ ^[0-9a-f]{40}$ ]] ||
            die 'verified my source commit is malformed'
        [[ "$AGENT_SOURCE_COMMIT" =~ ^[0-9a-f]{40}$ ]] ||
            die 'verified my-agent source commit is malformed'
    fi
    readonly SOURCE_COMMIT SOURCE_TAG_OBJECT
    readonly WEB_SOURCE_COMMIT WEB_SOURCE_TAG_OBJECT
    readonly AGENT_SOURCE_COMMIT AGENT_SOURCE_TAG_OBJECT

    local output_parent output_name
    output_parent="$(dirname -- "$OUTPUT_DIR")"
    output_name="$(basename -- "$OUTPUT_DIR")"
    [[ -n "$output_name" && "$output_name" != . && "$output_name" != .. ]] ||
        die 'output directory name is invalid'
    install -d -m 0755 "$output_parent"
    output_parent="$(readlink -f -- "$output_parent")"
    OUTPUT_DIR="${output_parent}/${output_name}"
    assert_output_outside_sources "$OUTPUT_DIR"
    [[ ! -e "$OUTPUT_DIR" && ! -L "$OUTPUT_DIR" ]] ||
        die "output directory already exists; refusing to overwrite it: $OUTPUT_DIR"

    # Choose the cleanup target and install all traps before creating it.  A
    # signal can therefore never land after directory creation but before the
    # cleanup handler knows its exact path.
    WORK_ROOT="${output_parent}/.probe-release-build.${BASHPID}.${RANDOM}${RANDOM}"
    trap cleanup EXIT
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM
    if ! mkdir -m 0700 -- "$WORK_ROOT"; then
        WORK_ROOT=""
        die "could not reserve a unique release work directory"
    fi

    local source_root="${WORK_ROOT}/sources"
    local pristine_super_source="${source_root}/pristine-super-my"
    local pristine_api_source="${pristine_super_source}/probe-api"
    local super_source="${source_root}/super-my"
    local api_source="${super_source}/probe-api"
    local web_source="" agent_source=""
    local binary_root="${WORK_ROOT}/binaries"
    local common_root="${WORK_ROOT}/common"
    local publish_root="${WORK_ROOT}/publish"
    install -d -m 0700 "$source_root" "$binary_root/api" \
        "$binary_root/setup" "$publish_root"
    # Read the live object database exactly once.  Packaging keeps this
    # pristine snapshot; all builds and tests use an independent work copy.
    stage_repository_commit super-my "$SUPER_MY_ROOT" "$SOURCE_COMMIT" "$pristine_super_source"
    cp -a -- "$pristine_super_source" "$super_source"
    if [[ "$PROFILE" == full ]]; then
        local pristine_web_source="${source_root}/pristine-my"
        local pristine_agent_source="${source_root}/pristine-my-agent"
        web_source="${source_root}/my"
        agent_source="${source_root}/my-agent"
        install -d -m 0700 "$binary_root/agent/linux-amd64" \
            "$binary_root/agent/linux-arm64" \
            "$common_root/agent/downloads/probe-agent/linux-amd64" \
            "$common_root/agent/downloads/probe-agent/linux-arm64"
        stage_repository_commit my "$WEB_ROOT" "$WEB_SOURCE_COMMIT" "$pristine_web_source"
        stage_repository_commit my-agent "$AGENT_ROOT" "$AGENT_SOURCE_COMMIT" "$pristine_agent_source"
        cp -a -- "$pristine_web_source" "$web_source"
        cp -a -- "$pristine_agent_source" "$agent_source"
    fi
    validate_sources "$super_source" "$api_source" "$super_source" \
        "$web_source" "$agent_source" "$PROFILE"
    validate_sources "$pristine_super_source" "$pristine_api_source" "$pristine_super_source" \
        "${pristine_web_source:-}" "${pristine_agent_source:-}" "$PROFILE"
    SOURCE_INPUT_DIGEST="$(source_input_digest "$super_source")"
    PRISTINE_SOURCE_DIGEST="$(source_input_digest "$pristine_super_source")"
    if [[ "$PROFILE" == full ]]; then
        WEB_INPUT_DIGEST="$(source_input_digest "$web_source")"
        AGENT_INPUT_DIGEST="$(source_input_digest "$agent_source")"
    fi
    readonly SOURCE_INPUT_DIGEST PRISTINE_SOURCE_DIGEST WEB_INPUT_DIGEST AGENT_INPUT_DIGEST

    log 'cross-building probe-api and probe-setup from the verified work snapshot'
    (
        cd "$api_source"
        for architecture in amd64 arm64; do
            CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" \
                go build -trimpath -ldflags='-s -w' \
                -o "$binary_root/api/probe-api-$architecture" ./cmd/probe-api
            CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" \
                go build -trimpath -ldflags='-s -w' \
                -o "$binary_root/setup/probe-setup-$architecture" ./cmd/probe-setup
        done
    )
    revalidate_all_source_inputs "$super_source" "$pristine_super_source" \
        "$web_source" "$agent_source"
    if [[ "$PROFILE" == full ]]; then
        log 'cross-building probe-agent from the verified work snapshot'
        (
            cd "$agent_source"
            for architecture in amd64 arm64; do
                CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" \
                    go build -trimpath -ldflags='-s -w' \
                    -o "$binary_root/agent/linux-$architecture/probe-agent" ./cmd/probe-agent
            done
        )
        revalidate_all_source_inputs "$super_source" "$pristine_super_source" \
            "$web_source" "$agent_source"
    fi
    build_frontend probe-admin "$super_source" "$common_root/admin"
    revalidate_all_source_inputs "$super_source" "$pristine_super_source" \
        "$web_source" "$agent_source"
    if [[ "$PROFILE" == full ]]; then
        build_frontend probe-web "$web_source" "$common_root/web"
        revalidate_all_source_inputs "$super_source" "$pristine_super_source" \
            "$web_source" "$agent_source"
    fi

    log 'checking deployment Shell contracts'
    shellcheck "$super_source/install.sh" "$super_source/install/build-standalone.sh" \
        "$super_source/install/common.sh" "$super_source"/install/platforms/*.sh \
        "$api_source"/deploy/scripts/*.sh "$api_source"/deploy/tests/*.sh
    if [[ "$PROFILE" == full ]]; then
        shellcheck "$agent_source/deploy/install.sh" "$agent_source"/deploy/tests/*.sh
    fi
    (
        cd "$super_source"
        sh probe-api/deploy/tests/bootstrap-install-contract.sh
        sh probe-api/deploy/tests/build-release-bundles-contract.sh
        sh probe-api/deploy/tests/management-lifecycle-contract.sh
    )
    revalidate_all_source_inputs "$super_source" "$pristine_super_source" \
        "$web_source" "$agent_source"

    log 'testing and vetting probe-api'
    (
        cd "$api_source"
        if [[ "$PROFILE" == management ]]; then
            go run ./cmd/probe-support-gate verify \
                --support-root deploy/support --release "$VERSION" \
                --require-zero-supported
        fi
        go test -count=1 ./...
        go vet ./...
    )
    revalidate_all_source_inputs "$super_source" "$pristine_super_source" \
        "$web_source" "$agent_source"

    if [[ "$PROFILE" == full ]]; then
        log 'testing and vetting probe-agent after capturing its binaries'
        (
            cd "$agent_source"
            go test -count=1 ./...
            go vet ./...
            sh deploy/tests/install-contract.sh
        )
    fi
    revalidate_all_source_inputs "$super_source" "$pristine_super_source" \
        "$web_source" "$agent_source"

    local architecture
    for architecture in amd64 arm64; do
        assert_binary_architecture "$binary_root/api/probe-api-$architecture" "$architecture"
        assert_binary_architecture "$binary_root/setup/probe-setup-$architecture" "$architecture"
        if [[ "$PROFILE" == full ]]; then
            assert_binary_architecture "$binary_root/agent/linux-$architecture/probe-agent" "$architecture"
            install -m 0755 "$binary_root/agent/linux-$architecture/probe-agent" \
                "$common_root/agent/downloads/probe-agent/linux-$architecture/probe-agent"
        fi
    done
    if [[ "$PROFILE" == full ]]; then
        install -m 0755 "$agent_source/deploy/install.sh" \
            "$common_root/agent/downloads/probe-agent/install.sh"
        install -m 0644 "$agent_source/deploy/systemd/probe-agent.service" \
            "$common_root/agent/downloads/probe-agent/probe-agent.service"
        (
            cd "$common_root/agent/downloads/probe-agent"
            sha256sum install.sh probe-agent.service linux-amd64/probe-agent \
                linux-arm64/probe-agent > SHA256SUMS
            sha256sum --check --strict SHA256SUMS >/dev/null
        )
    fi

    # Bracket RELEASE-MANIFEST creation with exact remote-ref checks.  A tag
    # move during the build therefore fails closed instead of publishing a
    # manifest whose named ref no longer identifies source_commit.
    revalidate_remote_source_refs
    for architecture in amd64 arm64; do
        log "assembling linux-$architecture release bundle"
        assemble_bundle "$architecture" \
            "$binary_root/api/probe-api-$architecture" \
            "$binary_root/setup/probe-setup-$architecture" \
            "$common_root" "$pristine_api_source" "$publish_root" "$PROFILE"
    done
    revalidate_all_source_inputs "$super_source" "$pristine_super_source" \
        "$web_source" "$agent_source"
    (
        cd "$publish_root"
        local bundle_prefix="probe-panel"
        [[ "$PROFILE" == full ]] || bundle_prefix="probe-panel-management"
        sha256sum \
            "${bundle_prefix}-${VERSION}-linux-amd64.tar.gz" \
            "${bundle_prefix}-${VERSION}-linux-arm64.tar.gz" > SHA256SUMS
        sha256sum --check --strict SHA256SUMS >/dev/null
    )
    revalidate_remote_source_refs
    publish_output_directory "$publish_root" "$OUTPUT_DIR"
    printf 'Probe Panel %s release assets created locally:\n  %s\n' "$VERSION" "$OUTPUT_DIR"
    printf 'No GitHub release was created or modified.\n'
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
