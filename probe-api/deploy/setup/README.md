# First-run setup bootstrap assets

These assets are installed only while a server is uninitialized. The HTTP setup
service binds exactly to `127.0.0.1:18080`; an operator reaches `/install`
through an SSH local-forward. It must never be exposed through Nginx or a
public listener. Its capability set remains empty and its only writable state
is the setup store plus `/run/probe-panel-setup`.

`probe-setup init` creates a 256-bit one-time code. Plaintext is returned once
to the invoking terminal. The root-owned setup store contains only its SHA-256,
expiry/consumption metadata, and the persistent installation state. Database
and administrator passwords are accepted only by the setup API and are never
installer arguments or environment-file values.

After strict request validation, the HTTP process atomically places a root-only
`/run/probe-panel-setup/finalize.json` request. A systemd path unit invokes the
separate, non-HTTP `probe-panel-finalizer.service`. Only that short-lived
oneshot can write production configuration and release paths, control the
required services, switch to the PostgreSQL account, and bind temporary TCP 80
for Certbot HTTP-01. Its 30-minute systemd deadline matches the broker context;
it cannot bind any other port. Its root-only result is
returned through `/run/probe-panel-setup/result.json`; both files live in the
mode-0700 systemd runtime directory rather than persistent storage.

The production finalizer is responsible for changing the state from
`pending` through `configuring` and `finalizing` to `installed`, then destroying
the setup code and disabling this unit. An existing or malformed state must
fail closed; loss of the database or an administrator must not reopen setup.

This separation limits the long-running HTTP attack surface, but the oneshot is
still intentionally privileged: it can write systemd and Nginx configuration
and ask PID 1 to control services. Its input must therefore remain a strict,
single-use, mode-0600 tmpfs file and it must always remove that file on success
or failure.

The finalizer capability boundary is limited to file ownership/override,
temporary TCP 80 binding, and UID/GID switching. Only `CAP_SETUID` and
`CAP_SETGID` are ambient so the process can enter the local `postgres` account;
the nested `setpriv` execution then drops inheritable and ambient capabilities
before `psql`. `AF_NETLINK` is allowed solely so `ss` can verify loopback
listeners. Nginx boot persistence is installed as a native systemd
`multi-user.target` want after its Debian unit path is verified; the finalizer
cannot write `/etc/rc*.d` or invoke SysV enable synchronization.
