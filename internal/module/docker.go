package module

import (
	"context"
	"fmt"
	"os"
)

type DockerModule struct{}

func NewDockerModule() *DockerModule { return &DockerModule{} }
func (m *DockerModule) Name() string { return "docker" }

var dockerComposePath = "/usr/local/bin/docker-compose"

var dockerPackages = []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"}

var composePluginPaths = []string{
	"/usr/libexec/docker/cli-plugins/docker-compose",
	"/usr/lib/docker/cli-plugins/docker-compose",
}

// daemonUpdates returns the daemon.json keys this module wants to change.
// Only data-root is enforced; log rotation defaults are added when the
// operator has not chosen a log driver. Other keys are left untouched.
func (m *DockerModule) daemonUpdates(cfg map[string]any, storageDir string) map[string]any {
	updates := map[string]any{}
	if storageDir == "" {
		return updates
	}
	if cur, _ := cfg["data-root"].(string); cur != storageDir {
		updates["data-root"] = storageDir
	}
	if _, ok := cfg["log-driver"]; !ok {
		updates["log-driver"] = "json-file"
		updates["log-opts"] = map[string]any{"max-size": "10m", "max-file": "3"}
	}
	return updates
}

func (m *DockerModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	var changes []Change
	restart := false

	if !rc.Runner.CommandExists("docker") {
		changes = append(changes, Change{
			Description: "Install Docker CE",
			Command:     packageInstallCommand(rc, dockerPackages),
		})
	}

	if !rc.Runner.FileExists(dockerComposePath) {
		for _, source := range composePluginPaths {
			if rc.Runner.FileExists(source) {
				changes = append(changes, Change{
					Description: "Create docker-compose compatibility symlink",
					Command:     "ln -sf " + source + " " + dockerComposePath,
				})
				break
			}
		}
	}

	cfg := rc.Config.Modules.Docker
	if cfg.StorageDir != "" {
		daemon, err := readDaemonJSON(rc)
		if err != nil {
			return nil, err
		}
		for key, val := range m.daemonUpdates(daemon, cfg.StorageDir) {
			restart = true
			changes = append(changes, Change{
				Description: fmt.Sprintf("Set Docker daemon %s = %v", key, val),
				Command:     "merge into " + dockerDaemonJSONPath,
			})
		}
	}

	if !serviceActive(ctx, rc, "docker") || !serviceEnabled(ctx, rc, "docker") {
		changes = append(changes, Change{Description: "Enable and start Docker service", Command: "systemctl enable --now docker"})
	}
	if restart {
		changes = append(changes, Change{Description: "Restart Docker after daemon configuration changes", Command: "systemctl restart docker"})
	}

	return &CheckResult{
		Satisfied: len(changes) == 0,
		Changes:   changes,
	}, nil
}

func (m *DockerModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	var messages, warnings []string
	changed := false
	cfg := rc.Config.Modules.Docker

	if !rc.Runner.CommandExists("docker") {
		codename := "jammy"
		if rc.Config.System != nil && rc.Config.System.Codename != "" {
			codename = rc.Config.System.Codename
		}
		arch := "amd64"
		if rc.Config.System != nil && rc.Config.System.Arch != "" {
			arch = rc.Config.System.Arch
		}

		if err := rc.APT.AddKeyring(ctx, "docker", "https://download.docker.com/linux/ubuntu/gpg"); err != nil {
			return nil, fmt.Errorf("adding docker apt key: %w", err)
		}
		repoLine := fmt.Sprintf("deb [arch=%s signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu %s stable",
			arch, codename)
		if err := rc.APT.AddSourceList(ctx, "docker", repoLine); err != nil {
			return nil, fmt.Errorf("adding docker apt source: %w", err)
		}
		if err := rc.APT.Update(ctx); err != nil {
			return nil, fmt.Errorf("apt update: %w", err)
		}

		if err := rc.APT.Install(ctx, dockerPackages); err != nil {
			return nil, fmt.Errorf("installing docker: %w", err)
		}
		messages = append(messages, "Docker CE installed")
		changed = true
	}

	// Merge managed keys into daemon.json, preserving everything else
	// (notably the nvidia runtime registered by nvidia-ctk or DGX OS).
	restart := false
	if cfg.StorageDir != "" {
		daemon, err := readDaemonJSON(rc)
		if err != nil {
			return nil, err
		}
		updates := m.daemonUpdates(daemon, cfg.StorageDir)
		if len(updates) > 0 {
			if old, _ := daemon["data-root"].(string); updates["data-root"] != nil {
				if old == "" {
					old = "/var/lib/docker"
				}
				if hasEntries(old) {
					warnings = append(warnings, fmt.Sprintf(
						"existing images/volumes in %s are not migrated to %s", old, cfg.StorageDir))
				}
			}
			for k, v := range updates {
				daemon[k] = v
			}
			if err := rc.Runner.MkdirAll(cfg.StorageDir, 0710); err != nil {
				return nil, fmt.Errorf("creating docker data-root: %w", err)
			}
			if err := writeDaemonJSON(rc, daemon); err != nil {
				return nil, fmt.Errorf("writing daemon.json: %w", err)
			}
			messages = append(messages, fmt.Sprintf("Docker data-root set to %s", cfg.StorageDir))
			changed = true
			restart = true
		}
	}

	// docker-compose v1 compatibility symlink
	if !rc.Runner.FileExists(dockerComposePath) {
		for _, src := range composePluginPaths {
			if rc.Runner.FileExists(src) {
				// A dangling symlink fails FileExists but blocks Symlink.
				if fi, err := os.Lstat(dockerComposePath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
					if err := rc.Runner.Remove(dockerComposePath); err != nil {
						return nil, fmt.Errorf("removing stale docker-compose symlink: %w", err)
					}
				}
				if err := rc.Runner.Symlink(src, dockerComposePath); err != nil {
					return nil, fmt.Errorf("creating docker-compose symlink: %w", err)
				}
				messages = append(messages, "docker-compose compatibility symlink created")
				changed = true
				break
			}
		}
	}

	enableOK := true
	if !serviceActive(ctx, rc, "docker") || !serviceEnabled(ctx, rc, "docker") {
		if _, err := rc.Runner.Run(ctx, "systemctl", "enable", "--now", "docker"); err != nil {
			warnings = append(warnings, fmt.Sprintf("enabling docker service: %s", firstLine(err.Error())))
			enableOK = false
		} else {
			messages = append(messages, "Docker service enabled and started")
			changed = true
		}
	}
	if restart && enableOK {
		if _, err := rc.Runner.Run(ctx, "systemctl", "restart", "docker"); err != nil {
			warnings = append(warnings, fmt.Sprintf("restarting docker: %s", firstLine(err.Error())))
		}
	}

	return &ApplyResult{Changed: changed, Messages: messages, Warnings: warnings}, nil
}

// hasEntries reports whether dir exists and contains anything.
func hasEntries(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()
	names, _ := f.Readdirnames(1)
	return len(names) > 0
}
