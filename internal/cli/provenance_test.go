package cli

import (
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/state"
)

func TestAppliedFingerprintRequiresSuccessfulMatchingRecordedIntent(t *testing.T) {
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	cfg := &config.Config{Timezone: "UTC"}
	fingerprint, err := cfg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		run  state.Run
		want string
	}{
		{"legacy", state.Run{Profile: "minimal", Success: true}, ""},
		{"failed", state.Run{Profile: "minimal", Success: false, ConfigSHA256: fingerprint}, ""},
		{"other-target", state.Run{Profile: "full", Success: true, ConfigSHA256: fingerprint}, ""},
		{"changed-config", state.Run{Profile: "minimal", Success: true, ConfigSHA256: "old"}, ""},
		{"matching-success", state.Run{Profile: "minimal", Success: true, ConfigSHA256: fingerprint}, fingerprint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := state.Record(tt.run); err != nil {
				t.Fatal(err)
			}
			got, err := appliedFingerprint(cfg, "minimal", "")
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
