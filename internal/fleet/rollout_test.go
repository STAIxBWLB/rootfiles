package fleet

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScheduleDefaultRemoteCommandCompatibility(t *testing.T) {
	for _, action := range []string{"enable", "disable"} {
		for _, sudo := range []string{"none", "nopasswd"} {
			t.Run(action+"/"+sudo, func(t *testing.T) {
				remoteFile := filepath.Join(t.TempDir(), "remote")
				t.Setenv("ROOTFILES_FLEET_SCHEDULE_WIRE", remoteFile)
				bin := writeFakeSSH(t, "#!/bin/sh\nprintf '%s' \"$7\" > \"$ROOTFILES_FLEET_SCHEDULE_WIRE\"\necho scheduled\n")
				hosts := []NamedHost{{Name: "fixture", Host: Host{SSH: "fixture"}, Sudo: sudo}}
				results, err := Schedule(context.Background(), SSHRunner{Binary: bin}, hosts, action, "", false)
				if err != nil || len(results) != 1 || results[0].State != "ok" {
					t.Fatalf("schedule results=%+v error=%v", results, err)
				}
				want := "/usr/local/bin/rootfiles schedule " + action
				if sudo == "nopasswd" {
					want = "sudo -n " + want
				}
				got, err := os.ReadFile(remoteFile)
				if err != nil || string(got) != want {
					t.Fatalf("remote command=%q error=%v, want exact %q", got, err, want)
				}
			})
		}
	}
}

func TestScheduleCalendarIsValidatedBeforeSSHAndQuoted(t *testing.T) {
	remoteFile := filepath.Join(t.TempDir(), "remote")
	t.Setenv("ROOTFILES_FLEET_SCHEDULE_WIRE", remoteFile)
	bin := writeFakeSSH(t, "#!/bin/sh\nprintf '%s' \"$7\" > \"$ROOTFILES_FLEET_SCHEDULE_WIRE\"\necho scheduled\n")
	hosts := []NamedHost{{Name: "fixture", Host: Host{SSH: "fixture"}, Sudo: "none"}}
	for _, expr := range []string{" ", "daily\nOnBootSec=1m", "daily\rOnBootSec=1m", "daily\x00", "it's"} {
		if _, err := Schedule(context.Background(), SSHRunner{Binary: bin}, hosts, "enable", expr, false); err == nil {
			t.Errorf("invalid calendar %q accepted", expr)
		}
		if _, err := os.Stat(remoteFile); !os.IsNotExist(err) {
			t.Fatalf("invalid calendar reached SSH: %v", err)
		}
	}
	expr := "Mon..Fri *-*-* 06:00:00"
	if _, err := Schedule(context.Background(), SSHRunner{Binary: bin}, hosts, "enable", expr, false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(remoteFile)
	want := "/usr/local/bin/rootfiles schedule enable --on-calendar 'Mon..Fri *-*-* 06:00:00'"
	if err != nil || string(got) != want {
		t.Fatalf("calendar command=%q error=%v, want %q", got, err, want)
	}
}
