#!/bin/sh
# Static contracts below intentionally contain unexpanded shell expressions.
# shellcheck disable=SC2016

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH='' cd -- "$SCRIPT_DIR/../../.." && pwd)
BUILDER=$ROOT_DIR/probe-api/deploy/scripts/build-release-bundles.sh
INSTALLER=$ROOT_DIR/install.sh
INSTALL_COMMON=$ROOT_DIR/install/common.sh
INSTALL_GENERATOR=$ROOT_DIR/install/build-standalone.sh
PLATFORM_DEBIAN=$ROOT_DIR/install/platforms/debian.sh
PLATFORM_UBUNTU=$ROOT_DIR/install/platforms/ubuntu.sh
PLATFORM_CENTOS=$ROOT_DIR/install/platforms/centos.sh
MANAGEMENT_RUNTIME=$ROOT_DIR/probe-api/deploy/scripts/deploy-common-management-runtime.sh
DEPLOY_COMMON=$ROOT_DIR/probe-api/deploy/scripts/deploy-common.sh
INSTALL_RELEASE=$ROOT_DIR/probe-api/deploy/scripts/install-release.sh
WORKFLOW=$ROOT_DIR/.github/workflows/release.yml
SUPPORT_POLICY=$ROOT_DIR/probe-api/deploy/support/policy-v1.json
SUPPORT_LEDGER=$ROOT_DIR/probe-api/deploy/support/releases/v1.2.0.json
SUPPORT_GATE=$ROOT_DIR/probe-api/cmd/probe-support-gate/main.go
TEST_ROOT=$(mktemp -d /var/tmp/probe-release-builder-contract.XXXXXX)

cleanup() {
    case "$TEST_ROOT" in
        /var/tmp/probe-release-builder-contract.*)
            rm -rf -- "$TEST_ROOT"
            ;;
    esac
}
trap cleanup EXIT HUP INT TERM

fail() {
    printf '%s\n' "release builder contract: $*" >&2
    exit 1
}

assert_contains() {
    needle=$1
    file=$2
    grep -Fq -- "$needle" "$file" || fail "$file is missing required contract: $needle"
}

[ -f "$BUILDER" ] || fail "missing release builder: $BUILDER"
[ -f "$INSTALLER" ] || fail "missing standalone installer: $INSTALLER"
[ -f "$INSTALL_COMMON" ] || fail "missing installer common source: $INSTALL_COMMON"
[ -f "$INSTALL_GENERATOR" ] || fail "missing standalone installer generator: $INSTALL_GENERATOR"
[ -f "$PLATFORM_DEBIAN" ] || fail "missing Debian installer adapter: $PLATFORM_DEBIAN"
[ -f "$PLATFORM_UBUNTU" ] || fail "missing Ubuntu installer adapter: $PLATFORM_UBUNTU"
[ -f "$PLATFORM_CENTOS" ] || fail "missing CentOS installer adapter: $PLATFORM_CENTOS"
[ -f "$MANAGEMENT_RUNTIME" ] || fail "missing management runtime source: $MANAGEMENT_RUNTIME"
[ -f "$DEPLOY_COMMON" ] || fail "missing common deploy helper: $DEPLOY_COMMON"
[ -f "$INSTALL_RELEASE" ] || fail "missing release installer: $INSTALL_RELEASE"
[ -f "$WORKFLOW" ] || fail "missing release workflow: $WORKFLOW"
[ -f "$SUPPORT_POLICY" ] || fail "missing formal-support policy: $SUPPORT_POLICY"
[ -f "$SUPPORT_LEDGER" ] || fail "missing formal-support release ledger: $SUPPORT_LEDGER"
[ -f "$SUPPORT_GATE" ] || fail "missing formal-support gate: $SUPPORT_GATE"
assert_contains '"source_repository": "Kcmose/super-my"' "$SUPPORT_POLICY"
assert_contains '"first_promotable_release": "v1.2.1"' "$SUPPORT_POLICY"
assert_contains '"promotion_lineage": [' "$SUPPORT_POLICY"
assert_contains '"upgrade_from_release": "v1.2.0"' "$SUPPORT_POLICY"
assert_contains '"promotion_eligible": false' "$SUPPORT_LEDGER"
assert_contains '"release-assets"' "$SUPPORT_GATE"
assert_contains '"source-commit"' "$SUPPORT_GATE"
assert_contains '"source-tag-object"' "$SUPPORT_GATE"
assert_contains '"upgrade-from-tag-object"' "$SUPPORT_GATE"
assert_contains 'must be provided together' "$SUPPORT_GATE"
for installer_source in \
    "$INSTALLER" "$INSTALL_COMMON" "$INSTALL_GENERATOR" \
    "$PLATFORM_DEBIAN" "$PLATFORM_UBUNTU" "$PLATFORM_CENTOS"; do
    bash -n "$installer_source"
done
bash "$INSTALL_GENERATOR" --check
bash -n "$BUILDER"
bash -n "$MANAGEMENT_RUNTIME"
bash -n "$INSTALL_RELEASE"
sh -n "$0"

# These are literal source contracts and must not expand in this test process.
# shellcheck disable=SC2016
for contract in \
    '--check-platform PLATFORM_ID' \
    'require_supported_runtime_platform' \
    'validate_management_platform_id "$CHECK_PLATFORM_ID"' \
    'validate_prebuilt_bundle "$BUNDLE_ROOT" management' \
    'setup platform $CHECK_PLATFORM_ID does not match this host $RUNTIME_PLATFORM_ID'; do
    assert_contains "$contract" "$INSTALL_RELEASE"
done

CANONICAL_PLATFORM_IDS='debian-9-systemd,debian-10-systemd,debian-11-systemd,debian-12-systemd,debian-13-systemd,ubuntu-18.04-systemd,ubuntu-20.04-systemd,ubuntu-22.04-systemd,ubuntu-24.04-systemd,ubuntu-26.04-systemd,centos-linux-7-systemd,centos-linux-8-systemd,centos-stream-8-systemd,centos-stream-9-systemd,centos-stream-10-systemd'
# Positional parameters in this single-quoted program expand in the child Bash.
# shellcheck disable=SC2016
/bin/bash -c '
    source "$1"
    [[ "$PROBE_MANAGEMENT_RUNTIME_ABI" == probe-linux-systemd-v2 ]]
    [[ "$PROBE_MANAGEMENT_PLATFORM_IDS" == "$2" ]]
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
    [[ "$(management_platform_id_from_release centos 7 "$4")" == centos-linux-7-systemd ]]
    [[ "$(management_platform_id_from_release centos 8 "$4")" == centos-linux-8-systemd ]]
    [[ "$(management_platform_id_from_release centos 8 "$5")" == centos-stream-8-systemd ]]
    [[ "$(management_platform_id_from_release centos 9 "$5")" == centos-stream-9-systemd ]]
    [[ "$(management_platform_id_from_release centos 10 "$5")" == centos-stream-10-systemd ]]
    [[ "$(management_platform_nginx_dialect debian-9-systemd)" == classic ]]
    [[ "$(management_platform_nginx_dialect debian-12-systemd)" == legacy ]]
    [[ "$(management_platform_nginx_dialect debian-13-systemd)" == modern ]]
    [[ "$(management_platform_systemd_profile debian-11-systemd)" == legacy ]]
    [[ "$(management_platform_systemd_profile debian-12-systemd)" == modern ]]
    [[ "$(management_platform_systemd_minimum centos-linux-7-systemd)" == 219 ]]
    [[ "$(management_platform_systemd_minimum ubuntu-26.04-systemd)" == 259 ]]
    [[ "$(management_platform_package_family centos-stream-9-systemd)" == rpm ]]
    [[ "$(management_platform_postgres_service centos-stream-9-systemd)" == postgresql-14.service ]]
    [[ "$(management_platform_postgres_service debian-10-systemd)" == postgresql.service ]]
    [[ "$(management_platform_certbot_timer centos-stream-9-systemd)" == certbot-renew.timer ]]
    [[ "$(management_platform_certbot_timer ubuntu-20.04-systemd)" == certbot.timer ]]
    [[ "$(management_platform_postgres_bin_dir centos-stream-9-systemd)" == /usr/pgsql-14/bin ]]
    [[ "$(management_platform_postgres_bin_dir ubuntu-20.04-systemd)" == /usr/bin ]]
    if uninitialized_pg_dump="$(runtime_postgres_command pg_dump 2>/dev/null)"; then
        exit 39
    fi
    initialize_runtime_platform_contract centos-stream-9-systemd
    [[ "$(runtime_postgres_command pg_dump)" == /usr/pgsql-14/bin/pg_dump ]]
    [[ "$(runtime_postgres_command pg_restore)" == /usr/pgsql-14/bin/pg_restore ]]
    [[ "$(runtime_postgres_command psql)" == /usr/pgsql-14/bin/psql ]]
    RUNTIME_PLATFORM_ID=
    RUNTIME_SYSTEMD_PROFILE=
    RUNTIME_POSTGRES_SERVICE=
    RUNTIME_CERTBOT_TIMER=
    initialize_runtime_platform_contract debian-10-systemd
    [[ "$(runtime_postgres_command pg_dump)" == /usr/bin/pg_dump ]]
    [[ "$(runtime_postgres_command psql)" == /usr/bin/psql ]]
    if ( runtime_postgres_command pg_isready >/dev/null 2>&1 ); then
        exit 40
    fi
    [[ -z "$uninitialized_pg_dump" ]]
    [[ "$(parse_management_os_release_token ID "$3")" == ubuntu ]]
