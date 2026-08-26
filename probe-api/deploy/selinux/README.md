# CentOS SELinux candidate contract for the management product

This directory contains the fail-closed candidate policy for management-only
Probe Panel on CentOS Linux 7/8 and CentOS Stream 8/9/10. It is deliberately
not wired into the public installer until the complete lifecycle has passed on
real full-system SELinux Enforcing VMs.

## Network boundary

- The API remains loopback-only on TCP `8080`. The helper accepts only an
  existing `http_cache_port_t` or `http_port_t` mapping and never creates,
  changes, or removes that shared mapping.
- IP ingress TCP `18455` is assigned `probe_panel_ingress_port_t` only when the
  port is unmapped. An existing `http_port_t` mapping is accepted unchanged;
  every other type is a hard conflict.
- The module grants `httpd_t` only `name_bind` on the Probe-owned 18455 type.
  It contains no API port type and no `name_connect` permission.
- Nginx loopback proxy access uses only the standard
  `httpd_can_network_relay` Boolean. The helper records the active, persistent,
  policy-default, and local override states so rollback never guesses.
- `httpd_can_network_connect`, wildcard port permissions, `semanage port -m`,
  and firewalld mutation are forbidden.

## Files and ownership

The helper uses standard file types for the administrator SPA, Nginx fragment,
allowlist, TLS material, API binary, backup scripts, and setup binary. It does
not expose API environment files, PostgreSQL data, or backup archives as Web
content.

The root-only state journal is
`/var/lib/probe-panel/selinux/nginx-management.state`; its directory and file
are strictly `0700`/`0600`. A root-only flock serializes operations. State is
written atomically and parsed as an exact schema. Rollback removes only rules
that the journal records as Probe-owned and whose current value still matches;
drift is retained for manual review.

## Lifecycle integration gate

Before this helper may enter the management bundle and installer, every CentOS
platform must prove on a real Enforcing VM that:

1. `preflight` rejects foreign/overlapping port and fcontext ownership without
   Probe package, account, service, or permanent-path mutation;
2. `install` applies the minimal module, Boolean, port, and fcontext set and
   survives a real reboot;
3. IP and domain ingress can proxy to loopback `8080` while unrelated Nginx and
   PostgreSQL state remains unchanged;
4. upgrade `refresh`, injected failure rollback, and ordinary uninstall
   `rollback` preserve pre-existing rules and operator drift;
5. the exact Release bundle and evidence hashes pass the support ledger's
   `selinux_enforcing` scenario.

Until those tests exist, the root installer must continue to reject Enforcing
before mutation. Permissive or Disabled SELinux is not a substitute for formal
support. Firewalld remains entirely operator-managed.
