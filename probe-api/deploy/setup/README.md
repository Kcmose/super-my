# First-run setup bootstrap assets

These assets exist only while a server is uninitialized. The setup service has
no TCP listener. systemd owns the fixed
`/run/probe-panel-setup/setup.sock` Unix stream socket and passes exactly one
descriptor named `setup-http` to `probe-setup serve`. The socket is
`root:root 0600`, its parent directory is mode `0700`, and the Go service also
checks Linux `SO_PEERCRED` before authorizing a request.

The bootstrap writes a canonical address to `PROBE_SETUP_SERVER_IP`. The setup
service exposes it to the page as the flat `defaults` object with `server_ip`,
`panel_url`, `agent_url`, and `admin_url`; URL construction uses ports 18453,
18454, and 18455 and brackets IPv6 literals.

An operator authenticates to the server as root and forwards a local browser
port directly to that remote Unix socket:

```text
ssh -N -o ExitOnForwardFailure=yes -L 127.0.0.1:18080:/run/probe-panel-setup/setup.sock root@SERVER
```

The browser still opens `http://127.0.0.1:18080/install`, so the existing exact
Host and Origin checks remain meaningful. The socket must never be proxied by
Nginx, made group/world accessible, or replaced by a server TCP listener.

`probe-setup init` creates only the root-owned persistent `pending` state. The
private channel automatically creates a 15-minute in-memory Session and
independent CSRF token. The first session advances `pending` to `configuring`.
While configuring, another valid private-channel request deliberately rotates
both credentials so a page refresh, lost response, or setup-service restart
can recover without a user-visible installation code. `finalizing`,
`installed`, `recovery_required`, malformed state, and all other states refuse
new sessions. A restart observed in `finalizing` preserves that state: the
separately running privileged worker is the only process allowed to commit
either terminal outcome, so a broker cancellation or HTTP-process restart
cannot preempt it.

The root installer exposes an explicit `migrate-bootstrap` operation for the
immutable v1.0.0 bootstrap only. It accepts strictly verified `pending` or
`configuring` layouts with no finalizer request/result or formal deployment,
quiesces the old broker, resets the state to `pending`, and invalidates the old
in-memory session. The old setup-code record remains recoverable for rollback
until the v1.1.0 binary, UI, units, environment, Unix socket permissions, and
HTTP readiness all pass; it is then durably unlinked. Every other state or
mixed layout fails closed.

After strict request validation, the HTTP process atomically places a root-only
`/run/probe-panel-setup/finalize.json` request. A systemd path unit invokes the
separate, non-HTTP `probe-panel-finalizer.service`. Only that short-lived
oneshot can write production configuration and release paths, control required
services, switch to the PostgreSQL account, and bind temporary TCP 80 where the
selected ingress mode requires it. Its root-only result is returned through
`/run/probe-panel-setup/result.json`; both files stay in the mode-0700 runtime
directory rather than persistent storage. Publishing the request transfers its
ownership to the root worker. A broker timeout or cancellation deliberately
leaves an unconsumed request in place instead of racing the worker by unlinking
it.

The long-running HTTP service has an empty capability set, `PrivateNetwork`,
and `RestrictAddressFamilies=AF_UNIX`. It may write only the setup state and
private runtime directory. The privileged finalizer remains separately
sandboxed; its input must stay a strict, single-use, mode-0600 tmpfs file and
must be removed on success or failure.

For a successful IP-mode installation, the temporary status response includes
the public `/etc/probe-panel/tls/private-ca/ca.pem` and the lowercase SHA-256 of
that exact PEM file. The setup service accepts only a bounded regular
non-symlink file containing CA certificate PEM blocks, keeps the handoff only in
process memory, and sends it with `Cache-Control: no-store`. If the current
broker response was lost, it may reconstruct the public admin URL and ingress
mode from the strict formal `/srv/probe/config/probe-api.env`; it never returns
the environment file or its database credential. A failed reconstruction is a
successful deployment with an explicit SSH/scp fallback, not permission to
reopen setup or bypass TLS.

Finalization changes the state from `configuring` through `finalizing` to the
terminal `installed` or `recovery_required` state. The root worker writes
`installed` only after the formal API and Nginx are ready and first-run service
closure has been arranged; that durable transition is the last fallible
success-path operation. If it fails, the worker independently stops and
disables every formal entry point, verifies that rollback, and then records
`recovery_required`. Its internal 25-minute deadline leaves cleanup and result
publication headroom before the broker and systemd 30-minute deadlines.

In either terminal state the finalizer leaves a 20-second window for the local
browser to read status, then stops the Unix socket and queues a non-blocking
setup-service stop. The non-blocking service stop avoids an ordering deadlock
with the finalizer's `After=probe-panel-setup.service` relationship. The HTTP
service also shuts itself down after 25 seconds as a fail-closed backup. The
socket unit exclusively owns the runtime directory lifecycle. An existing,
malformed, installed, or recovery state fails closed; loss of the production
database or administrator must never reopen first-run setup.
