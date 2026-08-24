#!/bin/sh

set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH='' cd -- "$SCRIPT_DIR/../../.." && pwd)
INSTALLER=$ROOT_DIR/install.sh
SETUP_UNIT=$ROOT_DIR/probe-api/deploy/setup/probe-panel-setup.service
FINALIZER_UNIT=$ROOT_DIR/probe-api/deploy/setup/probe-panel-finalizer.service
FINALIZER_PATH=$ROOT_DIR/probe-api/deploy/setup/probe-panel-finalizer.path
DEPLOY_COMMON=$ROOT_DIR/probe-api/deploy/scripts/deploy-common.sh

fail() {
    printf '%s\n' "bootstrap installer contract: $*" >&2
    exit 1
}

assert_contains() {
    needle=$1
    file=$2
    grep -Fq -- "$needle" "$file" || fail "$file is missing required contract: $needle"
}

[ -f "$INSTALLER" ] || fail "missing installer: $INSTALLER"
[ -f "$SETUP_UNIT" ] || fail "missing setup unit: $SETUP_UNIT"
[ -f "$FINALIZER_UNIT" ] || fail "missing finalizer unit: $FINALIZER_UNIT"
[ -f "$FINALIZER_PATH" ] || fail "missing finalizer path unit: $FINALIZER_PATH"
[ -f "$DEPLOY_COMMON" ] || fail "missing deployment helpers: $DEPLOY_COMMON"
bash -n "$INSTALLER"
sh -n "$0"

last_line=$(awk 'NF { line = $0 } END { print line }' "$INSTALLER")
[ "$last_line" = 'main "$@"' ] || fail 'installer entrypoint must be its final non-empty line'

TEST_ROOT=$(mktemp -d /tmp/probe-panel-bootstrap-contract.XXXXXX)
trap 'rm -rf -- "$TEST_ROOT"' EXIT
STUB_ROOT=$TEST_ROOT/bin
MARKER=$TEST_ROOT/external-command-ran
mkdir "$STUB_ROOT"
# The generated stub expands this variable only when the stub runs.
# shellcheck disable=SC2016
printf '%s\n' \
    '#!/bin/sh' \
    ': > "${PROBE_BOOTSTRAP_TRUNCATION_MARKER:?}"' \
    'exit 97' > "$STUB_ROOT/stub"
chmod 0755 "$STUB_ROOT/stub"
for name in addgroup adduser apt-get awk chown chmod cp curl find getent grep id install journalctl mktemp mv nginx psql python3 rm runuser setpriv sha256sum sleep ss stat systemctl systemd-analyze uname wc; do
    ln -s stub "$STUB_ROOT/$name"
done

TRUNCATED=$TEST_ROOT/truncated.sh
WITHOUT_ENTRYPOINT=$TEST_ROOT/without-entrypoint.sh
awk '
    { print }
    /^[[:space:]]*INSTALL_COMPLETED=1[[:space:]]*$/ { found = 1; exit }
    END { if (!found) exit 1 }
' "$INSTALLER" > "$TRUNCATED"
if PATH=$STUB_ROOT PROBE_BOOTSTRAP_TRUNCATION_MARKER=$MARKER /bin/bash "$TRUNCATED" >/dev/null 2>&1; then
    fail 'body-truncated installer unexpectedly succeeded'
fi
[ ! -e "$MARKER" ] || fail 'body-truncated installer executed an external command'

sed '$d' "$INSTALLER" > "$WITHOUT_ENTRYPOINT"
PATH=$STUB_ROOT PROBE_BOOTSTRAP_TRUNCATION_MARKER=$MARKER /bin/bash "$WITHOUT_ENTRYPOINT" >/dev/null 2>&1 ||
    fail 'function-only installer should parse without executing'
[ ! -e "$MARKER" ] || fail 'installer without final entrypoint executed an external command'

help_output=$(bash "$INSTALLER" --help)
for command_name in install status uninstall purge; do
    printf '%s\n' "$help_output" | grep -Fq -- "$command_name" || fail "help is missing $command_name"
done
if bash "$INSTALLER" purge >/dev/null 2>&1; then
    fail 'purge must fail closed'
fi
if bash "$INSTALLER" install unexpected >/dev/null 2>&1; then
    fail 'installer accepted extra command-line values'
fi

