package exec

import (
	"context"
	"fmt"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

// PackageManager is the package operation contract shared by distro
// backends. Repo/key methods remain for existing Ubuntu modules and fail
// explicitly on backends that do not implement APT source semantics.
type PackageManager interface {
	Update(context.Context) error
	Install(context.Context, []string) error
	IsInstalled(string) bool
	Installed([]string) map[string]bool
	AddKeyring(context.Context, string, string) error
	AddSourceList(context.Context, string, string) error
}

// NewPackageManager constructs the native package backend for a supported OS.
func NewPackageManager(runner *Runner, system *config.SystemInfo) (PackageManager, error) {
	distro := config.ResolveDistro(system)
	if !distro.Supported {
		return nil, fmt.Errorf("cannot select package manager: %s", distro.Reason)
	}
	switch distro.PackageBackend {
	case "apt":
		return NewAPT(runner), nil
	case "dnf":
		return NewRPM(runner), nil
	default:
		return nil, fmt.Errorf("unsupported package backend %q", distro.PackageBackend)
	}
}
