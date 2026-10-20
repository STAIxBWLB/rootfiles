package module

import (
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestRegistry_ResolveAll(t *testing.T) {
	reg := NewRegistry()
	cfg := &config.Config{
		Modules: config.ModulesConfig{
			Locale:   config.ModuleToggle{Enabled: true},
			System:   config.SystemConfig{Enabled: true},
			Packages: config.ModuleToggle{Enabled: true},
			SSH:      config.ModuleToggle{Enabled: true},
			Users:    config.ModuleToggle{Enabled: true},
			Security: config.SecurityConfig{Enabled: true},
			Docker:   config.DockerConfig{Enabled: true},
			Nvidia: config.NvidiaConfig{
				Enabled:       true,
				GPUAllocation: config.GPUAllocationConfig{Enabled: true},
			},
			Cloudflared: config.CloudflaredConfig{Enabled: true},
			Storage:     config.StorageConfig{Enabled: true},
			Network:     config.NetworkConfig{Enabled: true},
			Monitoring:  config.MonitoringConfig{Enabled: true},
			Tenant:      config.TenantConfig{Enabled: true},
		},
	}
	modules := reg.Resolve(cfg, nil)
	if len(modules) != 14 {
		t.Errorf("expected 14 modules, got %d", len(modules))
	}
	// Verify order
	expected := []string{"locale", "system", "packages", "users", "ssh", "security", "docker", "nvidia", "gpu", "cloudflared", "storage", "network", "monitoring", "tenant"}
	for i, m := range modules {
		if m.Name() != expected[i] {
			t.Errorf("module[%d] = %q, want %q", i, m.Name(), expected[i])
		}
	}
}

func TestRegistry_ResolveFiltered(t *testing.T) {
	reg := NewRegistry()
	cfg := &config.Config{
		Modules: config.ModulesConfig{
			Locale:      config.ModuleToggle{Enabled: true},
			Packages:    config.ModuleToggle{Enabled: true},
			SSH:         config.ModuleToggle{Enabled: true},
			Users:       config.ModuleToggle{Enabled: true},
			Docker:      config.DockerConfig{Enabled: true},
			Cloudflared: config.CloudflaredConfig{Enabled: true},
		},
	}
	modules := reg.Resolve(cfg, []string{"docker", "ssh"})
	if len(modules) != 2 {
		t.Errorf("expected 2 modules, got %d", len(modules))
	}
	if modules[0].Name() != "ssh" {
		t.Errorf("first module = %q, want ssh (should respect order)", modules[0].Name())
	}
	if modules[1].Name() != "docker" {
		t.Errorf("second module = %q, want docker", modules[1].Name())
	}
}

func TestRegistry_ResolveDisabledSkipped(t *testing.T) {
	reg := NewRegistry()
	cfg := &config.Config{
		Modules: config.ModulesConfig{
			Locale:   config.ModuleToggle{Enabled: true},
			Packages: config.ModuleToggle{Enabled: true},
			// Docker NOT enabled
		},
	}
	modules := reg.Resolve(cfg, nil)
	for _, m := range modules {
		if m.Name() == "docker" {
			t.Error("docker should not be in resolved modules (not enabled)")
		}
	}
}

func TestRegistry_ResolveEmpty(t *testing.T) {
	reg := NewRegistry()
	cfg := &config.Config{} // nothing enabled
	modules := reg.Resolve(cfg, nil)
	if len(modules) != 0 {
		t.Errorf("expected 0 modules, got %d", len(modules))
	}
}

// TestRegistryDefaultOrderSync guards against silent drift between the two
// static module contracts in module.go: NewRegistry() (what modules exist) and
// defaultOrder (what modules run, in what order). A module in only one of them
// is either dropped at runtime or never constructed — this test forces both
// lists to be updated together.
func TestRegistryDefaultOrderSync(t *testing.T) {
	reg := NewRegistry()

	registered := make(map[string]bool, len(reg.modules))
	for name := range reg.modules {
		registered[name] = true
	}

	ordered := make(map[string]bool, len(defaultOrder))
	for _, name := range defaultOrder {
		ordered[name] = true
	}

	var missingFromOrder []string
	for name := range registered {
		if !ordered[name] {
			missingFromOrder = append(missingFromOrder, name)
		}
	}
	var missingFromRegistry []string
	for name := range ordered {
		if !registered[name] {
			missingFromRegistry = append(missingFromRegistry, name)
		}
	}

	if len(missingFromOrder) > 0 {
		t.Errorf("modules registered but absent from defaultOrder (will never run): %v", missingFromOrder)
	}
	if len(missingFromRegistry) > 0 {
		t.Errorf("modules in defaultOrder but not registered (silently skipped): %v", missingFromRegistry)
	}
	if len(reg.modules) != len(defaultOrder) {
		t.Errorf("len(NewRegistry().modules)=%d, len(defaultOrder)=%d — counts differ", len(reg.modules), len(defaultOrder))
	}
}