# These are literal source-code contracts and must not expand in this process.
# shellcheck disable=SC2016
for contract in \
    'PANEL_VERSION="${PROBE_PANEL_RELEASE_VERSION:-v1.0.0}"' \
    'SUPER_MY_REF="refs/tags/v1.0.0"' \
    'WEB_REF="refs/tags/v1.0.0"' \
    'AGENT_REF="refs/tags/v1.0.1"' \
    'probe-panel-${PANEL_VERSION}-linux-${architecture}.tar.gz' \
    "https://github.com/Kcmose/super-my/releases/download/" \
    "--proto '=https'" \
    'sha256sum' \
    'BUNDLE-SHA256SUMS must cover every source, artifacts, and setup file exactly once' \
    'artifacts/api/probe-api' \
    'setup/probe-setup' \
    'artifacts/migrations' \
    'nginx postgresql postgresql-client certbot' \
    'iproute2 util-linux' \
    'systemctl mask nginx.service' \
    'systemctl unmask nginx.service' \
    'systemctl stop nginx.service' \
    'systemctl disable nginx.service' \
    "disabled the stock Debian Nginx default site" \
    'systemctl enable --now postgresql.service' \
    "psql --no-psqlrc -Atqc 'SHOW listen_addresses'" \
    'TCP 80/443 must remain closed until the setup finalizer activates verified Nginx configuration' \
    'source/probe-api/deploy/setup/probe-panel-finalizer.service' \
    'source/probe-api/deploy/setup/probe-panel-finalizer.path' \
    'FINALIZER_REQUEST_FILE="${FINALIZER_RUNTIME_ROOT}/finalize.json"' \
    'FINALIZER_RESULT_FILE="${FINALIZER_RUNTIME_ROOT}/result.json"' \
    'PROBE_SETUP_FINALIZE_REQUEST_FILE=%s' \
    'PROBE_SETUP_FINALIZE_RESULT_FILE=%s' \
    'PROBE_SETUP_BUNDLE_ROOT=%s' \
    'PROBE_SETUP_RELEASE_ID=%s' \
    'PurePosixPath' \
    'path.is_absolute()' \
    '".." in path.parts' \
    'member.issym() or member.islnk()' \
    'special files are forbidden' \
    'SETUP_LISTEN_ADDR="127.0.0.1:18080"' \
    'one_time_code" =~ ^[0-9A-Fa-f]{64}$' \
    '"$SETUP_BINARY" init' \
    'ssh -L 18080:127.0.0.1:18080' \
    'main() {' \
    'main "$@"'; do
    assert_contains "$contract" "$INSTALLER"
done

assert_contains "printf 'PROBE_SETUP_FINALIZE_REQUEST_FILE=%s\\n' \"\$FINALIZER_REQUEST_FILE\"" "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_FINALIZE_RESULT_FILE=%s\\n' \"\$FINALIZER_RESULT_FILE\"" "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_BUNDLE_ROOT=%s\\n' \"\$INSTALLED_RELEASE\"" "$INSTALLER"
assert_contains "printf 'PROBE_SETUP_RELEASE_ID=%s\\n' \"\$PANEL_VERSION\"" "$INSTALLER"

if grep -Eq '(^|[[:space:]])(golang-go|nodejs|npm)([[:space:]\\]|$)' "$INSTALLER"; then
    fail 'server bootstrap must use prebuilt assets and must not install Go, Node.js, or npm'
fi

if grep -Eiq -- '(database|db|admin)[_-]?(password|secret)[=:]' "$INSTALLER"; then
    fail 'installer appears to define a database or administrator secret'
fi
if grep -Eq -- '--(database|db|admin)-(password|secret)' "$INSTALLER"; then
    fail 'installer must not accept database or administrator secret arguments'
fi
if grep -Eq 'refs/heads/|/main/' "$INSTALLER"; then
    fail 'installer must not use a moving GitHub branch ref'
fi

for preserved in /etc/probe-panel /var/lib/probe-panel /var/backups/probe-panel 'all PostgreSQL databases'; do
    assert_contains "$preserved" "$INSTALLER"
done
if grep -Eq 'rm[[:space:]].*(/etc/probe-panel|/var/lib/probe-panel|/var/backups/probe-panel)' "$INSTALLER"; then
    fail 'ordinary uninstall must preserve configuration, state, and backups'
fi

