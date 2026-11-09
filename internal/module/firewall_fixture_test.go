package module

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ensureFixtureUFW makes generic command fixtures independent of the host's
// UFW installation. A UFW already under TestMain's private TMPDIR belongs to
// the current test and must retain its explicit active, inactive or failed state.
func ensureFixtureUFW(t *testing.T, dir string) {
	t.Helper()
	if executable, err := exec.LookPath("ufw"); err == nil {
		privateRoot, rootErr := filepath.EvalSymlinks(os.TempDir())
		executable, executableErr := filepath.EvalSymlinks(executable)
		if rootErr == nil && executableErr == nil {
			relative, err := filepath.Rel(privateRoot, executable)
			if err == nil && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "ufw"), []byte("#!/bin/sh\nprintf 'Status: inactive\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
}

func genericFirewallCommandFixtures() map[string]func(*testing.T) {
	return map[string]func(*testing.T){
		"command output":     func(t *testing.T) { fakeCommandOutput(t, map[string]string{"ss": ""}) },
		"monitoring command": func(t *testing.T) { fakeMonitoringCommand(t, "ss", "#!/bin/sh\nexit 0\n") },
		"generic binaries":   func(t *testing.T) { fakeBin(t, "systemctl") },
	}
}

func TestGenericCommandFixturesDoNotReachAmbientUFW(t *testing.T) {
	for name, install := range genericFirewallCommandFixtures() {
		t.Run(name, func(t *testing.T) {
			// Outside TestMain's private root, like a CI runner's /usr/sbin/ufw.
			ambient, err := os.MkdirTemp(filepath.Dir(os.TempDir()), "rootfiles-ambient-ufw-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(ambient) })
			marker := filepath.Join(ambient, "called")
			quotedMarker := "'" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
			if err := os.WriteFile(filepath.Join(ambient, "ufw"), []byte("#!/bin/sh\nprintf called > "+quotedMarker+"\nexit 1\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", ambient)
			install(t)
			state, err := inspectUFW(context.Background(), newDryRunRC(t), true)
			if err != nil || !state.Installed || !state.Known || state.Active {
				t.Fatalf("generic fixture did not supply known inactive UFW: %+v, %v", state, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("generic fixture reached ambient UFW: %v", err)
			}
		})
	}
}

func TestGenericCommandFixturesPreservePrivateUFW(t *testing.T) {
	for name, install := range genericFirewallCommandFixtures() {
		for _, tc := range []struct {
			name, script string
			known        bool
		}{
			{"active", "#!/bin/sh\nprintf 'Status: active\\n'\n", true},
			{"unknown", "#!/bin/sh\nprintf 'Status: maybe\\n'\n", false},
			{"failed query", "#!/bin/sh\nexit 1\n", false},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				fakeMonitoringCommand(t, "ufw", tc.script)
				before, err := exec.LookPath("ufw")
				if err != nil {
					t.Fatal(err)
				}
				install(t)
				after, err := exec.LookPath("ufw")
				if err != nil || after != before {
					t.Fatalf("generic fixture replaced explicit UFW: before=%s after=%s err=%v", before, after, err)
				}
				state, err := inspectUFW(context.Background(), newDryRunRC(t), true)
				if tc.known {
					if err != nil || !state.Known || !state.Active {
						t.Fatalf("active fixture was masked: %+v, %v", state, err)
					}
				} else if err == nil || state.Known {
					t.Fatalf("unknown fixture was masked: %+v, %v", state, err)
				}
			})
		}
	}
}