' probe-runtime-platform-map "$DEPLOY_COMMON" "$CANONICAL_PLATFORM_IDS" '"ubuntu"' \
    'CentOS Linux' 'CentOS Stream' ||
    fail 'management runtime platform mapping rejected the reviewed 15-platform matrix'
# Positional parameters in these single-quoted programs expand in the child Bash.
# shellcheck disable=SC2016
if /bin/bash -c 'source "$1"; management_platform_id_from_release debian 14' \
    probe-runtime-platform-reject "$DEPLOY_COMMON" >/dev/null 2>&1; then
    fail 'management runtime platform mapping accepted Debian 14'
fi
# shellcheck disable=SC2016
if /bin/bash -c 'source "$1"; management_platform_id_from_release linuxmint 22' \
    probe-runtime-derived-reject "$DEPLOY_COMMON" >/dev/null 2>&1; then
    fail 'management runtime platform mapping accepted a derived distribution'
fi
# shellcheck disable=SC2016
if /bin/bash -c 'source "$1"; management_platform_id_from_release Ubuntu 24.04' \
    probe-runtime-case-reject "$DEPLOY_COMMON" >/dev/null 2>&1; then
    fail 'management runtime platform mapping accepted a non-canonical ID case'
fi
# shellcheck disable=SC2016
if /bin/bash -c 'source "$1"; parse_management_os_release_token ID "$2"' \
    probe-runtime-quote-reject "$DEPLOY_COMMON" '"ubuntu' >/dev/null 2>&1; then
    fail 'management runtime parser accepted unmatched quotes'
fi

# Exercise the complete os-release parser used by the generated deployment
# runtime against the same canonical matrix and rejection classes as the root
# installer contract, including the NAME distinction between CentOS products.
PLATFORM_FIXTURE_ROOT=$TEST_ROOT/platform-fixtures
mkdir "$PLATFORM_FIXTURE_ROOT"
for debian_version in 9 10 11 12 13; do
    printf '%s\n' 'NAME="Debian GNU/Linux"' 'ID=debian' "VERSION_ID=\"$debian_version\"" \
        > "$PLATFORM_FIXTURE_ROOT/debian-$debian_version"
done
for ubuntu_version in 18.04 20.04 22.04 24.04 26.04; do
    printf '%s\n' 'NAME="Ubuntu"' 'ID=ubuntu' "VERSION_ID=\"$ubuntu_version\"" \
        > "$PLATFORM_FIXTURE_ROOT/ubuntu-$ubuntu_version"
done
printf '%s\n' 'NAME="CentOS Linux"' 'ID=centos' 'VERSION_ID="7"' > "$PLATFORM_FIXTURE_ROOT/centos-linux-7"
printf '%s\n' 'NAME="CentOS Linux"' 'ID=centos' 'VERSION_ID="8"' > "$PLATFORM_FIXTURE_ROOT/centos-linux-8"
for stream_version in 8 9 10; do
    printf '%s\n' 'NAME="CentOS Stream"' 'ID=centos' "VERSION_ID=\"$stream_version\"" \
        > "$PLATFORM_FIXTURE_ROOT/centos-stream-$stream_version"
done
while IFS='|' read -r fixture_name expected_platform expected_dialect expected_profile \
    expected_systemd expected_family expected_postgres expected_certbot; do
    [ -n "$fixture_name" ] || continue
    # Positional parameters expand in the child Bash, not in this contract.
    # shellcheck disable=SC2016
    /bin/bash -c '
        source "$1"
        platform_id="$(management_platform_id_from_os_release "$2")"
        [[ "$platform_id" == "$3" ]]
        [[ "$(management_platform_nginx_dialect "$platform_id")" == "$4" ]]
        [[ "$(management_platform_systemd_profile "$platform_id")" == "$5" ]]
        [[ "$(management_platform_systemd_minimum "$platform_id")" == "$6" ]]
        [[ "$(management_platform_package_family "$platform_id")" == "$7" ]]
        [[ "$(management_platform_postgres_service "$platform_id")" == "$8" ]]
        [[ "$(management_platform_certbot_timer "$platform_id")" == "$9" ]]
    ' probe-runtime-os-release "$DEPLOY_COMMON" "$PLATFORM_FIXTURE_ROOT/$fixture_name" \
        "$expected_platform" "$expected_dialect" "$expected_profile" "$expected_systemd" \
        "$expected_family" "$expected_postgres" "$expected_certbot" ||
        fail "management runtime os-release fixture was mapped incorrectly: $fixture_name"
done <<'EOF'
debian-9|debian-9-systemd|classic|legacy|232|deb|postgresql.service|certbot.timer
debian-10|debian-10-systemd|legacy|legacy|241|deb|postgresql.service|certbot.timer
debian-11|debian-11-systemd|legacy|legacy|247|deb|postgresql.service|certbot.timer
debian-12|debian-12-systemd|legacy|modern|252|deb|postgresql.service|certbot.timer
debian-13|debian-13-systemd|modern|modern|257|deb|postgresql.service|certbot.timer
ubuntu-18.04|ubuntu-18.04-systemd|legacy|legacy|237|deb|postgresql.service|certbot.timer
ubuntu-20.04|ubuntu-20.04-systemd|legacy|legacy|245|deb|postgresql.service|certbot.timer
ubuntu-22.04|ubuntu-22.04-systemd|legacy|modern|249|deb|postgresql.service|certbot.timer
ubuntu-24.04|ubuntu-24.04-systemd|legacy|modern|255|deb|postgresql.service|certbot.timer
ubuntu-26.04|ubuntu-26.04-systemd|modern|modern|259|deb|postgresql.service|certbot.timer
centos-linux-7|centos-linux-7-systemd|classic|legacy|219|rpm|postgresql-14.service|certbot-renew.timer
centos-linux-8|centos-linux-8-systemd|legacy|legacy|239|rpm|postgresql-14.service|certbot-renew.timer
centos-stream-8|centos-stream-8-systemd|legacy|legacy|239|rpm|postgresql-14.service|certbot-renew.timer
centos-stream-9|centos-stream-9-systemd|legacy|modern|252|rpm|postgresql-14.service|certbot-renew.timer
centos-stream-10|centos-stream-10-systemd|modern|modern|257|rpm|postgresql-14.service|certbot-renew.timer
EOF

mkdir -p "$PLATFORM_FIXTURE_ROOT/symlink/etc" "$PLATFORM_FIXTURE_ROOT/symlink/usr/lib"
printf '%s\n' 'ID=debian' 'VERSION_ID="13"' > "$PLATFORM_FIXTURE_ROOT/symlink/usr/lib/os-release"
ln -s ../usr/lib/os-release "$PLATFORM_FIXTURE_ROOT/symlink/etc/os-release"
# Positional parameters expand in the child Bash, not in this contract.
# shellcheck disable=SC2016
/bin/bash -c '
    source "$1"
    [[ "$(management_platform_id_from_os_release "$2")" == debian-13-systemd ]]
' probe-runtime-os-release-symlink "$DEPLOY_COMMON" \
    "$PLATFORM_FIXTURE_ROOT/symlink/etc/os-release" ||
    fail 'management runtime rejected a standards-compliant relative os-release symlink'

printf '%s\n' 'ID=debian' 'VERSION_ID="14"' > "$PLATFORM_FIXTURE_ROOT/debian-14"
printf '%s\n' 'ID=ubuntu' 'VERSION_ID="18.10"' > "$PLATFORM_FIXTURE_ROOT/ubuntu-18.10"
printf '%s\n' 'ID=ubuntu' 'VERSION_ID="28.04"' > "$PLATFORM_FIXTURE_ROOT/ubuntu-28.04"
printf '%s\n' 'NAME="CentOS Linux"' 'ID=centos' 'VERSION_ID="9"' > "$PLATFORM_FIXTURE_ROOT/centos-linux-9"
printf '%s\n' 'NAME="CentOS Stream"' 'ID=centos' 'VERSION_ID="7"' > "$PLATFORM_FIXTURE_ROOT/centos-stream-7"
printf '%s\n' 'NAME="CentOS Stream"' 'ID=centos' 'VERSION_ID="11"' > "$PLATFORM_FIXTURE_ROOT/centos-stream-11"
printf '%s\n' 'ID=centos' 'VERSION_ID="8"' > "$PLATFORM_FIXTURE_ROOT/centos-missing-name"
printf '%s\n' 'NAME="Rocky Linux"' 'ID=centos' 'VERSION_ID="8"' > "$PLATFORM_FIXTURE_ROOT/centos-wrong-name"
printf '%s\n' 'NAME="CentOS Linux"' 'NAME="CentOS Stream"' 'ID=centos' 'VERSION_ID="8"' > "$PLATFORM_FIXTURE_ROOT/duplicate-name"
printf '%s\n' 'ID=Ubuntu' 'VERSION_ID="24.04"' > "$PLATFORM_FIXTURE_ROOT/noncanonical-case"
printf '%s\n' 'ID=ubuntu' > "$PLATFORM_FIXTURE_ROOT/missing-version"
printf '%s\n' 'ID=ubuntu' 'ID=debian' 'VERSION_ID="24.04"' > "$PLATFORM_FIXTURE_ROOT/duplicate-id"
printf '%s\n' 'ID=linuxmint' 'VERSION_ID="22"' 'ID_LIKE="ubuntu debian"' > "$PLATFORM_FIXTURE_ROOT/derived-id-like"
for rejected_fixture in \
    debian-14 ubuntu-18.10 ubuntu-28.04 centos-linux-9 centos-stream-7 centos-stream-11 \
    centos-missing-name centos-wrong-name duplicate-name noncanonical-case \
    missing-version duplicate-id derived-id-like; do
    # Positional parameters expand in the child Bash, not in this contract.
    # shellcheck disable=SC2016
    if /bin/bash -c '
        source "$1"
        management_platform_id_from_os_release "$2"
    ' probe-runtime-os-release-reject "$DEPLOY_COMMON" \
        "$PLATFORM_FIXTURE_ROOT/$rejected_fixture" >/dev/null 2>&1; then
        fail "management runtime accepted an invalid os-release fixture: $rejected_fixture"
    fi
