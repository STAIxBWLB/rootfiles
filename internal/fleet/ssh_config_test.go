package fleet

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestImportSSHConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(dir, "conf.d"), 0700); err != nil {
		t.Fatal(err)
	}
	config := "Include conf.d/*.conf\nHost alpha *.example !no bad_name\nMatch host ignored\n  User nobody\nHost beta alpha\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conf.d", "hosts.conf"), []byte("Host included\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ImportSSHConfig(filepath.Join(dir, "config"), "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"included", "alpha", "beta"}; !reflect.DeepEqual(got.Hosts, want) {
		t.Fatalf("hosts = %v, want %v", got.Hosts, want)
	}
	if len(got.Skipped) != 3 || !strings.Contains(strings.Join(got.Skipped, " "), "bad_name") {
		t.Fatalf("skipped = %v", got.Skipped)
	}
	filtered, err := ImportSSHConfig(filepath.Join(dir, "config"), "a*")
	if err != nil || !reflect.DeepEqual(filtered.Hosts, []string{"alpha"}) {
		t.Fatalf("filtered = %+v, %v", filtered, err)
	}
}

func TestImportSSHConfigIncludeCycleAndInvalidPattern(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "config")
	if err := os.WriteFile(name, []byte("Include config\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSSHConfig(name, ""); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle: %v", err)
	}
	if _, err := ImportSSHConfig(name, "["); err == nil || !strings.Contains(err.Error(), "--match") {
		t.Fatalf("pattern: %v", err)
	}
}
