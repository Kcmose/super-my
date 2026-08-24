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
readonly WEB_REF="refs/tags/v1.0.0"
readonly AGENT_REF="refs/tags/v1.0.1"

ADMIN_ROOT="$SUPER_MY_ROOT"
WEB_ROOT="${SUPER_MY_ROOT}/../my"
AGENT_ROOT="${SUPER_MY_ROOT}/../my-agent"
OUTPUT_DIR="${SUPER_MY_ROOT}/../probe-panel-release-v1.0.0"
VERSION="v1.0.0"
WORK_ROOT=""

usage() {
    cat <<'EOF'
Usage: build-release-bundles.sh [options]

Build both Debian 13 release bundles locally without uploading anything.

Options:
  --admin-root DIR   probe-admin project root (default: super-my repository root)
  --web-root DIR     probe-web project root (default: ../my)
  --agent-root DIR   probe-agent project root (default: ../my-agent)
  --output-dir DIR   new directory that receives both tarballs and SHA256SUMS
  --version VERSION  release version; currently pinned to v1.0.0
  -h, --help         show this help

The probe-api source is always taken from <super-my>/probe-api. Source trees
must be clean source snapshots without generated dependencies or build output.
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
            --admin-root)
                ADMIN_ROOT="$(take_value "$1" "${2-}")"
                shift 2
                ;;
            --admin-root=*) ADMIN_ROOT="$(take_value --admin-root "${1#*=}")"; shift ;;
            --web-root)
                WEB_ROOT="$(take_value "$1" "${2-}")"
                shift 2
                ;;
            --web-root=*) WEB_ROOT="$(take_value --web-root "${1#*=}")"; shift ;;
            --agent-root)
                AGENT_ROOT="$(take_value "$1" "${2-}")"
                shift 2
                ;;
            --agent-root=*) AGENT_ROOT="$(take_value --agent-root "${1#*=}")"; shift ;;
            --output-dir)
                OUTPUT_DIR="$(take_value "$1" "${2-}")"
                shift 2
                ;;
            --output-dir=*) OUTPUT_DIR="$(take_value --output-dir "${1#*=}")"; shift ;;
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

canonical_source_directory() {
    local label="$1" candidate="$2" resolved
    [[ -d "$candidate" && ! -L "$candidate" ]] || die "$label is not a real directory: $candidate"
    resolved="$(readlink -f -- "$candidate")"
    [[ -n "$resolved" && "$resolved" == /* ]] || die "could not resolve $label: $candidate"
    printf '%s\n' "$resolved"
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

validate_sources() {
    local api_root="$1" required
    for required in \
        go.mod go.sum cmd/probe-api/main.go cmd/probe-setup/main.go \
        config/probe-api.env.example deploy/scripts/deploy-common.sh \
        deploy/scripts/build-release-bundles.sh deploy/scripts/install-release.sh \
        deploy/setup/probe-panel-setup.service deploy/setup/probe-panel-finalizer.service \
        deploy/setup/probe-panel-finalizer.path migrations/000001_initial.up.sql; do
        assert_regular_file "$api_root/$required"
    done
    assert_regular_file "$SUPER_MY_ROOT/install.sh"
    assert_regular_file "$SUPER_MY_ROOT/.github/workflows/release.yml"

    for required in package.json package-lock.json index.html vite.config.js; do
        assert_regular_file "$ADMIN_ROOT/$required"
        assert_regular_file "$WEB_ROOT/$required"
    done
    for required in go.mod cmd/probe-agent/main.go deploy/install.sh \
        deploy/tests/install-contract.sh deploy/systemd/probe-agent.service; do
        assert_regular_file "$AGENT_ROOT/$required"
    done

    assert_clean_source_tree probe-api "$api_root"
    assert_clean_source_tree probe-admin "$ADMIN_ROOT"
    assert_clean_source_tree probe-web "$WEB_ROOT"
    assert_clean_source_tree probe-agent "$AGENT_ROOT"
    [[ ! -e "$api_root/probe-api" && ! -L "$api_root/probe-api" ]] ||
        die "$api_root/probe-api is a generated API binary"
    [[ ! -e "$AGENT_ROOT/probe-agent" && ! -L "$AGENT_ROOT/probe-agent" ]] ||
        die "$AGENT_ROOT/probe-agent is a generated Agent binary"
}

assert_output_outside_sources() {
    local output="$1" source_root
    for source_root in "$SUPER_MY_ROOT" "$ADMIN_ROOT" "$WEB_ROOT" "$AGENT_ROOT"; do
        case "$output" in
            "$source_root"|"$source_root"/*) die "output directory must be outside source trees: $output" ;;
        esac
    done
}

copy_frontend_source() {
    local source_root="$1" destination="$2" entry
    install -d -m 0700 "$destination"
    for entry in package.json package-lock.json index.html vite.config.js \
        postcss.config.js tailwind.config.js src public test deploy; do
        [[ -e "$source_root/$entry" ]] || die "frontend source is incomplete: $source_root/$entry"
        cp -a -- "$source_root/$entry" "$destination/"
    done
}

copy_project_source() {
    local source_root="$1" destination="$2" entry name
    install -d -m 0700 "$destination"
    while IFS= read -r -d '' entry; do
        name="${entry##*/}"
        [[ "$name" == .git ]] && continue
        cp -a -- "$entry" "$destination/"
    done < <(find "$source_root" -mindepth 1 -maxdepth 1 -print0)
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
    log "testing, auditing, and building $label"
    (
        cd "$source_root"
        npm ci --no-audit --no-fund
        npm test
        npm audit --omit=dev --audit-level=high
        npm run build
    )
    [[ -f "$source_root/dist/index.html" && ! -L "$source_root/dist/index.html" ]] ||
        die "$label build did not create dist/index.html"
    assert_no_links_or_maps "$label production output" "$source_root/dist"
    install -d -m 0755 "$artifact_root"
    cp -a -- "$source_root/dist/." "$artifact_root/"
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
    local common_root="$4" api_source_root="$5" publish_root="$6"
    local bundle_name="probe-panel-${VERSION}-linux-${architecture}"
    local bundle_root="${WORK_ROOT}/${bundle_name}"

    install -d -m 0755 \
        "$bundle_root/artifacts/api" "$bundle_root/artifacts/admin" \
        "$bundle_root/artifacts/web" "$bundle_root/artifacts/agent" \
        "$bundle_root/artifacts/migrations" "$bundle_root/setup" \
        "$bundle_root/source/probe-api"
    install -m 0755 "$api_binary" "$bundle_root/artifacts/api/probe-api"
    install -m 0755 "$setup_binary" "$bundle_root/setup/probe-setup"
    cp -a -- "$common_root/admin/." "$bundle_root/artifacts/admin/"
    cp -a -- "$common_root/web/." "$bundle_root/artifacts/web/"
    cp -a -- "$common_root/agent/." "$bundle_root/artifacts/agent/"
    cp -a -- "$api_source_root/migrations/." "$bundle_root/artifacts/migrations/"
    cp -a -- "$api_source_root/config" "$bundle_root/source/probe-api/config"
    cp -a -- "$api_source_root/deploy" "$bundle_root/source/probe-api/deploy"

    cat > "$bundle_root/RELEASE-MANIFEST" <<EOF
format=probe-panel-release-v1
version=${VERSION}
architecture=linux-${architecture}
super_my_ref=refs/tags/${VERSION}
my_ref=${WEB_REF}
my_agent_ref=${AGENT_REF}
EOF

    assert_no_links_or_maps "$bundle_name" "$bundle_root"
    assert_manifest_safe_paths "$bundle_root"
    find "$bundle_root" -type d -exec chmod 0755 {} +
    find "$bundle_root" -type f -exec chmod 0644 {} +
    chmod 0755 \
        "$bundle_root/artifacts/api/probe-api" \
        "$bundle_root/setup/probe-setup" \
        "$bundle_root/artifacts/agent/downloads/probe-agent/install.sh" \
        "$bundle_root/artifacts/agent/downloads/probe-agent/linux-amd64/probe-agent" \
        "$bundle_root/artifacts/agent/downloads/probe-agent/linux-arm64/probe-agent" \
        "$bundle_root/source/probe-api/deploy/scripts/install-release.sh"
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
    [[ "$VERSION" == v1.0.0 ]] ||
        die 'this release builder is pinned to the reviewed v1.0.0 server release'
    require_debian_13
    local command_name
    for command_name in basename bash cat chmod cp dirname file find go gzip install \
        mktemp mv npm readlink rm sh sha256sum shellcheck sort tar xargs; do
        require_command "$command_name"
    done

    ADMIN_ROOT="$(canonical_source_directory probe-admin "$ADMIN_ROOT")"
    WEB_ROOT="$(canonical_source_directory probe-web "$WEB_ROOT")"
    AGENT_ROOT="$(canonical_source_directory probe-agent "$AGENT_ROOT")"
    local api_root="${SUPER_MY_ROOT}/probe-api"
    api_root="$(canonical_source_directory probe-api "$api_root")"
    validate_sources "$api_root"

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

    WORK_ROOT="$(mktemp -d "${output_parent}/.probe-release-build.XXXXXX")"
    trap cleanup EXIT
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM

    local source_root="${WORK_ROOT}/sources"
    local super_source="${source_root}/super-my"
    local api_source="${super_source}/probe-api"
    local web_source="${source_root}/my"
    local agent_source="${source_root}/my-agent"
    local binary_root="${WORK_ROOT}/binaries"
    local common_root="${WORK_ROOT}/common"
    local publish_root="${WORK_ROOT}/publish"
    install -d -m 0700 "$source_root" "$super_source" "$binary_root/api" \
        "$binary_root/setup" "$binary_root/agent/linux-amd64" \
        "$binary_root/agent/linux-arm64" "$common_root/agent/downloads/probe-agent/linux-amd64" \
        "$common_root/agent/downloads/probe-agent/linux-arm64" "$publish_root"
    copy_frontend_source "$ADMIN_ROOT" "$super_source"
    copy_frontend_source "$WEB_ROOT" "$web_source"
    copy_project_source "$api_root" "$api_source"
    copy_project_source "$AGENT_ROOT" "$agent_source"
    install -m 0755 "$SUPER_MY_ROOT/install.sh" "$super_source/install.sh"
    install -d -m 0755 "$super_source/.github/workflows"
    install -m 0644 "$SUPER_MY_ROOT/.github/workflows/release.yml" \
        "$super_source/.github/workflows/release.yml"

    log 'checking deployment Shell contracts'
    shellcheck "$api_source"/deploy/scripts/*.sh "$api_source"/deploy/tests/*.sh \
        "$agent_source/deploy/install.sh" "$agent_source"/deploy/tests/*.sh
    (
        cd "$super_source"
        sh probe-api/deploy/tests/bootstrap-install-contract.sh
        sh probe-api/deploy/tests/build-release-bundles-contract.sh
    )

    log 'testing and vetting probe-api'
    (
        cd "$api_source"
        go test -count=1 ./...
        go vet ./...
        for architecture in amd64 arm64; do
            CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" \
                go build -trimpath -ldflags='-s -w' \
                -o "$binary_root/api/probe-api-$architecture" ./cmd/probe-api
            CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" \
                go build -trimpath -ldflags='-s -w' \
                -o "$binary_root/setup/probe-setup-$architecture" ./cmd/probe-setup
        done
    )

    log 'testing, vetting, and cross-building probe-agent'
    (
        cd "$agent_source"
        go test -count=1 ./...
        go vet ./...
        sh deploy/tests/install-contract.sh
        for architecture in amd64 arm64; do
            CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" \
                go build -trimpath -ldflags='-s -w' \
                -o "$binary_root/agent/linux-$architecture/probe-agent" ./cmd/probe-agent
        done
    )

    local architecture
    for architecture in amd64 arm64; do
        assert_binary_architecture "$binary_root/api/probe-api-$architecture" "$architecture"
        assert_binary_architecture "$binary_root/setup/probe-setup-$architecture" "$architecture"
        assert_binary_architecture "$binary_root/agent/linux-$architecture/probe-agent" "$architecture"
        install -m 0755 "$binary_root/agent/linux-$architecture/probe-agent" \
            "$common_root/agent/downloads/probe-agent/linux-$architecture/probe-agent"
    done
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

    build_frontend probe-admin "$super_source" "$common_root/admin"
    build_frontend probe-web "$web_source" "$common_root/web"

    for architecture in amd64 arm64; do
        log "assembling linux-$architecture release bundle"
        assemble_bundle "$architecture" \
            "$binary_root/api/probe-api-$architecture" \
            "$binary_root/setup/probe-setup-$architecture" \
            "$common_root" "$api_source" "$publish_root"
    done
    (
        cd "$publish_root"
        sha256sum \
            "probe-panel-${VERSION}-linux-amd64.tar.gz" \
            "probe-panel-${VERSION}-linux-arm64.tar.gz" > SHA256SUMS
        sha256sum --check --strict SHA256SUMS >/dev/null
    )
    mv -T -- "$publish_root" "$OUTPUT_DIR"
    printf 'Probe Panel %s release assets created locally:\n  %s\n' "$VERSION" "$OUTPUT_DIR"
    printf 'No GitHub release was created or modified.\n'
}

main "$@"