done
RUNTIME_PLATFORM_EXECUTION_MARKER=$TEST_ROOT/runtime-os-release-was-executed
printf '%s\n' \
    "ID=\$(touch $RUNTIME_PLATFORM_EXECUTION_MARKER)" \
    'VERSION_ID="13"' \
    > "$PLATFORM_FIXTURE_ROOT/shell-syntax"
# Positional parameters expand in the child Bash, not in this contract.
# shellcheck disable=SC2016
if /bin/bash -c '
    source "$1"
    management_platform_id_from_os_release "$2"
' probe-runtime-os-release-data-only "$DEPLOY_COMMON" \
    "$PLATFORM_FIXTURE_ROOT/shell-syntax" >/dev/null 2>&1; then
    fail 'management runtime accepted executable shell syntax in os-release data'
fi
[ ! -e "$RUNTIME_PLATFORM_EXECUTION_MARKER" ] ||
    fail 'management runtime executed shell syntax from os-release instead of parsing data'
RUNTIME_PLATFORM_NAME_EXECUTION_MARKER=$TEST_ROOT/runtime-os-release-name-was-executed
printf '%s\n' \
    "NAME=\$(touch $RUNTIME_PLATFORM_NAME_EXECUTION_MARKER)" \
    'ID=centos' \
    'VERSION_ID="8"' \
    > "$PLATFORM_FIXTURE_ROOT/name-shell-syntax"
# Positional parameters expand in the child Bash, not in this contract.
# shellcheck disable=SC2016
if /bin/bash -c '
    source "$1"
    management_platform_id_from_os_release "$2"
' probe-runtime-os-release-name-data-only "$DEPLOY_COMMON" \
    "$PLATFORM_FIXTURE_ROOT/name-shell-syntax" >/dev/null 2>&1; then
    fail 'management runtime accepted executable shell syntax in os-release NAME data'
fi
[ ! -e "$RUNTIME_PLATFORM_NAME_EXECUTION_MARKER" ] ||
    fail 'management runtime executed shell syntax from os-release NAME instead of parsing data'

help_output=$(bash "$BUILDER" --help)
for option in --output-dir --profile --version; do
    printf '%s\n' "$help_output" | grep -Fq -- "$option" || fail "help is missing $option"
done
printf '%s\n' "$help_output" | grep -Fq 'without uploading anything' ||
    fail 'help must state that the builder never uploads'
printf '%s\n' "$help_output" | grep -Fq 'Sources are fixed to the Kcmose/super-my repository' ||
    fail 'help must state that source repositories are fixed'
printf '%s\n' "$help_output" | grep -Fq 'v1.2 accepts management only' ||
    fail 'help must state that the current builder is management-only'

if bash "$BUILDER" --unknown-option >/dev/null 2>&1; then
    fail 'builder accepted an unknown option'
fi
if bash "$BUILDER" --version v1.0.1 >/dev/null 2>&1; then
    fail 'builder accepted an unreviewed server version'
fi
if bash "$BUILDER" --profile unsupported >/dev/null 2>&1; then
    fail 'builder accepted an unsupported release profile'
fi
if bash "$BUILDER" --profile full >/dev/null 2>&1; then
    fail 'v1.2 builder accepted the historical full release profile'
fi
for option in --admin-root --web-root --agent-root; do
    if bash "$BUILDER" "$option" /tmp/untrusted-source >/dev/null 2>&1; then
        fail "builder accepted forbidden source override: $option"
    fi
done

