package module

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func rockySSHDMainState(rc *RunContext) (original, updated []byte, mode os.FileMode, changed bool, err error) {
	original, err = rc.Runner.ReadFile(sshdConfigPath)
	if err != nil {
		return nil, nil, 0, false, fmt.Errorf("cannot read Rocky sshd configuration %s: %w", sshdConfigPath, err)
	}
	info, err := os.Lstat(sshdConfigPath)
	if err != nil {
		return nil, nil, 0, false, fmt.Errorf("inspecting Rocky sshd configuration %s: %w", sshdConfigPath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, 0, false, fmt.Errorf("Rocky sshd configuration %s is not a regular file; refusing to update it", sshdConfigPath)
	}
	updated, changed, err = addRockySSHDInclude(original)
	if err != nil {
		return nil, nil, 0, false, err
	}
	return original, updated, info.Mode().Perm(), changed, nil
}

func addRockySSHDInclude(original []byte) ([]byte, bool, error) {
	matchSeen := false
	for _, line := range strings.Split(string(original), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if strings.EqualFold(fields[0], "Match") {
			matchSeen = true
			continue
		}
		if !strings.EqualFold(fields[0], "Include") {
			continue
		}
		for _, pattern := range fields[1:] {
			if isManagedSSHDInclude(pattern) {
				if matchSeen {
					return nil, false, fmt.Errorf("Rocky sshd_config.d include is inside a Match block; refusing to change authentication scope")
				}
				return original, false, nil
			}
		}
	}
	include := []byte("Include " + sshdConfigDir + "/*.conf\n")
	updated := make([]byte, 0, len(include)+len(original))
	updated = append(updated, include...)
	updated = append(updated, original...)
	return updated, true, nil
}

func isManagedSSHDInclude(pattern string) bool {
	pattern = strings.Trim(pattern, `"'`)
	return pattern == sshdConfigDir+"/*.conf" || pattern == sshdConfigDir+"/*"
}

func ensureRockySSHSELinuxPort(ctx context.Context, rc *RunContext, port int) (bool, error) {
	if port == 22 {
		return false, nil
	}
	labeled, err := rockySSHSELinuxPortLabeled(ctx, rc, port)
	if err != nil || labeled {
		return false, err
	}
	if _, err := rc.Runner.Run(ctx, "semanage", "port", "-a", "-t", "ssh_port_t", "-p", "tcp", strconv.Itoa(port)); err != nil {
		return false, fmt.Errorf("labeling TCP port %d for SSH: %w", port, err)
	}
	return true, nil
}

func rockySSHSELinuxPortLabeled(ctx context.Context, rc *RunContext, port int) (bool, error) {
	if port == 22 {
		return true, nil
	}
	if !rc.Runner.CommandExists("semanage") {
		return false, fmt.Errorf("semanage is required to label SSH port %d for SELinux; install policycoreutils-python-utils before changing sshd", port)
	}
	res, err := rc.Runner.Query(ctx, "semanage", "port", "-l")
	if err != nil {
		return false, fmt.Errorf("reading SELinux port labels: %w", err)
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "tcp" || !portInRangeList(fields[2:], port) {
			continue
		}
		if fields[0] == "ssh_port_t" {
			return true, nil
		}
		return false, fmt.Errorf("SELinux TCP port %d is already labeled %s; refusing to relabel it for SSH", port, fields[0])
	}
	return false, nil
}

func portInRangeList(groups []string, port int) bool {
	for _, group := range groups {
		for _, item := range strings.Split(group, ",") {
			bounds := strings.SplitN(item, "-", 2)
			low, err := strconv.Atoi(bounds[0])
			if err != nil {
				continue
			}
			high := low
			if len(bounds) == 2 {
				high, err = strconv.Atoi(bounds[1])
				if err != nil {
					continue
				}
			}
			if low <= port && port <= high {
				return true
			}
		}
	}
	return false
}

func queryFirewalldBoolean(ctx context.Context, rc *RunContext, args ...string) (bool, error) {
	res, err := rc.Runner.Query(ctx, "firewall-cmd", args...)
	if res != nil {
		switch strings.TrimSpace(res.Stdout) {
		case "yes":
			if err != nil || res.ExitCode != 0 {
				if err == nil {
					return false, fmt.Errorf("firewalld returned yes with exit status %d", res.ExitCode)
				}
				return false, err
			}
			return true, nil
		case "no":
			if (err == nil && res.ExitCode == 0) || (err != nil && res.ExitCode == 1) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			return false, fmt.Errorf("firewalld returned no with unexpected exit status %d", res.ExitCode)
		}
	}
	if err != nil {
		return false, err
	}
	if res == nil {
		return false, fmt.Errorf("firewalld returned no query result")
	}
	return false, fmt.Errorf("unexpected firewalld query output %q", strings.TrimSpace(res.Stdout))
}

func parseFirewalldZones(out string) []string {
	var zones []string
	for _, line := range strings.Split(out, "\n") {
		if line == "" || (line[0] != ' ' && line[0] != '\t') {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				zones = append(zones, fields[0])
			}
		}
	}
	return zones
}

func verifyRockySSHEffective(ctx context.Context, rc *RunContext, bin string) error {
	res, err := rc.Runner.Query(ctx, bin, "-T")
	if err != nil {
		return fmt.Errorf("reading effective sshd configuration: %w", err)
	}
	want := map[string]string{}
	cfg := rc.Config.SSH
	if cfg.DisableRootLogin {
		want["permitrootlogin"] = "no"
	}
	if cfg.DisablePasswordAuth {
		want["passwordauthentication"] = "no"
		want["kbdinteractiveauthentication"] = "no"
	}
	if cfg.Port > 0 {
		want["port"] = strconv.Itoa(cfg.Port)
	}
	if cfg.MaxAuthTries > 0 {
		want["maxauthtries"] = strconv.Itoa(cfg.MaxAuthTries)
	}
	got := make(map[string]string)
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			got[strings.ToLower(fields[0])] = strings.ToLower(strings.Join(fields[1:], " "))
		}
	}
	for key, expected := range want {
		if got[key] != expected {
			return fmt.Errorf("effective sshd %s is %q, want %q", key, got[key], expected)
		}
	}
	for _, user := range cfg.PasswordAuthUsers {
		args := []string{"-T", "-C", "user=" + user + ",host=localhost,addr=127.0.0.1"}
		match, err := rc.Runner.Query(ctx, bin, args...)
		if err != nil {
			return fmt.Errorf("reading effective sshd configuration for password exception %q: %w", user, err)
		}
		values := effectiveSSHValues(match.Stdout)
		if values["passwordauthentication"] != "yes" || values["kbdinteractiveauthentication"] != "yes" {
			return fmt.Errorf("effective sshd configuration does not preserve password authentication for %q", user)
		}
	}
	return nil
}

func effectiveSSHValues(out string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			values[strings.ToLower(fields[0])] = strings.ToLower(strings.Join(fields[1:], " "))
		}
	}
	return values
}
