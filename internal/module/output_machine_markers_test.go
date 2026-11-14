package module

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestMachineDescriptionsKeepRelationArrows(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		rc := newDryRunRC(t)
		dir := t.TempDir()
		link, target := filepath.Join(dir, "link"), filepath.Join(dir, "target")
		rc.Config.Modules.Storage = config.StorageConfig{Symlinks: map[string]string{link: target}}
		result := relationJSONLocales(t, func() (*CheckResult, error) { return NewStorageModule().Check(context.Background(), rc) })
		want := "Create symlink " + link + " → " + target
		if len(result.Changes) != 1 || result.Changes[0].Description != want {
			t.Fatalf("machine description changed: %+v", result)
		}
		payload, err := json.Marshal(result)
		if err != nil || !strings.Contains(string(payload), want) {
			t.Fatalf("JSON changed: %s err=%v", payload, err)
		}
	})
	t.Run("download", func(t *testing.T) {
		fakeCloudflaredPaths(t)
		if err := os.Remove(cloudflaredBinary); err != nil {
			t.Fatal(err)
		}
		rc := newDryRunRC(t)
		rc.Config.Modules.Cloudflared = config.CloudflaredConfig{Enabled: true}
		result := relationJSONLocales(t, func() (*CheckResult, error) { return NewCloudflaredModule().Check(context.Background(), rc) })
		want := "download cloudflared → " + cloudflaredBinary
		if len(result.Changes) != 1 || result.Changes[0].Command != want {
			t.Fatalf("machine command changed: %+v", result)
		}
		payload, err := json.Marshal(result)
		if err != nil || !strings.Contains(string(payload), want) {
			t.Fatalf("JSON changed: %s err=%v", payload, err)
		}
	})
}

// relationJSONLocales checks the actual Check result before JSON encoding, so a
// locale-dependent relation separator cannot hide behind an unchanged schema.
func relationJSONLocales(t *testing.T, check func() (*CheckResult, error)) *CheckResult {
	t.Helper()
	var baseline []byte
	var result *CheckResult
	for _, locale := range []string{"C.UTF-8", "C"} {
		t.Setenv("LC_ALL", "")
		t.Setenv("LC_CTYPE", "")
		t.Setenv("LANG", locale)
		var err error
		result, err = check()
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if baseline == nil {
			baseline = payload
		} else if !bytes.Equal(baseline, payload) {
			t.Fatalf("LANG=C changed JSON bytes:\nUTF-8: %s\nC: %s", baseline, payload)
		}
	}
	return result
}
