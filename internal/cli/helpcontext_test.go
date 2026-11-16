package cli

import (
	"strings"
	"testing"
)

// TestControllerHelpHidesNativeConfigInputs holds each page to the context its
// host runs: on Linux, status and doctor read the profile, config file and home
// base, so the page lists them; on a controller they read none of the three, so
// the page must not offer an input the report ignores.
func TestControllerHelpHidesNativeConfigInputs(t *testing.T) {
	savedOS := nativeHostOS
	t.Cleanup(func() { nativeHostOS = savedOS })
	for _, tc := range []struct {
		osName string
		hidden bool
	}{
		{"linux", false},
		{"darwin", true},
	} {
		t.Run(tc.osName, func(t *testing.T) {
			pinInventoryPage(t)
			nativeHostOS = tc.osName
			root := NewRootCmd("dev", "test")
			for _, path := range [][]string{{"status"}, {"doctor"}} {
				c, _, err := root.Find(path)
				if err != nil {
					t.Fatal(err)
				}
				page := renderHelp(c)
				for _, flag := range []string{"--profile", "--config", "--home-base"} {
					if listed := strings.Contains(page, flag); listed == tc.hidden {
						t.Errorf("%s: %s listed=%v, want listed=%v", c.CommandPath(), flag, listed, !tc.hidden)
					}
				}
			}
		})
	}
}

// TestControllerHelpListsTheInventory keeps the controller's own input on the
// page it belongs to: hiding the native inputs must not hide the one the report
// reads.
func TestControllerHelpListsTheInventory(t *testing.T) {
	savedOS := nativeHostOS
	t.Cleanup(func() { nativeHostOS = savedOS })
	for _, tc := range []struct {
		osName string
		listed bool
	}{
		{"linux", false},
		{"darwin", true},
	} {
		t.Run(tc.osName, func(t *testing.T) {
			pinInventoryPage(t)
			nativeHostOS = tc.osName
			root := NewRootCmd("dev", "test")
			for _, path := range [][]string{{"status"}, {"doctor"}} {
				c, _, err := root.Find(path)
				if err != nil {
					t.Fatal(err)
				}
				if listed := strings.Contains(renderHelp(c), "--inventory"); listed != tc.listed {
					t.Errorf("%s: --inventory listed=%v, want listed=%v", c.CommandPath(), listed, tc.listed)
				}
			}
		})
	}
}

// TestUpdateExampleLeadsWithTheHostsForm holds `update`'s example to the form
// the host can run: sudo on Linux, the operator's own binary on a controller,
// where upgrade.go refuses a root-owned install.
func TestUpdateExampleLeadsWithTheHostsForm(t *testing.T) {
	savedOS := nativeHostOS
	t.Cleanup(func() { nativeHostOS = savedOS })
	for _, tc := range []struct {
		osName string
		sudo   bool
	}{
		{"linux", true},
		{"darwin", false},
	} {
		nativeHostOS = tc.osName
		cmd := newUpgradeCmd("dev")
		first := strings.SplitN(strings.TrimSpace(cmd.Example), "\n", 2)[0]
		if got := strings.HasPrefix(strings.TrimSpace(first), "sudo "); got != tc.sudo {
			t.Errorf("%s: first example line %q (sudo=%v), want sudo=%v", tc.osName, first, got, tc.sudo)
		}
		if !strings.Contains(cmd.Example, "rootfiles update --check") {
			t.Errorf("%s: example does not name --check: %q", tc.osName, cmd.Example)
		}
	}
}
