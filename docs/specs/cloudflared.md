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
  - internal/module/github.go
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
- **systemctl failures are warnings** after the files are written (`installTunnelService`, `setupVLAN`). Shared helpers provide manual repair guidance without inventing a retry on a later converged apply. SSH/network/service ordering is unchanged.
- **Private network.** systemd-networkd files `10-cloudflared-vlan.netdev` (dummy kind) and `.network` (one `Address=`); interface `private_network.interface`, default `vlan0`. When `systemd-networkd` cannot be restarted, Apply creates the dummy link and assigns the address with `ip` directly. Validation requires a non-empty CIDR address when enabled and a valid interface name (`internal/config/validate.go`).
- Embedded profiles do not declare a site VLAN address. Use a site config or an address override on apply/check/config inspection or tunnel setup/install. Doctor/status reject `--vlan-address` rather than consuming an ignored input.
- All writes and removals go through `rc.Runner`.

## Behaviour

- Native tunnel text uses the run-context writer; update-check and CLI confirmation text use the command writer. Machine-facing download commands retain the unstyled arrow byte. Writer routing changes no query, download, reload or restart.

- `InspectCloudflared` owns the production binary path and performs one read-only version query. It returns presence, raw stdout, parsed version and query error separately; status keeps raw output while update comparisons keep their existing parsed/normalized policies. CLI path variables are test overrides only.
- Rootfiles and cloudflared latest-tag lookups share `FetchLatestGitHubTag`; wrappers keep their existing clients, deadlines, raw tag strings and error wording. Cloudflared retains its 15-second timeout. `ui.FirstNonEmpty` shares display fallback without trimming.

- Ordinary unit/token/VLAN writes use `ensureFile`, preserving current sibling files when only another group member changes. Token directory/file modes stay 0700/0600, and unit/network files stay 0644. Already-current direct setup helpers do not rewrite files or run service commands.

- `tunnel install` is an alias of `tunnel setup`, not a second constructor. Both share config-aware module Apply, reader annotations, native capability validation and the root/lock preflight.
- `tunnel setup [TOKEN] [--vlan-address CIDR]`: explicit positional/flag/environment inputs override loaded configuration; configured token-file and VLAN defaults are retained. Module Apply manages the binary, token service and enabled private network only on drift, so setup/install on converged state does not rewrite files or restart services.
- `tunnel status`, `tunnel restart`, `tunnel update [--version V] [--check]`: use `buildConfigFreeRunContext`, so they work without a loadable profile. `update` skips the download when the current version equals the target and restarts the service only when it is enabled. `--check` prints current vs latest (GitHub releases API, 15 s timeout) without changing anything.
- `tunnel uninstall`: disables the service, removes the unit, env file, private-network files, interface and binary; stop/delete failures are warnings, file removal failures are errors.
- `install`, `setup`, `restart`, `update`, `uninstall` are in `mutatingCommands` (root and lock unless `--dry-run`).

## Distro support

- Ubuntu and DGX OS. Rocky rejects `cloudflared` in `config.ValidateCapabilities` for `apply`/`check`; mutating `tunnel` subcommands validate their requested capability before host changes.

## Safety

- Generated-file comparisons preserve legacy headers using the [shared managed-header rule](exec-packages.md#contracts-and-invariants); changing the marker alone never rewrites a converged file or reloads its service.
- Never log, print or pass the token as an argument from rootfiles itself. Both setup/install positional tokens and `--tunnel-token` are redacted from audit/backup notes; prefer `tunnel_token_file` for unattended inputs.
- Never leave a partially downloaded binary at the install path.
- Removing `private_network` from a config does not tear down an existing interface; only `tunnel uninstall` does (unverified beyond reading Check/Apply, which only act when enabled).

## Tests that pin it

- `internal/module/cloudflared_test.go`: token service convergence, private-network address drift, bad token rejection, token file accepted/rejected (symlink, mode, owner), explicit token over file.
- `internal/cli/tunnel_test.go`: subcommand tree and the run-context loaders (contract owned by [cli-runtime](cli-runtime.md)).
- Scenario `tests/scenarios/tunnel-setup-teardown.sh` (CI `scenario` job).
