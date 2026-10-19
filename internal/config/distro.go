package config

import (
	"fmt"
	"strings"
)

// Distro describes the operating-system capabilities rootfiles knows how to
// use. It deliberately contains only tested distribution families.
type Distro struct {
	ID             string
	PackageBackend string
	AdminGroup     string
	LocalePath     string
	SSHService     string
	Supported      bool
	Reason         string
}

// ResolveDistro resolves only platforms with an explicit compatibility
// contract. Rocky 8 is limited to the versions exercised for this release.
func ResolveDistro(system *SystemInfo) Distro {
	if system == nil {
		return Distro{Reason: "operating system was not detected"}
	}
	switch strings.ToLower(system.OS) {
	case "ubuntu":
		return Distro{ID: "ubuntu", PackageBackend: "apt", AdminGroup: "sudo", LocalePath: "/etc/default/locale", SSHService: "ssh", Supported: true}
	case "dgx-os":
		return Distro{ID: "dgx-os", PackageBackend: "apt", AdminGroup: "sudo", LocalePath: "/etc/default/locale", SSHService: "ssh", Supported: true}
	case "rocky":
		major, _, _ := strings.Cut(system.Version, ".")
		supported := major == "9" || system.Version == "8.9" || system.Version == "8.10"
		if !supported {
			return Distro{ID: "rocky", Reason: fmt.Sprintf("Rocky Linux %q is outside the supported matrix (8.9, 8.10, 9.x)", system.Version)}
		}
		return Distro{ID: "rocky", PackageBackend: "dnf", AdminGroup: "wheel", LocalePath: "/etc/locale.conf", SSHService: "sshd", Supported: true}
	default:
		return Distro{ID: strings.ToLower(system.OS), Reason: fmt.Sprintf("distribution %q is not supported", system.OS)}
	}
}

// IsRocky reports whether system resolves to a supported Rocky release.
func IsRocky(system *SystemInfo) bool {
	d := ResolveDistro(system)
	return d.ID == "rocky" && d.Supported
}

// AdminGroup returns the native administrator group for a supported distro.
func AdminGroup(system *SystemInfo) string {
	d := ResolveDistro(system)
	if d.Supported {
		return d.AdminGroup
	}
	return "sudo"
}

// SupportedModules returns the core module set validated for a platform.
// Optional modules require their own platform-specific evidence before they
// are added to this set.
func SupportedModules(system *SystemInfo) map[string]bool {
	d := ResolveDistro(system)
	if !d.Supported {
		return map[string]bool{}
	}
	if d.ID != "rocky" {
		all := []string{"locale", "system", "packages", "users", "ssh", "security", "docker", "nvidia", "gpu", "cloudflared", "storage", "network", "monitoring", "tenant"}
		result := make(map[string]bool, len(all))
		for _, name := range all {
			result[name] = true
		}
		return result
	}
	core := []string{"locale", "system", "packages", "users", "ssh", "security"}
	result := make(map[string]bool, len(core))
	for _, name := range core {
		result[name] = true
	}
	return result
}

// ValidateCapabilities rejects enabled operations that have not been
// validated for the detected platform. It is intentionally called before
// module execution so a mixed supported/unsupported profile makes no change.
func ValidateCapabilities(cfg *Config, system *SystemInfo, enabledModules []string) error {
	d := ResolveDistro(system)
	if !d.Supported {
		return fmt.Errorf("unsupported operating system: %s", d.Reason)
	}
	if cfg == nil {
		return fmt.Errorf("configuration is required for capability validation")
	}
	if d.ID != "rocky" {
		return nil
	}
	requested := make(map[string]bool, len(enabledModules))
	for _, name := range enabledModules {
		requested[name] = true
	}
	if requested["system"] && cfg.Modules.System.AptMirror != "" {
		return fmt.Errorf("modules.system.apt_mirror is unsupported on Rocky Linux; configure DNF repositories separately")
	}
	supported := SupportedModules(system)
	var unsupported []string
	for _, name := range enabledModules {
		if !supported[name] {
			unsupported = append(unsupported, name)
		}
	}
	if requested["security"] && cfg.Modules.Security.Fail2ban {
		unsupported = append(unsupported, "security.fail2ban (requires an explicitly configured EPEL repository)")
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("unsupported modules or options on Rocky Linux: %s", strings.Join(unsupported, ", "))
	}
	return nil
}
