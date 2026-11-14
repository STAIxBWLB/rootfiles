package module

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func TestMetadataWarningUsesConfiguredErrorWriter(t *testing.T) {
	rc := newDryRunRC(t)
	var stdout, stderr, fallback bytes.Buffer
	rc.Out = &stdout
	rc.Err = ui.ConfigureWriter(&stderr, "never", true, false)
	old := warnOut
	warnOut = &fallback
	t.Cleanup(func() { warnOut = old })
	if err := os.Chmod(filepath.Join(rc.Config.Users.HomeBase, ".rootfiles"), 0777); err != nil {
		t.Fatal(err)
	}
	WarnUntrustedMetadata(rc)
	if !strings.HasPrefix(stderr.String(), "! ") || !strings.Contains(stderr.String(), "untrusted rootfiles metadata") || strings.ContainsRune(stderr.String(), 27) || strings.Contains(stderr.String(), ui.MarkWarn) {
		t.Fatalf("configured warning policy missing: %q", stderr.String())
	}
	if stdout.Len() != 0 || fallback.Len() != 0 {
		t.Fatalf("warning bypassed explicit stream: stdout=%q fallback=%q", stdout.String(), fallback.String())
	}
}
