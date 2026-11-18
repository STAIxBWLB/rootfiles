package cli

import (
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/state"
)

// recordProfileApply records a successful dgx apply with a --home-base
// override, keeping its resolved config copy as apply would, and returns the
// recorded fingerprint.
func recordProfileApply(t *testing.T, homeBase string) string {
	t.Helper()
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	t.Setenv("ROOTFILES_HOME_BASE", "") // keep the env override off the copy and its loads
	cfg, err := config.LoadWithHomeBase("dgx", "", &config.SystemInfo{OS: "ubuntu"}, homeBase)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := cfg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Record(state.Run{Profile: "dgx", ConfigSHA256: fingerprint, Success: true}); err != nil {
		t.Fatal(err)
	}
	if err := saveAppliedConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

// #45 AC1, AC2: reports prefer the kept applied copy when it is the recorded
// run's config and root alone controls it; otherwise they select their target
// as before, and name the recorded target when the copy is in use.
func TestResolveTarget_PrefersAppliedCopy(t *testing.T) {
	fingerprint := recordProfileApply(t, "/srv/h")
	sys := &config.SystemInfo{}
	sub, _, err := NewRootCmd("test", "abc").Find([]string{"check"})
	if err != nil {
		t.Fatal(err)
	}
	snap := state.AppliedConfigPath()

	// The copy wins over re-resolving the recorded profile.
	p, c, applied := resolveTarget(sub, sys)
	if p != "" || c != snap || applied == nil {
		t.Errorf("kept copy: got %q %q %v, want the copy", p, c, applied)
	}
	// Reports name the recorded profile and path, never the copy path.
	if p, c := reportedTarget("", snap, applied); p != "dgx" || c != "" {
		t.Errorf("reported target: got %q %q, want the recorded profile dgx", p, c)
	}

	// An explicit --profile still wins over a usable copy.
	explicit, _, err := NewRootCmd("test", "abc").Find([]string{"check"})
	if err != nil {
		t.Fatal(err)
	}
	if err := explicit.ParseFlags([]string{"--profile", "base"}); err != nil {
		t.Fatal(err)
	}
	if p, c, applied := resolveTarget(explicit, sys); p != "base" || c != "" || applied != nil {
		t.Errorf("explicit --profile: got %q %q %v, want base", p, c, applied)
	}

	// A copy left by another run is not reused; the recorded profile is, as
	// before the copy existed.
	if err := state.Record(state.Run{Profile: "dgx", ConfigSHA256: "another run", Success: true}); err != nil {
		t.Fatal(err)
	}
	if p, c, applied := resolveTarget(sub, sys); p != "dgx" || c != "" || applied != nil {
		t.Errorf("other run's copy: got %q %q %v, want the recorded profile", p, c, applied)
	}

	// A recorded config apply also reuses its copy, and reports its recorded
	// path; without a copy the recorded path is used as before.
	site := filepath.Join(t.TempDir(), "site.yaml")
	writeFile(t, site, "extends: minimal\n")
	if err := state.Record(state.Run{ConfigPath: site, ConfigSHA256: fingerprint, Success: true}); err != nil {
		t.Fatal(err)
	}
	p, c, applied = resolveTarget(sub, sys)
	if p != "" || c != snap || applied == nil {
		t.Errorf("config apply with a kept copy: got %q %q %v, want the copy", p, c, applied)
	}
	if p, c := reportedTarget("", snap, applied); p != "" || c != site {
		t.Errorf("reported target: got %q %q, want the recorded path", p, c)
	}
	if err := state.SaveAppliedConfig(nil); err != nil {
		t.Fatal(err)
	}
	if p, c, applied := resolveTarget(sub, sys); p != "" || c != site || applied != nil {
		t.Errorf("no kept copy: got %q %q %v, want the recorded path", p, c, applied)
	}

	// Without any record the detection suggestion is used.
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	if p, c, applied := resolveTarget(sub, sys); p != sys.SuggestProfile() || c != "" || applied != nil {
		t.Errorf("no record: got %q %q %v, want the suggestion", p, c, applied)
	}
}

// Reports label the kept copy with the recorded run it was validated
// against, even if a later apply has recorded another run since
// resolveTarget read the state.
func TestReportedTarget_UsesResolvedSnapshot(t *testing.T) {
	recordProfileApply(t, "/srv/h")
	sys := &config.SystemInfo{}
	sub, _, err := NewRootCmd("test", "abc").Find([]string{"check"})
	if err != nil {
		t.Fatal(err)
	}
	_, snap, applied := resolveTarget(sub, sys)
	if applied == nil {
		t.Fatal("resolveTarget did not return the recorded run")
	}
	// A concurrent apply records another run before the report names its
	// target; the report still belongs to the run the copy matched.
	if err := state.Record(state.Run{Profile: "full", ConfigSHA256: "newer run", Success: true}); err != nil {
		t.Fatal(err)
	}
	if p, c := reportedTarget("", snap, applied); p != "dgx" || c != "" {
		t.Errorf("reported target: got %q %q, want the run the copy was validated against (dgx)", p, c)
	}
	if p, c := reportedTarget("base", "", nil); p != "base" || c != "" {
		t.Errorf("reported target without a copy: got %q %q, want the selected target", p, c)
	}
}
