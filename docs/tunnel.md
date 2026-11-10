# Cloudflare tunnel

The `cloudflared` module installs the Cloudflare Tunnel connector as a systemd service and can give the host a private network address that the tunnel routes to. Contract: [docs/specs/cloudflared.md](specs/cloudflared.md). Not supported on Rocky Linux ([rocky.md](rocky.md)).

## What gets installed

- `/usr/local/bin/cloudflared`, downloaded from the upstream releases.
- `cloudflared.service`, which reads the token from `/etc/cloudflared/tunnel.env` (mode 0600). The token never appears in the unit file or on a command line.
- With a private network address: a systemd-networkd dummy interface (`vlan0` by default) carrying that address, in `/etc/systemd/network/10-cloudflared-vlan.netdev` and `.network`.

Without a token, the module installs only the binary, plus the private network address when one is configured.

## Token sources

In order of precedence:

1. `--tunnel-token` on `apply` (or the token argument of `tunnel setup`).
2. `ROOTFILES_TUNNEL_TOKEN`.
3. `modules.cloudflared.tunnel_token` in the config.
4. `modules.cloudflared.tunnel_token_file` in the config.

A config may set `tunnel_token` or `tunnel_token_file`, not both. The fleet controller refuses a site config that resolves an inline `tunnel_token`; use the file.

## Token file

On a shared host, keep the token out of command lines and site configs:

```yaml
modules:
  cloudflared:
    enabled: true
    tunnel_token_file: /etc/rootfiles/tunnel-token   # root:root 0600
```

- The path must be absolute. `check` and `apply` read the file on the host.
- They refuse a symlink, a file that is not regular, a file not owned by root, a file readable by group or others, an empty file, and a token containing spaces, quotes or line breaks.
- `config show` prints only the path.

## Private network address

```yaml
modules:
  cloudflared:
    enabled: true
    private_network:
      enabled: true
      interface: vlan0              # default
      address: 203.0.113.10/32      # a CIDR
```

- No built-in profile sets a private network; the address belongs in the site config of the host that owns it (for example one that `extends: dgx`).
- `private_network.enabled: true` without an address is a validation error that names `modules.cloudflared.private_network.address`. Add the address, pass `--vlan-address`, or set `enabled: false`.
- `--vlan-address` and `ROOTFILES_VLAN_ADDRESS` set the address before validation; `ROOTFILES_VLAN_INTERFACE` sets the interface. `apply`, `check`, `config show`, `config validate` and `tunnel setup` read `--vlan-address`; `status` and `doctor` refuse it.
- `tunnel setup --vlan-address` (or `ROOTFILES_VLAN_ADDRESS`) also turns the private network on for that run, without the config toggle.
- Removing the block from a config leaves an existing interface in place; `tunnel uninstall` removes it.

## Commands

```bash
sudo rootfiles tunnel setup "$TOKEN" --vlan-address 203.0.113.10/32   # address, binary, service
rootfiles tunnel status                  # service and private network
sudo rootfiles tunnel restart
sudo rootfiles tunnel uninstall          # removes the service, the interface and the binary
```

- `tunnel setup` takes the token from its argument, `--tunnel-token`, `ROOTFILES_TUNNEL_TOKEN` or the config, and the address from `--vlan-address`, `ROOTFILES_VLAN_ADDRESS` or the config. Without a token it manages only the binary and a configured address.
- `tunnel install` is an alias of `tunnel setup`.
- The token argument is redacted in the audit log ([operations.md](operations.md#lock-and-audit-log)).

## Update cloudflared

Replace only the `cloudflared` binary; no other module runs:

```bash
sudo rootfiles tunnel update                     # latest upstream release
rootfiles tunnel update --check                  # report only; no root or lock
sudo rootfiles tunnel update --version 2024.9.1  # a specific release
```

- The service is restarted when it exists; on a host without the tunnel only the binary changes.
- `--check` exits 0 when current, 2 when an update is available or cloudflared is missing, and 1 on a lookup error.
