#!/bin/sh

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH='' cd -- "$SCRIPT_DIR/../../.." && pwd)
BUILDER=$ROOT_DIR/probe-api/deploy/scripts/build-release-bundles.sh
WORKFLOW=$ROOT_DIR/.github/workflows/release.yml

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
[ -f "$WORKFLOW" ] || fail "missing release verification workflow: $WORKFLOW"
bash -n "$BUILDER"
sh -n "$0"

help_output=$(bash "$BUILDER" --help)
for option in --admin-root --web-root --agent-root --output-dir --version; do
    printf '%s\n' "$help_output" | grep -Fq -- "$option" || fail "help is missing $option"
done
printf '%s\n' "$help_output" | grep -Fq 'without uploading anything' ||
    fail 'help must state that the builder never uploads'

if bash "$BUILDER" --unknown-option >/dev/null 2>&1; then
    fail 'builder accepted an unknown option'
fi
if bash "$BUILDER" --admin-root >/dev/null 2>&1; then
    fail 'builder accepted a missing option value'
fi
if bash "$BUILDER" --version v1.0.1 >/dev/null 2>&1; then
    fail 'builder accepted an unreviewed server version'
fi

# These are literal source-code contracts and must not expand in this process.
# shellcheck disable=SC2016
for contract in \
    'release bundles must be built on Debian 13' \
    'VERSION="v1.0.0"' \
    'WEB_REF="refs/tags/v1.0.0"' \
    'AGENT_REF="refs/tags/v1.0.1"' \
    'npm test' \
    'npm audit --omit=dev --audit-level=high' \
    'npm run build' \
    'go test -count=1 ./...' \
    'go vet ./...' \
    'sh probe-api/deploy/tests/build-release-bundles-contract.sh' \
    'GOARCH="$architecture"' \
    './cmd/probe-api' \
    './cmd/probe-setup' \
    './cmd/probe-agent' \
    'artifacts/api/probe-api' \
    'setup/probe-setup' \
    'artifacts/migrations' \
    'source/probe-api/config' \
    'source/probe-api/deploy' \
    '$bundle_root/source/probe-api/deploy/scripts/install-release.sh' \
    'RELEASE-MANIFEST' \
    'BUNDLE-SHA256SUMS' \
    'probe-panel-${VERSION}-linux-amd64.tar.gz' \
    'probe-panel-${VERSION}-linux-arm64.tar.gz' \
    'sha256sum --check --strict' \
    "-name '*.map'" \
    'contains a symbolic link' \
    'contains generated source pollution' \
    '.probe-release-build.' \
    'rm -rf -- "$WORK_ROOT"' \
    'No GitHub release was created or modified.'; do
    assert_contains "$contract" "$BUILDER"
done

if grep -Eq '(^|[[:space:]])(gh|curl)[[:space:]].*(release (create|upload|edit)|uploads[.]github[.]com)' "$BUILDER"; then
    fail 'local release builder must not create or upload a GitHub release'
fi

for contract in \
    'permissions:' \
    'contents: read' \
    'immutable' \
    'gh api' \
    'gh release download' \
    'probe-panel-v1.0.0-linux-amd64.tar.gz' \
    'probe-panel-v1.0.0-linux-arm64.tar.gz' \
    'SHA256SUMS' \
    'sha256sum --check --strict'; do
    assert_contains "$contract" "$WORKFLOW"
done

if grep -Eq '(setup-go|setup-node|npm (ci|test|run|audit)|go (test|vet|build)|gh release (create|upload|edit)|actions/upload-artifact)' "$WORKFLOW"; then
    fail 'GitHub workflow must verify published assets only and must not build or upload'
fi

printf '%s\n' 'release builder contract: PASS'
