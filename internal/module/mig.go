package module

import (
	"context"
	"fmt"
	"strings"
)

// MIGGPU is the MIG state of one physical GPU.
type MIGGPU struct {
	Index     string
	Name      string
	Current   string // Enabled | Disabled | [N/A]
	Pending   string
	Instances []string // "MIG 3g.40gb Device 0 (UUID: MIG-…)"
}

// MIGStatus reads MIG mode and instances. It is read-only: repartitioning
// destroys running instances and may need a GPU reset, so it is left to
// the operator (nvidia-smi mig) rather than automated.
func MIGStatus(ctx context.Context, rc *RunContext) ([]MIGGPU, error) {
	res, err := rc.Runner.Query(ctx, "nvidia-smi", "--query-gpu=index,name,mig.mode.current,mig.mode.pending", "--format=csv,noheader")
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi: %w", err)
	}
	var gpus []MIGGPU
	byIndex := map[string]int{}
	for _, line := range nonEmptyLines(res.Stdout) {
		f := strings.Split(line, ",")
		if len(f) < 4 {
			continue
		}
		g := MIGGPU{Index: strings.TrimSpace(f[0]), Name: strings.TrimSpace(f[1]),
			Current: strings.TrimSpace(f[2]), Pending: strings.TrimSpace(f[3])}
		byIndex[g.Index] = len(gpus)
		gpus = append(gpus, g)
	}

	// `nvidia-smi -L` lists MIG devices indented under their GPU. A failed
	// listing is an error, not a GPU without instances.
	res, err = rc.Runner.Query(ctx, "nvidia-smi", "-L")
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi -L: %w", err)
	}
	cur := -1
	for _, line := range strings.Split(res.Stdout, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "GPU "):
			idx := strings.TrimSuffix(strings.Fields(t)[1], ":")
			if i, ok := byIndex[idx]; ok {
				cur = i
			} else {
				cur = -1
			}
		case strings.HasPrefix(t, "MIG ") && cur >= 0:
			gpus[cur].Instances = append(gpus[cur].Instances, t)
		}
	}
	return gpus, nil
}