for contract in \
    'User=root' \
    'Group=root' \
    'EnvironmentFile=/etc/probe-panel/setup.env' \
    'ExecStart=/usr/local/lib/probe-panel/probe-setup serve' \
    'ProtectSystem=strict' \
    'ReadOnlyPaths=/srv/probe/setup-ui' \
    'ReadWritePaths=/var/lib/probe-panel/setup' \
    'ReadWritePaths=/run/probe-panel-setup' \
    'RuntimeDirectory=probe-panel-setup' \
    'RuntimeDirectoryMode=0700' \
    'CapabilityBoundingSet=' \
    'SocketBindAllow=tcp:18080' \
    'SocketBindDeny=any'; do
    assert_contains "$contract" "$SETUP_UNIT"
done

if grep -Eq 'SocketBindAllow=tcp:(80|443)$' "$SETUP_UNIT"; then
    fail 'setup service must not bind production HTTP/HTTPS ports'
fi

for contract in \
    'User=root' \
    'Group=root' \
    'EnvironmentFile=/etc/probe-panel/setup.env' \
    'ExecStart=/usr/local/lib/probe-panel/probe-setup finalize' \
    'TimeoutStartSec=30min' \
    'CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE CAP_FOWNER CAP_NET_BIND_SERVICE CAP_SETGID CAP_SETUID' \
    'AmbientCapabilities=CAP_SETGID CAP_SETUID' \
    'NoNewPrivileges=true' \
    'ProtectSystem=strict' \
    'ReadOnlyPaths=/srv/probe/setup-ui' \
    'ReadWritePaths=/etc/probe-panel' \
    'ReadWritePaths=/etc/nginx/conf.d' \
    'ReadWritePaths=/etc/systemd/system' \
    'ReadWritePaths=/etc/letsencrypt' \
    'ReadWritePaths=/var/lib/letsencrypt' \
    'ReadWritePaths=/var/log/letsencrypt' \
    'ReadWritePaths=/var/log/nginx' \
    'ReadWritePaths=/srv/probe' \
    'ReadWritePaths=/var/backups/probe-panel' \
    'ReadWritePaths=/run/lock' \
    'ReadWritePaths=/run/probe-panel-setup' \
    'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK' \
    'SocketBindAllow=tcp:80' \
    'SocketBindDeny=any'; do
    assert_contains "$contract" "$FINALIZER_UNIT"
done

# These are literal shell snippets whose variables must remain unexpanded.
# shellcheck disable=SC2016
assert_contains 'PGHOST="$PROBE_VALIDATED_PGHOST"' "$DEPLOY_COMMON"
# shellcheck disable=SC2016
assert_contains 'PGPASSFILE="$PROBE_VALIDATED_PGPASSFILE"' "$DEPLOY_COMMON"
assert_contains '--no-password' "$DEPLOY_COMMON"
assert_contains '/usr/bin/env -i' "$DEPLOY_COMMON"
# shellcheck disable=SC2016
if grep -Fq 'PGDATABASE="$PROBE_DATABASE_URL" pg_dump' "$DEPLOY_COMMON"; then
    fail 'pre-migration backup must not pass a PostgreSQL URL through PGDATABASE'
fi
if grep -Eq '^ReadWritePaths=/etc/rc([0-6]|S)[.]d$' "$FINALIZER_UNIT"; then
    fail 'setup finalizer must not write Debian SysV boot-link directories'
fi
if grep -Eq 'systemctl enable.*nginx[.]service' "$DEPLOY_COMMON"; then
    fail 'Nginx persistence must not invoke Debian SysV enable synchronization'
fi
assert_contains 'systemctl add-wants multi-user.target nginx.service' "$DEPLOY_COMMON"
if grep -Fq 'SocketBindAllow=tcp:18080' "$FINALIZER_UNIT"; then
    fail 'non-HTTP finalizer must not bind the setup HTTP port'
fi
if grep -Eq 'SocketBindAllow=tcp:443$' "$FINALIZER_UNIT"; then
    fail 'finalizer needs temporary Certbot TCP 80 only, never TCP 443'
fi

for contract in \
    'Requires=probe-panel-setup.service' \
    'PathExists=/run/probe-panel-setup/finalize.json' \
    'Unit=probe-panel-finalizer.service'; do
    assert_contains "$contract" "$FINALIZER_PATH"
done

printf '%s\n' 'bootstrap installer contract: PASS'
