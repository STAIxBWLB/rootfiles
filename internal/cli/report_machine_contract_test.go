package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/module"
)

// Baselines use original d1ad011 check/doctor through a Go overlay with only
// a fixed clock and synthetic doctor findings; rendering is not stubbed.
// Only the temporary fixture root is normalized.
func TestReportMachineByteContract(t *testing.T) {
	for _, tc := range []struct {
		name, command, fixture, filter string
		strict                         bool
		exit                           int
	}{
		{name: "check-normal", command: "check", fixture: "populated", exit: 2},
		{name: "check-packages-filter", command: "check", fixture: "populated", filter: "packages", exit: 2},
		{name: "check-empty-filter", command: "check", fixture: "populated", filter: "locale", exit: 0},
		{name: "doctor-normal", command: "doctor", fixture: "populated", exit: 0},
		{name: "doctor-strict", command: "doctor", fixture: "populated", strict: true, exit: 2},
		{name: "doctor-config-fallback", command: "doctor", fixture: "config-fallback", exit: 2},
		{name: "doctor-unsupported-os", command: "doctor", fixture: "unsupported-os", exit: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixtureRoot, site, _ := statusContractFixture(t, tc.fixture)
			oldNow, oldChecks := checkNow, doctorChecks
			t.Cleanup(func() { checkNow, doctorChecks = oldNow, oldChecks })
			checkNow = func() time.Time { return time.Unix(1700000000, 0) }
			doctorChecks = func(context.Context, *module.RunContext) []module.Finding {
				return []module.Finding{
					{Check: "fixture <&> \"ok\"\nsecond", Level: module.LevelOK, Detail: "healthy <&> \"quoted\"\nnext line"},
					{Check: "fixture <&> \"warn\"\nsecond", Level: module.LevelWarn, Detail: "warning <&> \"quoted\"\nnext line", Hint: "inspect fixture <&> \"target\"\nthen retry"},
				}
			}
			for _, format := range []string{"json", "prometheus"} {
				root := NewRootCmd("report-contract", "fixture")
				var stdout, stderr bytes.Buffer
				root.SetOut(&stdout)
				root.SetErr(&stderr)
				args := []string{tc.command, "--config", site, "--output", format}
				if tc.filter != "" {
					args = append(args, "--module", tc.filter)
				}
				if tc.strict {
					args = append(args, "--strict")
				}
				root.SetArgs(args)
				err := root.Execute()
				exit := 0
				if err != nil {
					var coded *ExitError
					if !errors.As(err, &coded) {
						t.Fatalf("%s returned a non-report error: %v", format, err)
					}
					exit = coded.Code
				}
				if exit != tc.exit || stderr.Len() != 0 {
					t.Fatalf("%s exit=%d want=%d stderr=%s", format, exit, tc.exit, stderr.String())
				}
				if format == "json" {
					if !json.Valid(stdout.Bytes()) {
						t.Fatalf("machine output is not JSON: %s", stdout.String())
					}
					if tc.command == "check" {
						var report checkReport
						if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
							t.Fatal(err)
						}
						var names []string
						for _, entry := range report.Modules {
							names = append(names, entry.Name)
						}
						want := []string{"system", "packages"}
						if tc.filter == "packages" {
							want = []string{"packages"}
						}
						if tc.filter == "locale" {
							want = nil
							if report.Modules != nil || !strings.Contains(stdout.String(), "\"modules\": null") {
								t.Fatalf("empty check modules lost null encoding: %s", stdout.String())
							}
						}
						if !reflect.DeepEqual(names, want) || report.Satisfied != (tc.exit == 0) {
							t.Fatalf("check set/state changed: names=%v want=%v satisfied=%t", names, want, report.Satisfied)
						}
					} else if !strings.Contains(stdout.String(), "fixture <&>") {
						t.Fatalf("doctor JSON changed HTML escaping: %s", stdout.String())
					}
				} else if tc.command == "check" && !strings.Contains(stdout.String(), "rootfiles_check_timestamp_seconds 1700000000\n") {
					t.Fatalf("check metric did not use the fixture clock: %s", stdout.String())
				}
				// Do not decode/re-encode or sort the output. Escaping, field
				// ordering, metric ordering and trailing newline are contracts.
				got := strings.ReplaceAll(stdout.String(), fixtureRoot, "<fixture>")
				golden := filepath.Join("testdata", "reports-t3", tc.name+"."+format+".golden")
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("read baseline %s: %v; capture original d1ad011 check/doctor through the baseline overlay", golden, err)
				}
				if got != string(want) {
					t.Errorf("%s bytes changed\n--- baseline\n%s\n--- current\n%s", format, want, got)
				}
			}
		})
	}
}
