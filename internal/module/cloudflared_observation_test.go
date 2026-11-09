package module

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectCloudflaredSeparatesPresenceRawAndParsedVersion(t *testing.T) {
	for _, tc := range []struct {
		name, script, raw, version string
		installed, queryFailed     bool
	}{
		{"missing", "", "", "", false, false},
		{"raw version", "printf '  cloudflared version 2026.2.0 (built fixture)\\n'\n", "  cloudflared version 2026.2.0 (built fixture)\n", "2026.2.0", true, false},
		{"unparseable", "printf 'development build\\n'\n", "development build\n", "", true, false},
		{"query failed", "printf 'cloudflared version 2026.2.0\\n'\nexit 1\n", "cloudflared version 2026.2.0\n", "", true, true},
		{"first dotted token", "printf 'tool 1.0 2.345 2026.2.0\\n'\n", "tool 1.0 2.345 2026.2.0\n", "2.345", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "cloudflared")
			if tc.installed {
				if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+tc.script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			rc := newDryRunRC(t)
			queries := 0
			guard := rc.Runner.QueryGuard
			rc.Runner.QueryGuard = func(executable string) error { queries++; return guard(executable) }
			got := InspectCloudflared(context.Background(), rc.Runner, binary)
			if got.Path != binary || got.Installed != tc.installed || got.RawOutput != tc.raw || got.Version != tc.version || (got.QueryError != nil) != tc.queryFailed {
				t.Fatalf("observation=%+v, want installed=%t raw=%q version=%q query failure=%t", got, tc.installed, tc.raw, tc.version, tc.queryFailed)
			}
			wantQueries := 0
			if tc.installed {
				wantQueries = 1
			}
			if queries != wantQueries {
				t.Errorf("queries=%d, want %d", queries, wantQueries)
			}
			previous := cloudflaredBinary
			cloudflaredBinary = binary
			t.Cleanup(func() { cloudflaredBinary = previous })
			if fallback := InspectCloudflared(context.Background(), rc.Runner, ""); fallback.Path != binary || fallback.Version != tc.version {
				t.Fatalf("default installed path/parse policy changed: %+v", fallback)
			}
			if current := currentCloudflaredVersion(context.Background(), rc); current != tc.version {
				t.Errorf("native update parsed version=%q, want %q", current, tc.version)
			}
		})
	}
}
