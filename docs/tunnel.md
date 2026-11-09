# Cloudflare tunnel

The `cloudflared` module installs the Cloudflare Tunnel connector as a systemd service and can give the host a private network address that the tunnel routes to. Contract: [docs/specs/cloudflared.md](specs/cloudflared.md). Not supported on Rocky Linux ([rocky.md](rocky.md)).

## What gets installed

- `/usr/local/bin/cloudflared`, downloaded from the upstream releases.
- `cloudflared.service`, which reads the token from `/etc/cloudflared/tunnel.env` (mode 0600). The token never appears in the unit file or on a command line.
- With a private network address: a systemd-networkd dummy interface (`vlan0` by default) carrying that address, in `/etc/systemd/network/10-cloudflared-vlan.netdev` and `.network`.

Without a token, the module installs only the binary.

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

- `apply` configures the interface only when `private_network.enabled` is true and an address is set. `--vlan-address` and `ROOTFILES_VLAN_ADDRESS` set the address; `ROOTFILES_VLAN_INTERFACE` sets the interface.
- `tunnel setup --vlan-address` configures the address directly, without the config toggle.

## Commands

```bash
sudo rootfiles tunnel setup "$TOKEN" --vlan-address 203.0.113.10/32   # address, binary, service
sudo rootfiles tunnel install            # runs the cloudflared module: binary, plus service and address when configured
rootfiles tunnel status                  # service and private network
sudo rootfiles tunnel restart
sudo rootfiles tunnel uninstall          # removes the service, the interface and the binary
```

`tunnel setup` also takes the token from `--tunnel-token` or `ROOTFILES_TUNNEL_TOKEN`, and the address from `ROOTFILES_VLAN_ADDRESS`. Its token argument is redacted in the audit log ([operations.md](operations.md#lock-and-audit-log)).

## Update cloudflared

Replace only the `cloudflared` binary; no other module runs:

```bash
sudo rootfiles tunnel update                     # latest upstream release
rootfiles tunnel update --check                  # report only; no root or lock
sudo rootfiles tunnel update --version 2024.9.1  # a specific release
```

- The service is restarted when it exists; on a host without the tunnel only the binary changes.
- `--check` exits 0 when current, 2 when an update is available or cloudflared is missing, and 1 on a lookup error.
