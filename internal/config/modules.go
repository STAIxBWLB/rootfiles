package config

// moduleDefinitions is the execution order and toggle mapping for native modules.
// Keep the registry constructor set in agreement with this list.
var moduleDefinitions = []struct {
	name    string
	enabled func(*Config) bool
}{
	{"locale", func(c *Config) bool { return c.Modules.Locale.Enabled }},
	{"system", func(c *Config) bool { return c.Modules.System.Enabled }}, // Before packages: the mirror applies to the first install.
	{"packages", func(c *Config) bool { return c.Modules.Packages.Enabled }},
	{"users", func(c *Config) bool { return c.Modules.Users.Enabled }}, // Before SSH: declared accounts and keys must exist.
	{"ssh", func(c *Config) bool { return c.Modules.SSH.Enabled }},
	{"security", func(c *Config) bool { return c.Modules.Security.Enabled }}, // After SSH: fail2ban follows the configured port.
	{"docker", func(c *Config) bool { return c.Modules.Docker.Enabled }},
	{"nvidia", func(c *Config) bool { return c.Modules.Nvidia.Enabled }},
	{"gpu", func(c *Config) bool { return c.Modules.Nvidia.GPUAllocation.Enabled }},
	{"cloudflared", func(c *Config) bool { return c.Modules.Cloudflared.Enabled }},
	{"storage", func(c *Config) bool { return c.Modules.Storage.Enabled }},
	{"network", func(c *Config) bool { return c.Modules.Network.Enabled }},
	{"monitoring", func(c *Config) bool { return c.Modules.Monitoring.Enabled }},
	{"tenant", func(c *Config) bool { return c.Modules.Tenant.Enabled }}, // After Docker and NVIDIA: tenant containers need both.
}

// ModuleNames returns the module execution order as an independent slice.
func ModuleNames() []string {
	names := make([]string, len(moduleDefinitions))
	for i, definition := range moduleDefinitions {
		names[i] = definition.name
	}
	return names
}

// IsModuleEnabled reports the configured toggle; unknown names are disabled.
func (c *Config) IsModuleEnabled(name string) bool {
	for _, definition := range moduleDefinitions {
		if definition.name == name {
			return definition.enabled(c)
		}
	}
	return false
}
