package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inventoryPath is where the committed inventory lives: beside the hand-written
// command docs, because the two are read together.
var inventoryPath = filepath.Join("..", "..", "docs", "cli-inventory.md")

// pinInventoryPage pins everything the recorded help depends on, so the page
// and the style lint see the same tree on any machine.
//
// Several flag defaults embed an environment value, so every one is pinned:
// otherwise this machine's state directory and home directory would change the
// paths the page records. HOME is pinned to a literal rather than cleared,
// because the fleet inventory default falls back to $HOME when
// XDG_CONFIG_HOME is empty.
func pinInventoryPage(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"ROOTFILES_STATE_DIR": "", "ROOTFILES_LOG_FILE": "", "ROOTFILES_LOCK_FILE": "",
		"ROOTFILES_PROFILE": "", "ROOTFILES_USER": "", "ROOTFILES_SSH_PUBKEY": "",
		"ROOTFILES_TUNNEL_TOKEN": "", "ROOTFILES_YES": "",
		"XDG_STATE_HOME": "", "XDG_CONFIG_HOME": "",
		"HOME": InventoryHome,
	} {
		t.Setenv(key, value)
	}
	// Help differs by platform: a controller-only input exists only off Linux.
	// The page records the host this binary configures.
	savedOS := nativeHostOS
	nativeHostOS = InventoryPlatform
	t.Cleanup(func() { nativeHostOS = savedOS })
}

// TestDocsCommandInventory keeps the committed page in step with the command tree.
// The page is generated, so a help edit that was not regenerated fails here
// rather than reaching a reader; `make cli-inventory` rewrites it.
func TestDocsCommandInventory(t *testing.T) {
	pinInventoryPage(t)

	// The command walk runs before the capture: the capture points the state
	// directory at a fixture, and a flag default that embeds it would otherwise
	// record a temporary path in the page.
	facts := Inventory(NewRootCmd("dev", "test"))
	reports := append([]ReportFacts(nil), ReportInventory...)
	captured := captureReports(t)
	for i := range reports {
		if reports[i].State == ReportMeasured {
			reports[i].Issues = LayoutIssues(captured[reports[i].Command])
		}
	}
	got := RenderInventory(facts, reports)
	if os.Getenv("ROOTFILES_UPDATE") == "1" {
		if err := os.WriteFile(inventoryPath, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(inventoryPath)
	if err != nil {
		t.Fatalf("%s is missing (%v): run 'make cli-inventory'", inventoryPath, err)
	}
	if string(want) == got {
		return
	}
	t.Fatalf("%s no longer matches the command tree (run 'make cli-inventory'): %s",
		inventoryPath, firstDifference(string(want), got))
}

// firstDifference names the first line that moved, so a drift failure says what
// to look at instead of printing two whole pages.
func firstDifference(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		var have, g string
		if i < len(wantLines) {
			have = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if have != g {
			return fmt.Sprintf("line %d: committed %s, generated %s", i+1, clip(have), clip(g))
		}
	}
	return "the pages differ only in line endings"
}

func clip(s string) string {
	if len(s) > 160 {
		return `"` + s[:160] + `"...`
	}
	return `"` + s + `"`
}