# These are literal source-code contracts and must not expand in this process.
# shellcheck disable=SC2016
for contract in \
    'release bundles must be built on Debian 13' \
    'PROFILE="management"' \
    'MANAGEMENT_VERSION="v1.2.0"' \
    'FULL_VERSION="v1.1.0"' \
    'MANAGEMENT_SUPER_MY_REF="refs/tags/v1.2.0"' \
    'MANAGEMENT_RUNTIME_ABI="probe-linux-systemd-v2"' \
    'MANAGEMENT_PLATFORM_IDS="debian-9-systemd,debian-10-systemd,debian-11-systemd,debian-12-systemd,debian-13-systemd,ubuntu-18.04-systemd,ubuntu-20.04-systemd,ubuntu-22.04-systemd,ubuntu-24.04-systemd,ubuntu-26.04-systemd,centos-linux-7-systemd,centos-linux-8-systemd,centos-stream-8-systemd,centos-stream-9-systemd,centos-stream-10-systemd"' \
    'FULL_SUPER_MY_REF="refs/tags/v1.1.0"' \
    'WEB_REF="refs/tags/v1.0.0"' \
    'AGENT_REF="refs/tags/v1.0.2"' \
    'SOURCE_REPOSITORY="Kcmose/super-my"' \
    'SUPER_MY_REMOTE="https://github.com/${SOURCE_REPOSITORY}.git"' \
    'WEB_REMOTE="https://github.com/Kcmose/my.git"' \
    'AGENT_REMOTE="https://github.com/Kcmose/my-agent.git"' \
    'readonly ADMIN_ROOT WEB_ROOT AGENT_ROOT' \
    'validate_fixed_repository_sources' \
    'printf -v "$commit_output"' \
    'readonly SOURCE_COMMIT SOURCE_TAG_OBJECT' \
    'readonly WEB_SOURCE_COMMIT WEB_SOURCE_TAG_OBJECT' \
    'readonly AGENT_SOURCE_COMMIT AGENT_SOURCE_TAG_OBJECT' \
    'trusted_git() (' \
    'unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_OBJECT_DIRECTORY' \
    'unset GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_REPLACE_REF_BASE GIT_NAMESPACE' \
    'unset GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS GIT_ATTR_NOSYSTEM' \
    'export GIT_NO_REPLACE_OBJECTS=1' \
    'command git --no-replace-objects "$@"' \
    'install.sh install/common.sh install/build-standalone.sh' \
    'install/platforms/debian.sh install/platforms/ubuntu.sh install/platforms/centos.sh' \
    'bash "$super_root/install/build-standalone.sh" --check' \
    'stage_repository_commit super-my "$SUPER_MY_ROOT" "$SOURCE_COMMIT" "$pristine_super_source"' \
    'cp -a -- "$pristine_super_source" "$super_source"' \
    'trusted_git -C "$root" rev-parse --git-path objects' \
    'source_object_dir="$root/$source_object_dir"' \
    'GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_ATTR_NOSYSTEM=1' \
    'trusted_git -c core.attributesFile=/dev/null' \
    'init --bare --template=' \
    '"$isolated_git/objects/info/alternates"' \
    'trusted_git -c core.attributesFile=/dev/null --git-dir="$isolated_git"' \
    'cat-file -e "${commit}^{commit}"' \
    'archive --format=tar --output="$archive_path" "$commit"' \
    'tar -xf "$archive_path" -C "$snapshot_tree"' \
    'mv -T -- "$snapshot_tree" "$destination"' \
    '"$snapshot_work" == "$WORK_ROOT"/.probe-source-snapshot.*' \
    'rm -rf -- "$snapshot_work"' \
    '"$super_source/install/build-standalone.sh"' \
    '"$super_source/install/common.sh"' \
    '"$super_source"/install/platforms/*.sh' \
    'if [[ "$PROFILE" == full ]]; then' \
    'trusted_git -c core.fsmonitor=false -c core.untrackedCache=false' \
    '-C "$root" status --porcelain=v1 --untracked-files=all' \
    'trusted_git -C "$root" rev-parse --verify "${ref}^{commit}"' \
    'trusted_git -C / ls-remote --exit-code "$expected_remote" "$ref" "${ref}^{}"' \
    'remote tag object does not exactly equal the pinned local tag' \
    'remote tag does not exactly equal the verified commit' \
    'revalidate_remote_source_refs' \
    'SOURCE_TAG_OBJECT' \
    'source_input_digest() (' \
    'assert_source_inputs_unchanged()' \
    'revalidate_all_source_inputs' \
    'npm test' \
    'npm audit --omit=dev --audit-level=high' \
    'npm run build' \
    'testing and auditing $label after capturing its build artifact' \
    'assert_management_setup_artifact "$bundle_root/artifacts/admin"' \
    'management administrator artifact contains a historical multi-product setup control' \
    'cmd/probe-support-gate/main.go' \
    'internal/supportevidence/ledger.go internal/supportevidence/ledger_test.go' \
    'deploy/support/policy-v1.json deploy/support/releases/v1.2.0.json' \
    'go run ./cmd/probe-support-gate verify' \
    '--support-root deploy/support --release "$VERSION"' \
    '--require-zero-supported' \
    'go test -count=1 ./...' \
    'go vet ./...' \
    'sh probe-api/deploy/tests/build-release-bundles-contract.sh' \
    'sh probe-api/deploy/tests/management-lifecycle-contract.sh' \
    'GOARCH="$architecture"' \
    './cmd/probe-api' \
    './cmd/probe-setup' \
    './cmd/probe-agent' \
    'artifacts/api/probe-api' \
    'setup/probe-setup' \
    'artifacts/migrations' \
    'source/probe-api/config' \
    'source/probe-api/deploy' \
    'config/probe-postgres-backup.env.example' \
    'deploy/scripts/install-release.sh deploy/scripts/validate-management.sh' \
    'deploy/scripts/restore-management.sh deploy/scripts/uninstall-management.sh' \
    'deploy/scripts/backup-postgres.sh' \
    'deploy/scripts/deploy-common-management-runtime.sh' \
    'deploy/scripts/restore-postgres.sh' \
    'deploy/systemd/probe-api.service deploy/systemd/probe-postgres-backup.service' \
    'deploy/systemd/probe-postgres-backup.timer' \
    'systemd/probe-api-legacy.service' \
    'systemd/probe-postgres-backup-legacy.service' \
    'systemd/probe-postgres-backup-legacy.timer' \
    'deploy/nginx/nginx.conf deploy/nginx/nginx-ip.conf' \
    'deploy/nginx/nginx-management.conf deploy/nginx/nginx-management-ip.conf' \
    'deploy/nginx/nginx-management-legacy.conf deploy/nginx/nginx-management-ip-legacy.conf' \
    'nginx/nginx-management-classic.conf' \
    'nginx/nginx-management-ip-classic.conf' \
    'deploy/setup/probe-panel-finalizer-management.service' \
    'setup/probe-panel-finalizer-management-legacy.service' \
    'setup/probe-panel-setup-legacy.service' \
    'setup/probe-panel-setup-legacy.socket' \
    '$bundle_root/source/probe-api/deploy/scripts/install-release.sh' \
    'copy_management_runtime_script' \
    'runtime_functions=' \
    'could not extract the reviewed management runtime header' \
    'generated management runtime contains an unreviewed ProtectSystem=full occurrence' \
    'ProtectSystem=reviewed-legacy-hardening' \
    '$0 !~ /^MANAGEMENT_ROLLBACK_(RELEASE_PROFILE|OLD_AGENT|OLD_WEB)=/' \
    'parse_management_os_release_token parse_management_os_release_name management_platform_id_from_release' \
    'management_platform_systemd_profile management_platform_systemd_minimum' \
    'management_platform_postgres_service management_platform_certbot_timer management_platform_postgres_bin_dir' \
    'assert_runtime_platform_contract initialize_runtime_platform_contract runtime_platform_id runtime_systemd_profile' \
    'runtime_postgres_service runtime_certbot_timer runtime_account_family runtime_postgres_command' \
    'acquire_root_lock acquire_deployment_lock' \
    'cat "$management_source" >> "$destination"' \
    'reviewed management runtime function set' \
    'shellcheck "$destination"' \
    'generated management runtime deploy helper failed its platform contract self-test' \
    'uninitialized_pg_dump="$(runtime_postgres_command pg_dump 2>/dev/null)"' \
    'artifacts/(agent|web)' \
    'PROBE_(AGENT|WEB)_DIR' \
    'old_(agent|web)' \
    '/srv/probe/(agent|web)' \
    'management runtime deploy helper still contains forbidden full, Agent-artifact, visitor, or build logic' \
    'RELEASE-MANIFEST' \
    'profile=${profile}' \
    'runtime_abi=${MANAGEMENT_RUNTIME_ABI}' \
    'platform_ids=${MANAGEMENT_PLATFORM_IDS}' \
    'source_repository=${SOURCE_REPOSITORY}' \
    'source_commit=${SOURCE_COMMIT}' \
    'source_tag_object=${SOURCE_TAG_OBJECT}' \
    'super_my_ref=${SUPER_MY_REF}' \
    '"$common_root" "$pristine_api_source" "$publish_root" "$PROFILE"' \
    '$bundle_root/source/probe-api/deploy/nginx/nginx.conf' \
    '$bundle_root/source/probe-api/deploy/nginx/nginx-management.conf' \
    'BUNDLE-SHA256SUMS' \
    'bundle_prefix="probe-panel-management"' \
    '"${bundle_prefix}-${VERSION}-linux-amd64.tar.gz"' \
    '"${bundle_prefix}-${VERSION}-linux-arm64.tar.gz"' \
    'management profile' \
    'sha256sum --check --strict' \
    "-name '*.map'" \
    'contains a symbolic link' \
    'contains generated source pollution' \
    '.probe-release-build.' \
    'trap cleanup EXIT' \
    'mkdir -m 0700 -- "$WORK_ROOT"' \
    'publish_output_directory "$publish_root" "$OUTPUT_DIR"' \
    'python3 -I -S - "$source" "$destination"' \
    'renameat2 = getattr(libc, "renameat2", None)' \
    'RENAME_NOREPLACE = 1' \
    'release output could not be atomically published without overwriting' \
    'atomic release publication did not produce a real output directory' \
    'rm -rf -- "$WORK_ROOT"' \
    'No GitHub release was created or modified.'; do
    assert_contains "$contract" "$BUILDER"
done

assert_contains '"$api_source_root/deploy/scripts/deploy-common.sh"' "$BUILDER"

# Exercise the actual runtime extractor. This catches drift between the
# allowlisted functions and the final management-only scan before the release
# builder recursively reaches this contract during a full candidate build.
GENERATED_MANAGEMENT_RUNTIME=$TEST_ROOT/generated-management-runtime.sh
/bin/bash -c '
    set -Eeuo pipefail
    source "$1"
    copy_management_runtime_script "$2" "$3" "$4"
    [[ -f "$4" && ! -L "$4" ]]
    [[ "$(grep -Foc "ProtectSystem=full" "$4")" == 2 ]]
    grep -Fxq "        grep -Fxq '\''ProtectSystem=full'\'' \"\$unit_file\" || die \"legacy probe-api unit must protect the system\"" "$4"
    grep -Fxq "        grep -Fxq '\''ProtectSystem=full'\'' \"\$service_file\" ||" "$4"
    ! grep -Fq "full-source" "$4"
' probe-runtime-extractor-contract "$BUILDER" "$DEPLOY_COMMON" \
    "$MANAGEMENT_RUNTIME" "$GENERATED_MANAGEMENT_RUNTIME" ||
    fail 'management runtime extractor rejected its reviewed systemd legacy hardening checks'

BOOTSTRAP_MARKER_ROOT=$TEST_ROOT/bootstrap-managed-release
mkdir "$BOOTSTRAP_MARKER_ROOT"
: > "$BOOTSTRAP_MARKER_ROOT/.probe-panel-bootstrap-managed"
chmod 0600 "$BOOTSTRAP_MARKER_ROOT/.probe-panel-bootstrap-managed"
run_bootstrap_marker_validation() {
    expected_owner=$1
    expected_group=$2
    PROBE_TEST_MARKER_OWNER=$expected_owner \
    PROBE_TEST_MARKER_GROUP=$expected_group \
    /bin/bash -c '
        set -Eeuo pipefail
        stat() {
            if [[ ${1:-} == -c ]]; then
                case ${2:-} in
                    %U)
                        printf "%s\n" "$PROBE_TEST_MARKER_OWNER"
                        return 0
                        ;;
                    %U:%G)
                        printf "%s:%s\n" "$PROBE_TEST_MARKER_OWNER" "$PROBE_TEST_MARKER_GROUP"
                        return 0
                        ;;
                esac
            fi
            command stat "$@"
        }
        source "$1"
        validate_bootstrap_managed_release_marker "$2"
    ' probe-bootstrap-marker-validation "$GENERATED_MANAGEMENT_RUNTIME" "$BOOTSTRAP_MARKER_ROOT"
}

run_bootstrap_marker_validation root root ||
    fail 'management runtime rejected the exact root-owned bootstrap release marker'

if run_bootstrap_marker_validation nobody root >/dev/null 2>&1; then
    fail 'management runtime accepted a bootstrap release marker not owned by root'
fi
if run_bootstrap_marker_validation root nogroup >/dev/null 2>&1; then
    fail 'management runtime accepted a bootstrap release marker not grouped to root'
fi

printf '%s\n' tainted > "$BOOTSTRAP_MARKER_ROOT/.probe-panel-bootstrap-managed"
if run_bootstrap_marker_validation root root >/dev/null 2>&1; then
    fail 'management runtime accepted a non-empty bootstrap release marker'
fi
: > "$BOOTSTRAP_MARKER_ROOT/.probe-panel-bootstrap-managed"
chmod 0644 "$BOOTSTRAP_MARKER_ROOT/.probe-panel-bootstrap-managed"
if run_bootstrap_marker_validation root root >/dev/null 2>&1; then
    fail 'management runtime accepted a public bootstrap release marker'
