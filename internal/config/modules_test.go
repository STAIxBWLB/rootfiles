package config

import (
	"gopkg.in/yaml.v3"
	"reflect"
	"testing"
)

func TestModuleNamesAndToggles(t *testing.T) {
	want := []string{"locale", "system", "packages", "users", "ssh", "security", "docker", "nvidia", "gpu", "cloudflared", "storage", "network", "monitoring", "tenant"}
	if got := ModuleNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("module order = %v, want %v", got, want)
	}
	names := ModuleNames()
	names[0] = "mutated"
	if ModuleNames()[0] != "locale" {
		t.Fatal("ModuleNames exposes shared storage")
	}
	for _, enabled := range want {
		t.Run(enabled, func(t *testing.T) {
			body := "modules:\n  " + enabled + ":\n    enabled: true\n"
			if enabled == "gpu" {
				body = "modules:\n  nvidia:\n    gpu_allocation:\n      enabled: true\n"
			}
			var cfg Config
			if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
				t.Fatal(err)
			}
			for _, name := range want {
				if got := cfg.IsModuleEnabled(name); got != (name == enabled) {
					t.Errorf("enabled %s: %s = %v", enabled, name, got)
				}
			}
			if cfg.IsModuleEnabled("unknown") {
				t.Error("unknown module enabled")
			}
		})
	}
	for _, osName := range []string{"ubuntu", "dgx-os"} {
		got := SupportedModules(&SystemInfo{OS: osName})
		if len(got) != len(want) {
			t.Fatalf("%s supports %d modules, want %d", osName, len(got), len(want))
		}
		for _, name := range want {
			if !got[name] {
				t.Errorf("%s lacks %s", osName, name)
			}
		}
	}
	rocky := SupportedModules(&SystemInfo{OS: "rocky", Version: "9.5"})
	core := map[string]bool{"locale": true, "system": true, "packages": true, "users": true, "ssh": true, "security": true}
	if !reflect.DeepEqual(rocky, core) {
		t.Fatalf("Rocky subset changed: %v", rocky)
	}
}
