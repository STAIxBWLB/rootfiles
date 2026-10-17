package cli

import "github.com/STAIxBWLB/rootfiles/internal/config"

// Overridable in unit tests so native command tests never depend on the host distro.
var detectSystem = config.DetectSystem
