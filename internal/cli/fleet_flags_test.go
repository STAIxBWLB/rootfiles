package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/fleet"
)

// fleetFlagFixture records any attempt to open SSH without contacting a host.
func fleetFlagFixture(t *testing.T) (inventory, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "ssh-called")
	ssh := "#!/bin/sh\nprintf called >> " + fleet.ShellQuote(calls) + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(ssh), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	inventory = filepath.Join(dir, "fleet.yaml")
	if err := os.WriteFile(inventory, []byte("hosts:\n  host1:\n    ssh: host1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return inventory, calls
}

func TestFleetRejectsIgnoredRootFlagsBeforeSSH(t *testing.T) {
	flags := [][]string{
		{"--module", "ssh"}, {"--profile", "minimal"}, {"--config", "site.yaml"},
		{"--force=false"}, {"--tunnel-token", "test-token"}, {"--home-base", "/srv/home"},
		{"--user", "alice"}, {"--ssh-pubkey", "test-key"}, {"--vlan-address", "192.0.2.1/32"},
	}
	for _, op := range []string{"apply", "update", "bootstrap", "schedule", "status", "check", "doctor", "targets"} {
		for _, flag := range flags {
			t.Run(op+"/"+flag[0], func(t *testing.T) {
				inventory, calls := fleetFlagFixture(t)
				args := []string{"fleet", op}
				if op == "schedule" {
					args = append(args, "enable")
				}
				args = append(args, "--inventory", inventory, "--all")
				if op == "apply" || op == "update" || op == "bootstrap" || op == "schedule" {
					args = append(args, "--yes")
				}
				args = append(args, flag...)
				cmd := NewRootCmd("test", "test")
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs(args)
				err := cmd.Execute()
				name := strings.SplitN(flag[0], "=", 2)[0]
				if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "inventory") {
					t.Fatalf("ignored flag %s: %v", name, err)
				}
				if _, err := os.Stat(calls); !os.IsNotExist(err) {
					t.Fatalf("ignored flag %s opened SSH: %v", name, err)
				}
			})
		}
	}
}

func TestFleetRejectsInvalidOutputBeforeSSH(t *testing.T) {
	for _, op := range []string{"status", "check", "doctor", "apply", "update", "bootstrap", "schedule"} {
		t.Run(op, func(t *testing.T) {
			inventory, calls := fleetFlagFixture(t)
			args := []string{"fleet", op}
			if op == "schedule" {
				args = append(args, "enable")
			}
			args = append(args, "--inventory", inventory, "--all", "-o", "yaml")
			if op == "apply" || op == "update" || op == "bootstrap" || op == "schedule" {
				args = append(args, "--yes")
			}
			cmd := NewRootCmd("test", "test")
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--output") {
				t.Fatalf("invalid format was not refused: %v", err)
			}
			if _, err := os.Stat(calls); !os.IsNotExist(err) {
				t.Fatalf("invalid format opened SSH: %v", err)
			}
		})
	}
}

func TestFleetStillRequiresExplicitYes(t *testing.T) {
	inventory, calls := fleetFlagFixture(t)
	t.Setenv("ROOTFILES_YES", "true")
	cmd := NewRootCmd("test", "test")
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"fleet", "update", "--inventory", inventory, "--all", "--version", "v1.2.3"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("fleet accepted ROOTFILES_YES without --yes: %v", err)
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("unauthorized rollout opened SSH: %v", err)
	}
}

func TestFleetMutationPreviewStillRequiresSelection(t *testing.T) {
	for _, operation := range []string{"apply", "update", "bootstrap", "schedule"} {
		for _, tc := range []struct {
			name, want string
			flags      []string
		}{
			{"preview all", "", []string{"--all", "--dry-run"}},
			{"preview host", "", []string{"--host", "host1", "--dry-run"}},
			{"preview no selection", "--host", []string{"--dry-run"}},
			{"preview empty host", "--host", []string{"--host=", "--dry-run"}},
			{"real no yes", "--yes", []string{"--all"}},
			{"real explicit false dry run", "--yes", []string{"--all", "--dry-run=false"}},
			{"real yes no selection", "--host", []string{"--yes"}},
			{"real authorized", "", []string{"--yes", "--all"}},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				inventory, calls := fleetFlagFixture(t)
				root := NewRootCmd("test", "test")
				cmd, _, err := root.Find([]string{"fleet", operation})
				if err != nil {
					t.Fatal(err)
				}
				if err := cmd.ParseFlags(append([]string{"--inventory", inventory}, tc.flags...)); err != nil {
					t.Fatal(err)
				}
				_, selected, _, err := authorizeFleetMutation(cmd)
				if tc.want == "" {
					if err != nil || len(selected) != 1 {
						t.Fatalf("authorized selection=%v error=%v", selected, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("authorization error=%v, want %q", err, tc.want)
				}
				if _, err := os.Stat(calls); !os.IsNotExist(err) {
					t.Fatalf("authorization opened SSH: %v", err)
				}
			})
		}
	}
}

func TestFleetScheduleRejectsExplicitInvalidCalendarBeforeSSH(t *testing.T) {
	for _, tc := range []struct{ action, calendar string }{
		{"enable", ""}, {"enable", " "}, {"enable", "daily\rOnBootSec=1m"},
		{"enable", "daily\x00"}, {"disable", ""}, {"disable", "hourly"},
	} {
		t.Run(tc.action+"/"+tc.calendar, func(t *testing.T) {
			inventory, calls := fleetFlagFixture(t)
			root := NewRootCmd("test", "test")
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs([]string{"fleet", "schedule", tc.action, "--inventory", inventory, "--all", "--yes", "--on-calendar", tc.calendar})
			if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--on-calendar") {
				t.Fatalf("calendar %q error=%v", tc.calendar, err)
			}
			if _, err := os.Stat(calls); !os.IsNotExist(err) {
				t.Fatalf("invalid calendar opened SSH: %v", err)
			}
		})
	}
}
