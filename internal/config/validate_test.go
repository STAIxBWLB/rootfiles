package config

import (
	"strings"
	"testing"
)

func TestValidatePrivateNetworkRequiresAddress(t *testing.T) {
	validate := func(t *testing.T, pn PrivateNetworkConfig) error {
		t.Helper()
		cfg, err := resolveProfile("minimal", 0)
		if err != nil {
			t.Fatalf("resolveProfile: %v", err)
		}
		cfg.Modules.Cloudflared.PrivateNetwork = pn
		return cfg.Validate()
	}

	err := validate(t, PrivateNetworkConfig{Enabled: true, Interface: "vlan0"})
	if err == nil || !strings.Contains(err.Error(), "modules.cloudflared.private_network.address") {
		t.Fatalf("enabled without an address: want an error naming modules.cloudflared.private_network.address, got %v", err)
	}

	if err := validate(t, PrivateNetworkConfig{Enabled: true, Interface: "vlan0", Address: "198.51.100.10/32"}); err != nil {
		t.Errorf("enabled with a valid CIDR: %v", err)
	}

	if err := validate(t, PrivateNetworkConfig{}); err != nil {
		t.Errorf("disabled: %v", err)
	}

	err = validate(t, PrivateNetworkConfig{Enabled: true, Address: "198.51.100.10"})
	if err == nil || !strings.Contains(err.Error(), "CIDR") {
		t.Errorf("enabled with a bare address: want a CIDR error, got %v", err)
	}
}
