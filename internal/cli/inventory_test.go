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

// TestDocsCommandInventory keeps the committed page in step with the command tree.
// The page is generated, so a help edit that was not regenerated fails here
// rather than reaching a reader; `make cli-inventory` rewrites it.
func TestDocsCommandInventory(t *testing.T) {
	// Several flag defaults embed an environment value, so the walk pins every
	// one of them: otherwise this machine's state directory and home directory
	// would change the paths the page records, and the page would differ
	// between a developer's checkout and CI.
	//
	// HOME is pinned to a literal rather than cleared: the fleet inventory
	// default falls back to $HOME when XDG_CONFIG_HOME is empty, so an empty
	// value there still yields whatever home the walk runs under.
	for key, value := range map[string]string{
		"ROOTFILES_STATE_DIR": "", "ROOTFILES_LOG_FILE": "", "ROOTFILES_LOCK_FILE": "",
		"ROOTFILES_PROFILE": "", "ROOTFILES_USER": "", "ROOTFILES_SSH_PUBKEY": "",
		"ROOTFILES_TUNNEL_TOKEN": "", "ROOTFILES_YES": "",
		"XDG_STATE_HOME": "", "XDG_CONFIG_HOME": "",
		"HOME": InventoryHome,
	} {
		t.Setenv(key, value)
	}
	got := RenderInventory(Inventory(NewRootCmd("dev", "test")))
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
