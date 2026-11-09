package module

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDryRunScenarioFailureDiagnostics(t *testing.T) {
	for _, failMinimal := range []bool{false, true} {
		name := "success"
		if failMinimal {
			name = "failure diagnostics"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			// Shadow every command that could inspect the host; only shell utility
			// commands operate for real, on this test's private temporary files.
			scripts := map[string]string{
				"md5sum":    "#!/bin/sh\necho unchanged\n",
				"rootfiles": "#!/bin/sh\nif [ \"$ROOTFILES_FIXTURE_FAIL\" = yes ] && [ \"$3\" = minimal ]; then echo 'sentinel minimal preview failure'; exit 42; fi\necho 'locale packages ssh security'\n",
			}
			for command, script := range scripts {
				if err := os.WriteFile(filepath.Join(dir, command), []byte(script), 0755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if failMinimal {
				t.Setenv("ROOTFILES_FIXTURE_FAIL", "yes")
			} else {
				t.Setenv("ROOTFILES_FIXTURE_FAIL", "no")
			}
			assert, err := filepath.Abs("../../tests/integration/assert.sh")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("ROOTFILES_TEST_ASSERT", assert)
			command := osexec.Command("bash", "../../tests/scenarios/dry-run-all-profiles.sh")
			output, err := command.CombinedOutput()
			if failMinimal {
				if err == nil || !strings.Contains(string(output), "sentinel minimal preview failure") || !strings.Contains(string(output), "dry-run minimal: exit 42") {
					t.Fatalf("scenario hid preview failure: err=%v output=%s", err, output)
				}
			} else if err != nil || !strings.Contains(string(output), "dry-run full: exit 0") {
				t.Fatalf("dry-run scenario harness failed: err=%v output=%s", err, output)
			}
		})
	}
}
