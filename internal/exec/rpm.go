package exec

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var validRPMName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+:-]*$`)

// rpmInstalledAlternatives maps logical profile packages to native variants
// that provide the same command on supported RPM-family systems.
var rpmInstalledAlternatives = map[string][]string{
	"curl": {"curl-minimal"},
}

// RPM wraps native DNF installation and RPM database queries.
type RPM struct {
	Runner *Runner
}

// NewRPM creates a native DNF/RPM package wrapper.
func NewRPM(runner *Runner) *RPM { return &RPM{Runner: runner} }

// Update refreshes repository metadata only; it does not upgrade installed
// packages.
func (r *RPM) Update(ctx context.Context) error {
	_, err := r.Runner.Run(ctx, "dnf", "-q", "makecache")
	return err
}

// Install installs packages without enabling DNF's allowerasing behavior.
func (r *RPM) Install(ctx context.Context, packages []string) error {
	if len(packages) == 0 {
		return nil
	}
	for _, name := range packages {
		if !validRPMName.MatchString(name) {
			return fmt.Errorf("invalid RPM package name %q", name)
		}
	}
	args := []string{"-y", "--setopt=install_weak_deps=False", "install"}
	args = append(args, packages...)
	_, err := r.Runner.Run(ctx, "dnf", args...)
	return err
}

func (r *RPM) IsInstalled(pkg string) bool { return r.Installed([]string{pkg})[pkg] }

// Installed queries RPM once. `rpm -q` may return non-zero if any requested
// package is missing, while still returning installed package names.
func (r *RPM) Installed(packages []string) map[string]bool {
	installed := make(map[string]bool)
	if len(packages) == 0 {
		return installed
	}
	requested := make([]string, 0, len(packages))
	seen := make(map[string]bool, len(packages))
	for _, name := range packages {
		if validRPMName.MatchString(name) && !seen[name] {
			requested = append(requested, name)
			seen[name] = true
		}
	}
	if len(requested) == 0 {
		return installed
	}
	queryNames := append([]string(nil), requested...)
	for _, name := range requested {
		for _, alternative := range rpmInstalledAlternatives[name] {
			if validRPMName.MatchString(alternative) && !seen[alternative] {
				queryNames = append(queryNames, alternative)
				seen[alternative] = true
			}
		}
	}
	args := append([]string{"-q", "--qf", "%{NAME}\\n"}, queryNames...)
	res, _ := r.Runner.Query(context.Background(), "rpm", args...)
	if res == nil {
		return installed
	}
	found := make(map[string]bool)
	for _, line := range strings.Split(res.Stdout, "\n") {
		name := strings.TrimSpace(line)
		if validRPMName.MatchString(name) {
			found[name] = true
		}
	}
	for _, name := range requested {
		if found[name] {
			installed[name] = true
			continue
		}
		for _, alternative := range rpmInstalledAlternatives[name] {
			if found[alternative] {
				installed[name] = true
				break
			}
		}
	}
	return installed
}