fi
rm -f -- "$BOOTSTRAP_MARKER_ROOT/.probe-panel-bootstrap-managed"
ln -s /dev/null "$BOOTSTRAP_MARKER_ROOT/.probe-panel-bootstrap-managed"
if run_bootstrap_marker_validation root root >/dev/null 2>&1; then
    fail 'management runtime accepted a symbolic-link bootstrap release marker'
fi
rm -f -- "$BOOTSTRAP_MARKER_ROOT/.probe-panel-bootstrap-managed"
if run_bootstrap_marker_validation root root >/dev/null 2>&1; then
    fail 'management runtime accepted a missing bootstrap release marker'
fi

TAINTED_MANAGEMENT_RUNTIME=$TEST_ROOT/tainted-management-runtime.sh
cp -- "$MANAGEMENT_RUNTIME" "$TAINTED_MANAGEMENT_RUNTIME"
printf '\n%s\n' '# independent full release profile concern' >> "$TAINTED_MANAGEMENT_RUNTIME"
if /bin/bash -c '
    set -Eeuo pipefail
    source "$1"
    copy_management_runtime_script "$2" "$3" "$4"
' probe-runtime-full-reject "$BUILDER" "$DEPLOY_COMMON" \
    "$TAINTED_MANAGEMENT_RUNTIME" "$TEST_ROOT/tainted-generated.sh" \
    >/dev/null 2>&1; then
    fail 'management runtime extractor accepted an independent full-profile concern'
fi

EXTRA_HARDENING_RUNTIME=$TEST_ROOT/extra-hardening-runtime.sh
cp -- "$MANAGEMENT_RUNTIME" "$EXTRA_HARDENING_RUNTIME"
printf '\n%s\n' '# ProtectSystem=full' >> "$EXTRA_HARDENING_RUNTIME"
if /bin/bash -c '
    set -Eeuo pipefail
    source "$1"
    copy_management_runtime_script "$2" "$3" "$4"
' probe-runtime-hardening-reject "$BUILDER" "$DEPLOY_COMMON" \
    "$EXTRA_HARDENING_RUNTIME" "$TEST_ROOT/extra-hardening-generated.sh" \
    >/dev/null 2>&1; then
    fail 'management runtime extractor accepted an unreviewed ProtectSystem=full occurrence'
fi

BROKEN_BOUNDARY_COMMON=$TEST_ROOT/broken-boundary-deploy-common.sh
sed 's/^cleanup_deploy_work_root() {/missing_deploy_work_root_boundary() {/' \
    "$DEPLOY_COMMON" > "$BROKEN_BOUNDARY_COMMON"
if /bin/bash -c '
    set -Eeuo pipefail
    source "$1"
    copy_management_runtime_script "$2" "$3" "$4"
' probe-runtime-boundary-reject "$BUILDER" "$BROKEN_BOUNDARY_COMMON" \
    "$MANAGEMENT_RUNTIME" "$TEST_ROOT/broken-boundary-generated.sh" \
    >"$TEST_ROOT/broken-boundary.stdout" 2>"$TEST_ROOT/broken-boundary.stderr"; then
    fail 'management runtime extractor accepted a missing header boundary'
fi
grep -Fq 'could not extract the reviewed management runtime header' \
    "$TEST_ROOT/broken-boundary.stderr" ||
    fail 'management runtime extractor did not fail closed at its missing header boundary'

if grep -Eq '^[[:space:]]*(cp|install)[[:space:]].*\$(\{)?(SUPER_MY_ROOT|ADMIN_ROOT|WEB_ROOT|AGENT_ROOT)' "$BUILDER"; then
    fail 'release builder must not stage source content from a live working tree'
fi
if grep -Eq '^[[:space:]]*(copy_frontend_source|copy_project_source)\(\)' "$BUILDER"; then
    fail 'release builder still defines a working-tree source copier'
fi
if grep -Eq '^[[:space:]]*git[[:space:]]' "$BUILDER"; then
    fail 'release builder bypasses the trusted Git environment wrapper'
fi
# This is a literal builder variable, not a contract-test expansion.
# shellcheck disable=SC2016
if grep -Fq -- '--worktree-attributes' "$BUILDER" ||
    grep -Fq 'git -C "$root" archive' "$BUILDER"; then
    fail 'release builder must archive through its isolated Git object view'
fi

# Prove the archive path itself ignores refs/replace, rather than accepting a
# collection of implementation strings that could be disconnected from the
# actual Git invocation.
REPLACE_FIXTURE=$TEST_ROOT/replace-fixture
/bin/bash -c '
    set -Eeuo pipefail
    source "$1"
    repository=$2
    work_root=$3
    mkdir -p "$repository" "$work_root"
    git -C "$repository" init -q
    printf "%s\n" original > "$repository/payload"
    git -C "$repository" add payload
    git -C "$repository" -c user.name=contract -c user.email=contract@example.invalid \
        commit -q -m original
    original_commit=$(git -C "$repository" rev-parse HEAD)
    printf "%s\n" replacement > "$repository/payload"
    git -C "$repository" add payload
    git -C "$repository" -c user.name=contract -c user.email=contract@example.invalid \
        commit -q -m replacement
    replacement_commit=$(git -C "$repository" rev-parse HEAD)
    git -C "$repository" replace "$original_commit" "$replacement_commit"
    [[ "$(git -C "$repository" show "$original_commit:payload")" == replacement ]]
    WORK_ROOT=$work_root
    stage_repository_commit replace-fixture "$repository" "$original_commit" "$work_root/snapshot"
    [[ "$(cat "$work_root/snapshot/payload")" == original ]]
' probe-release-replace-contract "$BUILDER" "$REPLACE_FIXTURE/repository" \
    "$REPLACE_FIXTURE/work" || fail 'git replace changed the archived verified commit snapshot'

INPUT_FIXTURE=$TEST_ROOT/input-integrity-fixture
/bin/bash -c '
    set -Eeuo pipefail
    source "$1"
    source_root=$2
    mkdir -p "$source_root"
    printf "%s\n" reviewed > "$source_root/source.txt"
    expected=$(source_input_digest "$source_root")
    assert_source_inputs_unchanged fixture "$source_root" "$expected"
    mkdir "$source_root/dist"
    printf "%s\n" generated > "$source_root/dist/index.html"
    assert_source_inputs_unchanged fixture "$source_root" "$expected"
    chmod 0644 "$source_root/source.txt"
    if ( assert_source_inputs_unchanged fixture "$source_root" "$expected" \
        >/dev/null 2>&1 ); then
        exit 40
    fi
    chmod 0600 "$source_root/source.txt"
    printf "%s\n" polluted > "$source_root/source.txt"
    if ( assert_source_inputs_unchanged fixture "$source_root" "$expected" \
        >/dev/null 2>&1 ); then
        exit 41
    fi
' probe-release-input-contract "$BUILDER" "$INPUT_FIXTURE" ||
    fail 'source input digest did not distinguish generated output from source pollution'

# Exercise the same kernel-enforced no-replace path used by the builder. Every
# destination kind must survive byte-for-byte; there is no check-then-rename or
# cross-filesystem copy fallback.
PUBLISH_FIXTURE=$TEST_ROOT/publish-fixture
/bin/bash -c '
    set -Eeuo pipefail
    source "$1"
    root=$2
    mkdir -p "$root/staged-collision" "$root/existing"
    printf "%s\n" staged > "$root/staged-collision/candidate"
    printf "%s\n" preserve > "$root/existing/sentinel"
    if ( publish_output_directory "$root/staged-collision" "$root/existing" \
        >/dev/null 2>&1 ); then
        exit 50
    fi
    [[ -f "$root/staged-collision/candidate" ]]
    [[ "$(cat "$root/existing/sentinel")" == preserve ]]
    [[ ! -e "$root/existing/candidate" ]]

    mkdir -p "$root/staged-empty-directory" "$root/existing-empty-directory"
    printf "%s\n" staged > "$root/staged-empty-directory/candidate"
    if ( publish_output_directory "$root/staged-empty-directory" \
        "$root/existing-empty-directory" >/dev/null 2>&1 ); then
        exit 51
    fi
    [[ -f "$root/staged-empty-directory/candidate" ]]
    [[ -d "$root/existing-empty-directory" && ! -L "$root/existing-empty-directory" ]]
    [[ -z "$(find "$root/existing-empty-directory" -mindepth 1 -print -quit)" ]]

    mkdir -p "$root/staged-symlink" "$root/symlink-target"
    printf "%s\n" staged > "$root/staged-symlink/candidate"
    printf "%s\n" preserve > "$root/symlink-target/sentinel"
    ln -s "$root/symlink-target" "$root/existing-symlink"
    symlink_before=$(readlink "$root/existing-symlink")
    if ( publish_output_directory "$root/staged-symlink" \
        "$root/existing-symlink" >/dev/null 2>&1 ); then
        exit 52
    fi
    [[ -f "$root/staged-symlink/candidate" ]]
    [[ -L "$root/existing-symlink" ]]
    [[ "$(readlink "$root/existing-symlink")" == "$symlink_before" ]]
    [[ "$(cat "$root/existing-symlink/sentinel")" == preserve ]]

    mkdir -p "$root/staged-success"
    printf "%s\n" candidate > "$root/staged-success/candidate"
    publish_output_directory "$root/staged-success" "$root/published"
    [[ ! -e "$root/staged-success" ]]
    [[ "$(cat "$root/published/candidate")" == candidate ]]
