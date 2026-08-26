# First-run setup bootstrap assets

These assets exist only while a server is uninitialized. The setup service has
no TCP listener. systemd owns the fixed
`/run/probe-panel-setup/setup.sock` Unix stream socket and passes exactly one
descriptor named `setup-http` to `probe-setup serve`. The socket is
`root:root 0600`, its parent directory is mode `0700`, and the Go service also
checks Linux `SO_PEERCRED` before authorizing a request.

The root installer fixes `PROBE_SETUP_PROFILE` and writes a canonical address
to `PROBE_SETUP_SERVER_IP`. The setup service always exposes `profile`,
`server_ip`, and `admin_url`. The management profile deliberately omits
`panel_url` and `agent_url` and uses only port 18455 in IP mode. The historical
full profile also returns those two URLs on ports 18453 and 18454. URL
construction brackets IPv6 literals.

Management setup accepts exactly one management domain with ACME, or no domain
plus one canonical routable address with a private CA. Its browser request has
only `domains.admin`; visitor and Agent ingress fields do not exist in this
management protocol. This product does not install or configure either program.

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

The privileged worker distinguishes read-only preflight from production
ownership. DNS, reserved-port, existing Probe path, PostgreSQL name, current
Nginx activity/configuration, or Certbot unit-state failures discovered before
any owned production mutation atomically return `finalizing` to `configuring` and
publish only `preflight_failed`. The browser clears submitted passwords, mints
a new private session, and requires an explicit corrected resubmission. Once
the retryable result is durable, the state-aware finalizer cleanup leaves the
root-private Setup socket and HTTP service running. Only `installed` or
`recovery_required` closes them after a 30-second handoff window. Once
the worker starts creating Probe production paths or credentials, every failure
remains fail-closed as `recovery_required`. The combined Nginx candidate check
requires generated Probe configuration/TLS material, so it is post-ownership,
but it runs before database role creation or administrator bootstrap.

The v1.2 management-only root installer does not expose a bootstrap migration
operation. Existing v1.0/v1.1 full installations must use their matching
immutable historical tag and separately reviewed recovery path; they are never
silently converted into the independent management product.

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

Finalization changes the state from `configuring` through `finalizing` to
terminal `installed`/`recovery_required`, except that a proven side-effect-free
preflight rejection returns to `configuring`. The root worker writes
`installed` only after the formal API and Nginx are ready and first-run service
closure has been arranged; that durable transition is the last fallible
success-path operation. If it fails, the worker removes and disables only the
Probe-owned formal entry points, verifies that rollback, and then records
`recovery_required`. For a coexisting management install it preserves shared
Nginx and Certbot enablement/activity as observed before finalization, removes
only the Probe configuration, validates Nginx, and reloads it only when it had
already been active. Its internal 25-minute deadline leaves cleanup and result
publication headroom before the broker and systemd 30-minute deadlines.

In either terminal state the finalizer leaves a 20-second window for the local
browser to read status, then stops the Unix socket and queues a non-blocking
setup-service stop. The non-blocking service stop avoids an ordering deadlock
with the finalizer's `After=probe-panel-setup.service` relationship. The HTTP
service also shuts itself down after 25 seconds as a fail-closed backup. The
socket unit exclusively owns the runtime directory lifecycle. An existing,
malformed, installed, or recovery state fails closed; loss of the production
database or administrator must never reopen first-run setup.
