package fleet

import (
	"context"
	"fmt"
	"strings"
)

// Ping checks connectivity, binary version and the configured privilege mode.
// The commands below are fixed, read-only and do not write on the remote host.
func (r SSHRunner) Ping(ctx context.Context, h NamedHost, controllerVersion string) Result {
	result := r.run(ctx, h, "ping", remoteBinary+" --version", nil)
	if result.State != "ok" {
		return pingFailure(result, h)
	}
	version := strings.Fields(result.Output)
	if len(version) < 3 || version[0] != "rootfiles" || version[1] != "version" {
		result.State = "error"
		result.Reason = "remote version output is unrecognized; inspect the remote rootfiles binary"
		return result
	}
	remoteVersion := version[2]
	switch h.Sudo {
	case "nopasswd":
		probe := "sudo -n " + readOnlyRemoteCommand("status")
		check := r.run(ctx, h, "status", probe, nil)
		check.Command = "ping"
		if check.State != "ok" {
			if check.State == "needs-privilege" {
				check.Reason = "read-only sudo needs privilege; configure users.fleet_sudo_users"
				return check
			}
			if check.State == "unreachable" {
				return pingFailure(check, h)
			}
			check.State = "error"
			if check.Reason == "" {
				check.Reason = "remote status probe failed; inspect the host's status"
			}
			return check
		}
	case "root":
		check := r.run(ctx, h, "ping", "id -u", nil)
		if check.State != "ok" {
			return pingFailure(check, h)
		}
		if strings.TrimSpace(check.Output) != "0" {
			check.State = "needs-privilege"
			check.Reason = "sudo mode root requires a root SSH login; configure the SSH user or inventory sudo mode"
			return check
		}
	}
	result.Output = remoteVersion
	if controllerVersion != "" && controllerVersion != "dev" && remoteVersion != controllerVersion {
		result.State = "version-skew"
		result.Reason = fmt.Sprintf("remote version %s differs from controller %s; update the host", remoteVersion, controllerVersion)
	} else {
		result.Reason = "version " + remoteVersion
	}
	return result
}

func pingFailure(result Result, h NamedHost) Result {
	output := strings.ToLower(result.Output)
	destination := ShellQuote(h.Host.SSH)
	switch {
	case result.Reason == "ssh timed out" || strings.Contains(output, "connection timed out"):
		result.State, result.Reason = "timeout", "SSH timed out; check network reachability and SSH configuration"
	case strings.Contains(output, "remote host identification has changed") || strings.Contains(output, "host key has changed"):
		result.State, result.Reason = "host-key-changed", "SSH host key changed; verify it out of band before updating known_hosts"
	case strings.Contains(output, "host key verification failed") || strings.Contains(output, "no host key is known"):
		result.State, result.Reason = "host-key-unknown", "SSH host key is untrusted; verify it out of band, then run ssh "+destination
	case strings.Contains(output, "permission denied") && result.ExitCode == 255:
		result.State, result.Reason = "auth-failed", "SSH authentication failed; check the key and run ssh "+destination
	case missingRemoteBinary(result) || result.ExitCode == 127:
		result.State, result.Reason = "missing", "rootfiles is not installed; use fleet bootstrap with a pinned --version for host "+h.Name
	default:
		result.Reason = "SSH or remote version check failed; inspect the connection with ssh " + destination
	}
	return result
}