' probe-release-publish-contract "$BUILDER" "$PUBLISH_FIXTURE" ||
    fail 'atomic release publication did not preserve a concurrent destination'

SIGNAL_FIXTURE=$TEST_ROOT/signal-cleanup-fixture
set +e
/bin/bash -c '
    set -Eeuo pipefail
    source "$1"
    mkdir -p "$2"
    WORK_ROOT="$2/.probe-release-build.signal-contract"
    trap cleanup EXIT
    trap "exit 129" HUP
    trap "exit 130" INT
    trap "exit 143" TERM
    mkdir -m 0700 -- "$WORK_ROOT"
    printf "%s\n" temporary > "$WORK_ROOT/payload"
    kill -TERM "$BASHPID"
    exit 99
' probe-release-signal-contract "$BUILDER" "$SIGNAL_FIXTURE" \
    >"$TEST_ROOT/signal-cleanup.stdout" 2>"$TEST_ROOT/signal-cleanup.stderr"
signal_status=$?
set -e
[ "$signal_status" -eq 143 ] ||
    fail "release work-root signal trap returned $signal_status instead of 143"
[ ! -e "$SIGNAL_FIXTURE/.probe-release-build.signal-contract" ] ||
    fail 'release work-root signal trap left its private directory behind'

# The sed program matches literal builder variables.
# shellcheck disable=SC2016
management_assembly="$({
    sed -n '/^[[:space:]]*if \[\[ "$profile" == management \]\]; then$/,/^[[:space:]]*else$/p' "$BUILDER"
} || true)"
printf '%s\n' "$management_assembly" | grep -Fq 'copy_management_runtime_script' ||
    fail 'management assembly must generate its dedicated runtime deploy helper'
if printf '%s\n' "$management_assembly" | grep -Fq 'build-release-bundles.sh'; then
    fail 'management assembly must not carry the CI/release builder'
fi
if printf '%s\n' "$management_assembly" | grep -Fq 'deploy/support'; then
    fail 'management assembly must not carry formal-support policy or evidence metadata'
fi

for forbidden in \
    '(^|[^[:alnum:]_])full([^[:alnum:]_]|$)' \
    'old_(agent|web)' \
    'artifacts/(agent|web)' \
    'PROBE_(AGENT|WEB)_DIR' \
    '/srv/probe/(agent|web)' \
    'build_release_artifacts' \
    'deploy_release\(\)'; do
    grep -Eiq "$forbidden" "$MANAGEMENT_RUNTIME" &&
        fail "management runtime source contains a forbidden release concern: $forbidden"
done

# This is a literal generated-function name, not a shell expansion.
runtime_function_contract=$(grep '^[[:space:]]*runtime_functions=' "$BUILDER")
if printf '%s\n' "$runtime_function_contract" | grep -Fq 'disable_default_nginx_site'; then
    fail 'management runtime function allowlist still carries default-site mutation logic'
fi
printf '%s\n' "$runtime_function_contract" | grep -Fq 'require_supported_runtime_platform' ||
    fail 'management runtime function allowlist is missing exact platform detection'
printf '%s\n' "$runtime_function_contract" | grep -Fq 'management_platform_id_from_os_release' ||
    fail 'management runtime function allowlist is missing the reviewed os-release parser'
printf '%s\n' "$runtime_function_contract" | grep -Fq 'management_platform_systemd_profile' ||
    fail 'management runtime function allowlist is missing systemd profile selection'
printf '%s\n' "$runtime_function_contract" | grep -Fq 'management_platform_postgres_service' ||
    fail 'management runtime function allowlist is missing PostgreSQL service selection'
printf '%s\n' "$runtime_function_contract" | grep -Fq 'management_platform_certbot_timer' ||
    fail 'management runtime function allowlist is missing Certbot timer selection'
printf '%s\n' "$runtime_function_contract" | grep -Fq 'management_platform_postgres_bin_dir' ||
    fail 'management runtime function allowlist is missing PostgreSQL command-directory selection'
printf '%s\n' "$runtime_function_contract" | grep -Fq 'assert_runtime_platform_contract' ||
    fail 'management runtime function allowlist is missing the fail-closed runtime contract assertion'
printf '%s\n' "$runtime_function_contract" | grep -Fq 'initialize_runtime_platform_contract' ||
    fail 'management runtime function allowlist is missing atomic runtime contract initialization'
printf '%s\n' "$runtime_function_contract" | grep -Fq 'runtime_platform_id' ||
    fail 'management runtime function allowlist is missing strict platform access'
printf '%s\n' "$runtime_function_contract" | grep -Fq 'runtime_systemd_profile' ||
    fail 'management runtime function allowlist is missing strict systemd-profile access'
printf '%s\n' "$runtime_function_contract" | grep -Fq 'runtime_account_family' ||
    fail 'management runtime function allowlist is missing strict account-family access'
printf '%s\n' "$runtime_function_contract" | grep -Fq 'runtime_postgres_command' ||
    fail 'management runtime function allowlist is missing PostgreSQL command-path selection'
if printf '%s\n' "$runtime_function_contract" | grep -Fq 'require_debian_13'; then
    fail 'management runtime function allowlist still carries the build-host Debian 13 gate'
fi

for compatibility_script in "$DEPLOY_COMMON" "$MANAGEMENT_RUNTIME"; do
    if grep -Fq -- '--value' "$compatibility_script"; then
        fail "management runtime still requires systemctl --value: $compatibility_script"
    fi
    if grep -Fq -- '--reset-env' "$compatibility_script"; then
        fail "management runtime still requires setpriv --reset-env: $compatibility_script"
    fi
    if grep -Eq 'systemctl[[:space:]]+(enable|disable)[[:space:]]+--now' "$compatibility_script"; then
        fail "management runtime still combines enablement with --now: $compatibility_script"
    fi
    if grep -Eq 'systemctl.*(postgresql(-14)?[.]service|certbot(-renew)?[.]timer)' "$compatibility_script"; then
        fail "management runtime contains a hard-coded platform service unit: $compatibility_script"
    fi
done
# These are literal runtime source contracts.
# shellcheck disable=SC2016
assert_contains 'timer_unit="$(runtime_certbot_timer)"' "$DEPLOY_COMMON"
# shellcheck disable=SC2016
assert_contains 'systemctl start "$(runtime_postgres_service)"' "$DEPLOY_COMMON"
# shellcheck disable=SC2016
assert_contains '"$(runtime_postgres_command pg_dump)" --no-password' "$DEPLOY_COMMON"
# shellcheck disable=SC2016
assert_contains '"$(runtime_postgres_command pg_restore)" --list' "$DEPLOY_COMMON"

proof_line=$(grep -n '^[[:space:]]*validate_fixed_repository_sources[[:space:]]*$' "$BUILDER" | cut -d: -f1)
[ "$(grep -c '^[[:space:]]*validate_fixed_repository_sources[[:space:]]*$' "$BUILDER")" -eq 1 ] ||
    fail 'release builder must prove each fixed repository exactly once before snapshotting its commit'
# These grep patterns match literal source invocations, including their variable syntax.
# shellcheck disable=SC2016
snapshot_line=$(grep -n '^[[:space:]]*stage_repository_commit super-my "$SUPER_MY_ROOT" "$SOURCE_COMMIT" "$pristine_super_source"$' "$BUILDER" | cut -d: -f1)
# shellcheck disable=SC2016
[ "$(grep -c '^[[:space:]]*stage_repository_commit super-my "$SUPER_MY_ROOT" "$SOURCE_COMMIT"' "$BUILDER")" -eq 1 ] ||
    fail 'release builder must read the live super-my object database exactly once'
# shellcheck disable=SC2016
work_copy_line=$(grep -n '^[[:space:]]*cp -a -- "$pristine_super_source" "$super_source"$' "$BUILDER" | cut -d: -f1)
# shellcheck disable=SC2016
snapshot_validation_line=$(grep -n '^[[:space:]]*validate_sources "$super_source" "$api_source" "$super_source"' "$BUILDER" | cut -d: -f1)
# shellcheck disable=SC2016
last_artifact_go_build_line=$(grep -n '^[[:space:]]*go build -trimpath ' "$BUILDER" | tail -n 1 | cut -d: -f1)
# shellcheck disable=SC2016
admin_build_line=$(grep -n '^[[:space:]]*build_frontend probe-admin "$super_source" "$common_root/admin"$' "$BUILDER" | cut -d: -f1)
# shellcheck disable=SC2016
web_build_line=$(grep -n '^[[:space:]]*build_frontend probe-web "$web_source" "$common_root/web"$' "$BUILDER" | cut -d: -f1)
first_shell_test_line=$(grep -n '^[[:space:]]*sh probe-api/deploy/tests/bootstrap-install-contract[.]sh$' "$BUILDER" | cut -d: -f1)
# shellcheck disable=SC2016
last_test_line=$(grep -n '^[[:space:]]*go vet ./[.][.]' "$BUILDER" | tail -n 1 | cut -d: -f1)
# Exact no-argument calls bracket manifest assembly; the function declaration
# and parameterized helper calls cannot satisfy this pattern.
remote_revalidation_lines=$(grep -n '^[[:space:]]*revalidate_remote_source_refs$' "$BUILDER" | cut -d: -f1)
[ "$(printf '%s\n' "$remote_revalidation_lines" | sed '/^$/d' | wc -l)" -eq 2 ] ||
    fail 'release builder must bracket manifest assembly with exactly two remote-ref revalidations'
