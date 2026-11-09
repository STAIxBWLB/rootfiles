---
id: cloudflared
type: module
title: Cloudflare Tunnel and private network
status: active
parent: architecture
depends-on:
  - engine
paths:
  - internal/module/cloudflared.go
  - internal/cli/tunnel.go
---

# Cloudflare Tunnel and private network

## Purpose

The `cloudflared` module and `rootfiles tunnel` install the upstream `cloudflared` binary, run a Cloudflare Tunnel as a systemd service from a root-only token file, and optionally create a dummy interface that carries a private-network address routed through the tunnel. It is also the documented external access path for the monitoring hub UIs ([monitoring-hub](monitoring-hub.md)). `internal/cli/tunnel.go` also holds `buildRunContext`, `buildConfigFreeRunContext` and `loadRunContext`, used by most root-mutating subcommands; their contract (which config they load, fallback rules) is owned by [cli-runtime](cli-runtime.md), not by this spec.

## Contracts and invariants

- **The token is a credential.** It is written only to `/etc/cloudflared/tunnel.env` (`TUNNEL_TOKEN=...`, mode 0600, directory 0700) and read by systemd through `EnvironmentFile`; it never goes into the world-readable unit (`cloudflaredUnit`) or onto the `cloudflared` command line. `installTunnelService` rejects tokens containing whitespace, newlines or quotes. `resolveTunnelToken` applies the same rule to a token read from `tunnel_token_file`, but returns an explicit token (flag, env or `tunnel_token`) unchecked, so Check can report drift for an inline token that Apply later refuses (known gap).
- **Token sources.** Explicit token wins: `--tunnel-token`, `ROOTFILES_TUNNEL_TOKEN` or `modules.cloudflared.tunnel_token`; otherwise `tunnel_token_file`. Setting both `tunnel_token` and `tunnel_token_file` in config is a load error (`internal/config/loader.go`).
- **tunnel_token_file checks** (`resolveTunnelToken`): opened with `O_NOFOLLOW` and checked through the same descriptor; must be a regular file, owned by root (`tunnelTokenFileUID`), with no group/other permission bits, non-empty, read up to 64 KiB. Any violation is an error from both Check and Apply. Other modules that read secret files (the hub receiver file) follow this pattern.
- **Convergence by content.** Check reports, and Apply performs, exactly: binary missing; unit or env file content differs (`tunnelServiceDrift`) when a token is known; private-network netdev/network files differ (`vlanDrift`) when `private_network.enabled` and `address` are set. With no token the service is not touched. Normal config validation refuses `private_network.enabled` without an address; config, environment or `--vlan-address` on a declared reader must supply it before validation.
- **Binary install is atomic.** `installBinaryVersion` downloads with `curl -fsSL --retry 3` to `/usr/local/bin/cloudflared.rootfiles-download`, chmods it and renames it into place; a failed download never leaves a truncated binary. Only `amd64` and `arm64` are supported. No version means GitHub's `latest/download` alias; `--version` uses the tagged release URL.
- **systemctl failures are warnings** after the files are written (`installTunnelService`, `setupVLAN`): the service or interface comes up on the next boot.
- **Private network.** systemd-networkd files `10-cloudflared-vlan.netdev` (dummy kind) and `.network` (one `Address=`); interface `private_network.interface`, default `vlan0`. When `systemd-networkd` cannot be restarted, Apply creates the dummy link and assigns the address with `ip` directly. Validation requires a non-empty CIDR address when enabled and a valid interface name (`internal/config/validate.go`).
- Embedded profiles do not declare a site VLAN address. Use a site config or an address override on apply/check/config inspection or tunnel setup/install. Doctor/status reject `--vlan-address` rather than consuming an ignored input.
- All writes and removals go through `rc.Runner`.

## Behaviour

- `tunnel install`: runs the whole module Apply (binary, service, private network), despite its help text.
- `tunnel setup [TOKEN] [--vlan-address CIDR]`: private network first (it does not need the binary), then binary if missing, then the service if a token is given (positional, `--tunnel-token`, or `ROOTFILES_TUNNEL_TOKEN`; address also from `ROOTFILES_VLAN_ADDRESS`).
- `tunnel status`, `tunnel restart`, `tunnel update [--version V] [--check]`: use `buildConfigFreeRunContext`, so they work without a loadable profile. `update` skips the download when the current version equals the target and restarts the service only when it is enabled. `--check` prints current vs latest (GitHub releases API, 15 s timeout) without changing anything.
- `tunnel uninstall`: disables the service, removes the unit, env file, private-network files, interface and binary; stop/delete failures are warnings, file removal failures are errors.
- `install`, `setup`, `restart`, `update`, `uninstall` are in `mutatingCommands` (root and lock unless `--dry-run`).

## Distro support

- Ubuntu and DGX OS. Rocky rejects `cloudflared` in `config.ValidateCapabilities` for `apply`/`check`; `tunnel` subcommands do not run capability validation yet (#91).

## Safety

- Never log, print or pass the token as an argument from rootfiles itself. Note that a token given as `tunnel setup TOKEN` or `--tunnel-token` is still recorded in the audit log and backup note today (#88); prefer `tunnel_token_file`.
- Never leave a partially downloaded binary at the install path.
- Removing `private_network` from a config does not tear down an existing interface; only `tunnel uninstall` does (unverified beyond reading Check/Apply, which only act when enabled).

## Tests that pin it

- `internal/module/cloudflared_test.go`: token service convergence, private-network address drift, bad token rejection, token file accepted/rejected (symlink, mode, owner), explicit token over file.
- `internal/cli/tunnel_test.go`: subcommand tree and the run-context loaders (contract owned by [cli-runtime](cli-runtime.md)).
- Scenario `tests/scenarios/tunnel-setup-teardown.sh` (CI `scenario` job).

## Open issues

- [#88](https://github.com/STAIxBWLB/rootfiles/issues/88): token in audit log/backup note; `tunnel update --check` should run without root and exit 2 when an update exists.
- [#91](https://github.com/STAIxBWLB/rootfiles/issues/91): capability validation and distro package manager in `buildRunContext` for `tunnel`.
- [#92](https://github.com/STAIxBWLB/rootfiles/issues/92): `tunnel install` becomes an alias of `tunnel setup`; replace the example address in help and prompts with a TEST-NET address.
- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): duplicated binary path, version parsing, latest-version fetch and `firstNonEmptyCloudflared`.
- [#95](https://github.com/STAIxBWLB/rootfiles/issues/95): `# Managed by rootfiles-v2` header.
