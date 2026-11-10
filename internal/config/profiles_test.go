package config

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var updateProfileGoldens = flag.Bool("update-profile-goldens", false, "rewrite testdata/profiles from the embedded profiles")

// TestProfileEffectiveGolden pins the fully resolved configuration of every
// embedded profile. Removing a key a parent already sets, or a block that
// never takes effect, must leave these files untouched; any other difference
// is a behaviour change for hosts applying that profile.
func TestProfileEffectiveGolden(t *testing.T) {
	for _, name := range AvailableProfiles() {
		t.Run(name, func(t *testing.T) {
			cfg, err := resolveProfile(name, 0)
			if err != nil {
				t.Fatalf("resolveProfile(%q): %v", name, err)
			}
			got, err := cfg.Effective()
			if err != nil {
				t.Fatalf("effective config: %v", err)
			}
			path := filepath.Join("testdata", "profiles", name+".yaml")
			if *updateProfileGoldens {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v (regenerate with -update-profile-goldens)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("effective config changed:\n--- want\n%s\n--- got\n%s", want, got)
			}
		})
	}
}

// TestEmbeddedProfilesHaveNoAddresses keeps site-specific addresses out of the
// shipped profiles: they belong in a site config that extends a profile.
func TestEmbeddedProfilesHaveNoAddresses(t *testing.T) {
	ipv4 := regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+`)
	entries, err := embeddedProfiles.ReadDir("profiles")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		path := "profiles/" + entry.Name()
		data, err := embeddedProfiles.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if match := ipv4.Find(data); match != nil {
			t.Errorf("%s contains an IP address (%s); put site-specific values in a site config", path, match)
		}
	}
}