remote_before_line=$(printf '%s\n' "$remote_revalidation_lines" | sed -n '1p')
remote_after_line=$(printf '%s\n' "$remote_revalidation_lines" | sed -n '2p')
input_revalidation_lines=$(grep -n '^[[:space:]]*revalidate_all_source_inputs "$super_source" "$pristine_super_source"' "$BUILDER" | cut -d: -f1)
[ "$(printf '%s\n' "$input_revalidation_lines" | sed '/^$/d' | wc -l)" -ge 6 ] ||
    fail 'release builder does not revalidate source inputs after every material build/test phase'
# This grep pattern matches a literal source invocation, including its variable syntax.
# shellcheck disable=SC2016
assemble_line=$(grep -n '^[[:space:]]*assemble_bundle "$architecture"' "$BUILDER" | tail -n 1 | cut -d: -f1)
publish_line=$(grep -n '^[[:space:]]*publish_output_directory "$publish_root" "$OUTPUT_DIR"$' "$BUILDER" | cut -d: -f1)
main_section=$(sed -n '/^main() {$/,/^}$/p' "$BUILDER")
work_path_line=$(printf '%s\n' "$main_section" | grep -n 'WORK_ROOT="${output_parent}/[.]probe-release-build[.]${BASHPID}[.]${RANDOM}${RANDOM}"' | cut -d: -f1)
work_trap_line=$(printf '%s\n' "$main_section" | grep -n '^[[:space:]]*trap cleanup EXIT$' | cut -d: -f1)
work_mkdir_line=$(printf '%s\n' "$main_section" | grep -n 'mkdir -m 0700 -- "$WORK_ROOT"' | cut -d: -f1)
input_after_tests=$(printf '%s\n' "$input_revalidation_lines" | awk -v lower="$last_test_line" -v upper="$remote_before_line" '$1 > lower && $1 < upper { print; exit }')
input_after_assembly=$(printf '%s\n' "$input_revalidation_lines" | awk -v lower="$assemble_line" -v upper="$remote_after_line" '$1 > lower && $1 < upper { print; exit }')
if [ -z "$proof_line" ] ||
    [ -z "$snapshot_line" ] ||
    [ -z "$work_copy_line" ] ||
    [ -z "$snapshot_validation_line" ] ||
    [ -z "$last_artifact_go_build_line" ] ||
    [ -z "$admin_build_line" ] ||
    [ -z "$web_build_line" ] ||
    [ -z "$first_shell_test_line" ] ||
    [ -z "$last_test_line" ] ||
    [ -z "$remote_before_line" ] ||
    [ -z "$assemble_line" ] ||
    [ -z "$remote_after_line" ] ||
    [ -z "$publish_line" ] ||
    [ -z "$work_path_line" ] ||
    [ -z "$work_trap_line" ] ||
    [ -z "$work_mkdir_line" ] ||
    [ -z "$input_after_tests" ] ||
    [ -z "$input_after_assembly" ] ||
    [ "$proof_line" -ge "$snapshot_line" ] ||
    [ "$snapshot_line" -ge "$work_copy_line" ] ||
    [ "$work_copy_line" -ge "$snapshot_validation_line" ] ||
    [ "$snapshot_validation_line" -ge "$last_artifact_go_build_line" ] ||
    [ "$last_artifact_go_build_line" -ge "$admin_build_line" ] ||
    [ "$admin_build_line" -ge "$web_build_line" ] ||
    [ "$web_build_line" -ge "$first_shell_test_line" ] ||
    [ "$first_shell_test_line" -ge "$last_test_line" ] ||
    [ "$snapshot_validation_line" -ge "$last_test_line" ] ||
    [ "$last_test_line" -ge "$remote_before_line" ] ||
    [ "$remote_before_line" -ge "$assemble_line" ] ||
    [ "$assemble_line" -ge "$remote_after_line" ] ||
    [ "$remote_after_line" -ge "$publish_line" ] ||
    [ "$work_path_line" -ge "$work_trap_line" ] ||
    [ "$work_trap_line" -ge "$work_mkdir_line" ]; then
    fail 'immutable snapshotting, artifact builds, tests, remote binding, assembly, and atomic publication are misordered'
fi

if grep -Eq '(^|[[:space:]])(gh|curl)[[:space:]].*(release (create|upload|edit)|uploads[.]github[.]com)' "$BUILDER"; then
    fail 'local release builder must not create or upload a GitHub release'
fi

# Workflow contracts deliberately contain literal GitHub shell variables.
# shellcheck disable=SC2016
for contract in \
    'push:' \
    'tags:' \
    '- v1.2.0' \
    'workflow_dispatch:' \
    "github.ref == 'refs/tags/v1.2.0') ||" \
    "(github.event_name == 'workflow_dispatch' && inputs.version == 'v1.2.0')" \
    'image: debian:13-slim' \
    'contents: read' \
    'ca-certificates curl file git gzip python3 tar xz-utils' \
    '- name: Install pinned ShellCheck 0.11.0' \
    'SHELLCHECK_ARCHIVE_SHA256: 8c3be12b05d5c177a04c29e3c78ce89ac86f1595681cab149b65b97c4e227198' \
    '[[ "$(shellcheck --version | awk' \
    'uses: actions/checkout@v4' \
    'fetch-depth: 0' \
    'persist-credentials: false' \
    'ref: refs/tags/v1.2.0' \
    'cache-dependency-path: probe-api/go.sum' \
    '[[ "$GITHUB_REPOSITORY" == Kcmose/super-my ]]' \
    'git status --porcelain=v1 --untracked-files=all' \
    'git ls-remote --exit-code origin' \
    '- name: Prepare a root-owned clean source copy' \
    'cp -a --no-preserve=ownership -- "$GITHUB_WORKSPACE" "$BUILD_SOURCE"' \
    'find "$BUILD_SOURCE" ! -uid "$(id -u)" -print -quit' \
    'bash "$BUILD_SOURCE/probe-api/deploy/scripts/build-release-bundles.sh"' \
    '--profile management' \
    '--version v1.2.0' \
    'probe-panel-management-v1.2.0-linux-amd64.tar.gz' \
    'probe-panel-management-v1.2.0-linux-arm64.tar.gz' \
    'verify_management_candidate:' \
    'needs: build_management' \
    'Verify management v1.2.0 candidate on a fresh runner' \
    'BASH_ENV: /dev/null' \
    'probe-panel-management-v1.2.0-unverified' \
    'uses: actions/download-artifact@v4' \
    'Download the unverified management build' \
    '- name: Bind candidate tarballs to the verified tag commit' \
    'trusted_git() (' \
    'source_tag_object="$(trusted_git -C "$VERIFIER_SOURCE"' \
    'https://github.com/Kcmose/super-my.git' \
    '[[ "$remote_tag_object" == "$source_tag_object" ]]' \
    'go run ./cmd/probe-support-gate verify' \
    '--release-assets "$UNVERIFIED_DOWNLOAD_ROOT"' \
    '--source-commit "$source_commit"' \
    '--source-tag-object "$source_tag_object"' \
    '- name: Stage exactly the verified candidate for upload' \
    'CANDIDATE_UPLOAD_ROOT' \
    'cmp -- "$source_asset" "$staged_asset"' \
    'path: ${{ github.workspace }}/release-upload-v1.2.0/' \
    'uses: actions/upload-artifact@v4' \
    'name: probe-panel-management-v1.2.0-candidate' \
    'if-no-files-found: error' \
    'retention-days: 14' \
    'overwrite: false' \
    'immutable' \
    'gh api' \
    'gh release download' \
    'probe-panel-v1.1.0-linux-amd64.tar.gz' \
    'probe-panel-v1.1.0-linux-arm64.tar.gz' \
    'SHA256SUMS' \
    'sha256sum --check --strict'; do
    assert_contains "$contract" "$WORKFLOW"
done

if grep -Fq 'branches:' "$WORKFLOW" || grep -Fq 'push:refs/heads/main' "$WORKFLOW"; then
    fail 'management candidate workflow must not run on branch pushes'
fi

checkout_count=$(grep -Fc 'uses: actions/checkout@' "$WORKFLOW")
[ "$checkout_count" -eq 2 ] ||
    fail 'management build and fresh verifier must each perform exactly one checkout'

