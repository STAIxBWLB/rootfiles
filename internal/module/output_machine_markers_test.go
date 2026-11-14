package module

import (
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
		result, err := NewStorageModule().Check(context.Background(), rc)
		if err != nil {
			t.Fatal(err)
		}
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
		result, err := NewCloudflaredModule().Check(context.Background(), rc)
		if err != nil {
			t.Fatal(err)
		}
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
