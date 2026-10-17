package module

import (
	"context"
	"fmt"
	"strings"
)

type NvidiaModule struct{}

func NewNvidiaModule() *NvidiaModule { return &NvidiaModule{} }
func (m *NvidiaModule) Name() string { return "nvidia" }

// needsRuntime reports whether Docker is present but lacks the nvidia
// runtime. Hosts without Docker have nothing to configure.
func (m *NvidiaModule) needsRuntime(rc *RunContext) (bool, error) {
	if !rc.Runner.CommandExists("docker") {
		return false, nil
	}
	daemon, err := readDaemonJSON(rc)
	if err != nil {
		return false, err
	}
	return !hasNvidiaRuntime(daemon), nil
}

func (m *NvidiaModule) Check(_ context.Context, rc *RunContext) (*CheckResult, error) {
	var changes []Change

	if !rc.APT.IsInstalled("nvidia-container-toolkit") {
		changes = append(changes, Change{
			Description: "Install NVIDIA Container Toolkit",
			Command:     "apt-get install nvidia-container-toolkit",
		})
	}

	need, err := m.needsRuntime(rc)
	if err != nil {
		return nil, err
	}
	if need {
		changes = append(changes, Change{
			Description: "Configure Docker nvidia runtime",
			Command:     "nvidia-ctk runtime configure --runtime=docker",
		})
	}

	for _, svc := range m.services(rc) {
		if !serviceActive(context.Background(), rc, svc) {
			changes = append(changes, Change{Description: "Enable " + svc, Command: "systemctl enable --now " + svc})
		}
	}

	return &CheckResult{
		Satisfied: len(changes) == 0,
		Changes:   changes,
	}, nil
}

// services lists the driver-side daemons to keep running. Each is only
// managed when its unit is installed (DGX OS / the driver packages ship
// them); rootfiles does not install driver components.
func (m *NvidiaModule) services(rc *RunContext) []string {
	cfg := rc.Config.Modules.Nvidia
	var out []string
	if cfg.Persistenced && unitInstalled(context.Background(), rc, "nvidia-persistenced") {
		out = append(out, "nvidia-persistenced")
	}
	if cfg.FabricManager && unitInstalled(context.Background(), rc, "nvidia-fabricmanager") {
		out = append(out, "nvidia-fabricmanager")
	}
	return out
}

func unitInstalled(ctx context.Context, rc *RunContext, unit string) bool {
	res, err := rc.Runner.Query(ctx, "systemctl", "list-unit-files", unit+".service", "--no-legend")
	return err == nil && strings.Contains(res.Stdout, unit+".service")
}

func serviceActive(ctx context.Context, rc *RunContext, unit string) bool {
	res, err := rc.Runner.Query(ctx, "systemctl", "is-active", unit)
	return err == nil && strings.TrimSpace(res.Stdout) == "active"
}

func (m *NvidiaModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	var messages, warnings []string
	changed := false

	installed := false
	if !rc.APT.IsInstalled("nvidia-container-toolkit") {
		if err := rc.APT.AddKeyring(ctx, "nvidia-container-toolkit",
			"https://nvidia.github.io/libnvidia-container/gpgkey"); err != nil {
			return nil, fmt.Errorf("adding nvidia apt key: %w", err)
		}

		arch := "amd64"
		if rc.Config.System != nil && rc.Config.System.Arch != "" {
			arch = rc.Config.System.Arch
		}
		repoLine := fmt.Sprintf(
			"deb [signed-by=/etc/apt/keyrings/nvidia-container-toolkit.gpg arch=%s] https://nvidia.github.io/libnvidia-container/stable/deb/$(ARCH) /",
			arch)
		if err := rc.APT.AddSourceList(ctx, "nvidia-container-toolkit", repoLine); err != nil {
			return nil, fmt.Errorf("adding nvidia apt source: %w", err)
		}
		if err := rc.APT.Update(ctx); err != nil {
			return nil, fmt.Errorf("apt update: %w", err)
		}
		if err := rc.APT.Install(ctx, []string{"nvidia-container-toolkit"}); err != nil {
			return nil, fmt.Errorf("installing nvidia-container-toolkit: %w", err)
		}
		messages = append(messages, "NVIDIA Container Toolkit installed")
		changed = true
		installed = true
	}

	// Runtime registration is independent of installation: a preinstalled
	// toolkit (DGX OS) may still be missing from a rewritten daemon.json.
	need, err := m.needsRuntime(rc)
	if err != nil {
		return nil, err
	}
	if need {
		if _, err := rc.Runner.Run(ctx, "nvidia-ctk", "runtime", "configure", "--runtime=docker"); err != nil {
			return nil, fmt.Errorf("configuring docker nvidia runtime: %w", err)
		}
		// dockerd rereads registered runtimes on reload, so registering one
		// on a host that already had the toolkit keeps containers running.
		action := "reload"
		if installed {
			action = "restart"
		}
		if _, err := rc.Runner.Run(ctx, "systemctl", action, "docker"); err != nil {
			warnings = append(warnings, fmt.Sprintf("%sing docker: %s", action, firstLine(err.Error())))
		}
		messages = append(messages, "Docker nvidia runtime configured")
		changed = true
	}

	for _, svc := range m.services(rc) {
		if serviceActive(ctx, rc, svc) {
			continue
		}
		if _, err := rc.Runner.Run(ctx, "systemctl", "enable", "--now", svc); err != nil {
			warnings = append(warnings, "enabling "+svc+": "+firstLine(err.Error()))
			continue
		}
		messages = append(messages, svc+" enabled")
		changed = true
	}

	return &ApplyResult{Changed: changed, Messages: messages, Warnings: warnings}, nil
}