candidate_copy_line=$(grep -Fn -- '- name: Prepare a root-owned clean source copy' "$WORKFLOW" | cut -d: -f1)
candidate_build_line=$(grep -Fn -- '- name: Build management-only release assets' "$WORKFLOW" | cut -d: -f1)
candidate_shape_line=$(grep -Fn -- '- name: Require exactly the three management assets' "$WORKFLOW" | cut -d: -f1)
unverified_stage_line=$(grep -Fn -- '- name: Stage exactly the built candidate for fresh-runner verification' "$WORKFLOW" | cut -d: -f1)
unverified_upload_line=$(grep -Fn -- '- name: Upload the unverified management build' "$WORKFLOW" | cut -d: -f1)
fresh_job_line=$(grep -Fn -- '  verify_management_candidate:' "$WORKFLOW" | cut -d: -f1)
fresh_download_line=$(grep -Fn -- '- name: Download the unverified management build' "$WORKFLOW" | cut -d: -f1)
candidate_binding_line=$(grep -Fn -- '- name: Bind candidate tarballs to the verified tag commit' "$WORKFLOW" | cut -d: -f1)
candidate_stage_line=$(grep -Fn -- '- name: Stage exactly the verified candidate for upload' "$WORKFLOW" | cut -d: -f1)
candidate_upload_line=$(grep -Fn -- '- name: Upload the unpublished management candidate' "$WORKFLOW" | cut -d: -f1)
if [ -z "$candidate_copy_line" ] ||
    [ -z "$candidate_build_line" ] ||
    [ -z "$candidate_shape_line" ] ||
    [ -z "$unverified_stage_line" ] ||
    [ -z "$unverified_upload_line" ] ||
    [ -z "$fresh_job_line" ] ||
    [ -z "$fresh_download_line" ] ||
    [ -z "$candidate_binding_line" ] ||
    [ -z "$candidate_stage_line" ] ||
    [ -z "$candidate_upload_line" ] ||
    [ "$candidate_copy_line" -ge "$candidate_build_line" ] ||
    [ "$candidate_build_line" -ge "$candidate_shape_line" ] ||
    [ "$candidate_shape_line" -ge "$unverified_stage_line" ] ||
    [ "$unverified_stage_line" -ge "$unverified_upload_line" ] ||
    [ "$unverified_upload_line" -ge "$fresh_job_line" ] ||
    [ "$fresh_job_line" -ge "$fresh_download_line" ] ||
    [ "$fresh_download_line" -ge "$candidate_binding_line" ] ||
    [ "$candidate_binding_line" -ge "$candidate_stage_line" ] ||
    [ "$candidate_stage_line" -ge "$candidate_upload_line" ]; then
    fail 'candidate must be built, handed to a fresh runner, commit-bound, staged, and only then uploaded'
fi

candidate_binding_section=$(sed -n \
    '/^[[:space:]]*- name: Bind candidate tarballs to the verified tag commit$/,/^[[:space:]]*- name: Upload the unpublished management candidate$/p' \
    "$WORKFLOW")
# These are literal workflow shell variables and must not expand in this contract.
# shellcheck disable=SC2016
for binding_contract in \
    'unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE' \
    'unset GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES' \
    'unset GIT_REPLACE_REF_BASE GIT_NAMESPACE GIT_EXEC_PATH' \
    'unset GIT_CONFIG GIT_CONFIG_SYSTEM GIT_CONFIG_GLOBAL' \
    'unset GIT_CONFIG_NOSYSTEM GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS' \
    'unset GIT_ATTR_NOSYSTEM' \
    'export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1' \
    'export GIT_ATTR_NOSYSTEM=1 GIT_NO_REPLACE_OBJECTS=1' \
    'command /usr/bin/git --no-replace-objects' \
    '-c "safe.directory=$VERIFIER_SOURCE" "$@"' \
    'trusted_git -C "$VERIFIER_SOURCE" rev-parse --verify' \
    'cat-file -t' \
    '"$source_tag_object")" == tag' \
    'trusted_git -C / ls-remote --exit-code' \
    'status --porcelain=v1 --untracked-files=all' \
    'expected_checksum_names="$(printf' \
    '$1 !~ /^[0-9a-f]{64}$/ { exit 2 }' \
    'NF != 2 { exit 3 }' \
    '[[ "$actual_checksum_names" == "$expected_checksum_names" ]]' \
    '[[ "$source_tag_object" =~ ^[0-9a-f]{40}$ ]]' \
    '[[ "$source_commit" =~ ^[0-9a-f]{40}$ ]]' \
    '[[ "$remote_tag_object" == "$source_tag_object" ]]' \
    '[[ "$remote_tag_commit" == "$source_commit" ]]' \
    '--release v1.2.0' \
    '--require-zero-supported' \
    '--release-assets "$UNVERIFIED_DOWNLOAD_ROOT"' \
    '--source-commit "$source_commit"' \
    '--source-tag-object "$source_tag_object"'; do
    printf '%s\n' "$candidate_binding_section" | grep -Fq -- "$binding_contract" ||
        fail "candidate binding step is missing: $binding_contract"
done

build_job_section=$(sed -n \
    '/^  build_management:$/,/^  verify_management_candidate:$/p' "$WORKFLOW")
fresh_verifier_section=$(sed -n \
    '/^  verify_management_candidate:$/,/^  verify_legacy:$/p' "$WORKFLOW")
for fresh_contract in \
    'needs: build_management' \
    'image: debian:13-slim' \
    'contents: read' \
    'ref: refs/tags/v1.2.0' \
    'cache: false' \
    'uses: actions/download-artifact@v4' \
    'name: probe-panel-management-v1.2.0-unverified' \
    'path: ${{ runner.temp }}/probe-panel-management-unverified-v1.2.0' \
    'BASH_ENV: /dev/null' \
    'UNVERIFIED_DOWNLOAD_ROOT: ${{ runner.temp }}/probe-panel-management-unverified-v1.2.0' \
    'VERIFIER_SOURCE: ${{ runner.temp }}/probe-panel-management-verifier-source-v1.2.0' \
    'cp -a --no-preserve=ownership -- "$GITHUB_WORKSPACE" "$VERIFIER_SOURCE"' \
    'find "$VERIFIER_SOURCE" ! -uid "$(id -u)"' \
    'PATH: /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin' \
    'name: probe-panel-management-v1.2.0-candidate' \
    'path: ${{ github.workspace }}/release-upload-v1.2.0/'; do
    printf '%s\n' "$fresh_verifier_section" | grep -Fq -- "$fresh_contract" ||
        fail "fresh verifier job is missing: $fresh_contract"
done

if [ "$(printf '%s\n' "$build_job_section" | grep -Fc 'uses: actions/checkout@')" -ne 1 ] ||
    [ "$(printf '%s\n' "$fresh_verifier_section" | grep -Fc 'uses: actions/checkout@')" -ne 1 ]; then
    fail 'build and fresh-verifier jobs must each own exactly one checkout'
fi
for build_forbidden in \
    'Bind candidate tarballs to the verified tag commit' \
    'go run ./cmd/probe-support-gate verify' \
    'name: probe-panel-management-v1.2.0-candidate'; do
    if printf '%s\n' "$build_job_section" | grep -Fq -- "$build_forbidden"; then
        fail "build job crosses the fresh-verifier trust boundary: $build_forbidden"
    fi
done
if printf '%s\n' "$fresh_verifier_section" | grep -Fq -- '--profile management'; then
    fail 'fresh verifier must consume the build artifact without rebuilding it'
fi
if [ "$(grep -Fc 'name: probe-panel-management-v1.2.0-unverified' "$WORKFLOW")" -ne 2 ]; then
    fail 'unverified artifact must have exactly one same-run upload and one named download'
fi
if [ "$(grep -Fc 'name: probe-panel-management-v1.2.0-candidate' "$WORKFLOW")" -ne 1 ]; then
    fail 'only the fresh verifier may upload the final candidate artifact'
fi

download_step_section=$(sed -n \
    '/^[[:space:]]*- name: Download the unverified management build$/,/^[[:space:]]*- name: Bind candidate tarballs to the verified tag commit$/p' \
    "$WORKFLOW")
for forbidden_download_input in \
    'github-token:' 'repository:' 'run-id:' 'artifact-ids:' 'pattern:' \
    'merge-multiple:'; do
    if printf '%s\n' "$download_step_section" | grep -Fq -- "$forbidden_download_input"; then
        fail "fresh verifier artifact download must stay bound to this run and exact name: $forbidden_download_input"
    fi
done

gate_line=$(grep -Fn -- 'go run ./cmd/probe-support-gate verify' "$WORKFLOW" | cut -d: -f1)
remote_after_line=$(grep -Fn -- 'remote_tags_after="$(trusted_git -C / ls-remote --exit-code' "$WORKFLOW" | cut -d: -f1)
if [ -z "$gate_line" ] || [ -z "$remote_after_line" ] ||
    [ "$gate_line" -ge "$remote_after_line" ] ||
    [ "$remote_after_line" -ge "$candidate_stage_line" ]; then
    fail 'fresh verifier must revalidate the remote annotated tag after the support gate and before staging'
fi

if printf '%s\n' "$candidate_binding_section" |
    grep -Eq '(^|[^_[:alnum:]])git --no-replace-objects -C'; then
    fail 'candidate binding must route every Git operation through trusted_git isolation'
fi

if grep -Eq '(Kcmose/my([.]git)?|my-agent|repository:[[:space:]].*/my)' "$WORKFLOW"; then
    fail 'management release workflow must not reference the visitor or Agent repositories'
fi
if grep -Fq -- '--profile full' "$WORKFLOW"; then
    fail 'management release workflow must never invoke the full bundle profile'
fi
if grep -Fq 'probe-panel-v1.2.0-linux-' "$WORKFLOW"; then
    fail 'management release workflow must never name or upload full v1.2.0 assets'
fi
if grep -Fq 'contents: write' "$WORKFLOW"; then
    fail 'tag workflow must not receive permission to publish a GitHub Release'
fi
if grep -Eq '(gh release (create|upload|edit)|action-gh-release|release-action)' "$WORKFLOW"; then
    fail 'tag workflow must never create, upload, edit, or publish a GitHub Release'
fi

printf '%s\n' 'release builder contract: PASS'
