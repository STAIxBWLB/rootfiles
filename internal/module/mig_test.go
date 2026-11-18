package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMIGStatus(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
case "$*" in
  *mig.mode.current*) printf '0, NVIDIA A100-SXM4-80GB, Enabled, Enabled\n1, NVIDIA A100-SXM4-80GB, Disabled, Enabled\n' ;;
  -L) printf 'GPU 0: NVIDIA A100-SXM4-80GB (UUID: GPU-aaa)\n  MIG 3g.40gb     Device  0: (UUID: MIG-111)\n  MIG 3g.40gb     Device  1: (UUID: MIG-222)\nGPU 1: NVIDIA A100-SXM4-80GB (UUID: GPU-bbb)\n' ;;
esac
`
	os.WriteFile(filepath.Join(dir, "nvidia-smi"), []byte(script), 0755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	gpus, err := MIGStatus(context.Background(), newDryRunRC(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(gpus) != 2 || gpus[0].Current != "Enabled" || len(gpus[0].Instances) != 2 {
		t.Fatalf("unexpected: %+v", gpus)
	}
	if gpus[1].Current != "Disabled" || gpus[1].Pending != "Enabled" || len(gpus[1].Instances) != 0 {
		t.Errorf("GPU 1 should show a pending enable and no instances: %+v", gpus[1])
	}
}

// A failed MIG listing is an error, not GPUs without instances; a host
// whose listing has no MIG devices still reads cleanly.
func TestMIGStatusReportsFailedListing(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
case "$*" in
  *mig.mode.current*) printf '0, NVIDIA A100-SXM4-80GB, Disabled, Disabled\n' ;;
  -L) [ "$MIG_LIST_FAIL" = 1 ] && { echo 'fixture driver error' >&2; exit 9; }; printf 'GPU 0: NVIDIA A100-SXM4-80GB (UUID: GPU-aaa)\n' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "nvidia-smi"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	gpus, err := MIGStatus(context.Background(), newDryRunRC(t))
	if err != nil || len(gpus) != 1 || len(gpus[0].Instances) != 0 {
		t.Fatalf("MIG disabled: gpus=%+v err=%v, want one GPU, no instances, no error", gpus, err)
	}
	t.Setenv("MIG_LIST_FAIL", "1")
	if gpus, err = MIGStatus(context.Background(), newDryRunRC(t)); err == nil || !strings.Contains(err.Error(), "nvidia-smi -L") {
		t.Fatalf("failed listing: gpus=%+v err=%v, want an nvidia-smi -L error", gpus, err)
	}
}
