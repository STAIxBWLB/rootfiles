package module

import (
	"strings"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func packageInstallCommand(rc *RunContext, packages []string) string {
	if rc.Config != nil && config.IsRocky(rc.Config.System) {
		return "dnf install " + strings.Join(packages, " ")
	}
	return "apt-get install -y " + strings.Join(packages, " ")
}

func packageUpdateCommand(rc *RunContext) string {
	if rc.Config != nil && config.IsRocky(rc.Config.System) {
		return "dnf makecache"
	}
	return "apt-get update"
}
