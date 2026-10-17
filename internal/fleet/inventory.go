package fleet

import (
	"bytes"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"gopkg.in/yaml.v3"
)

type Inventory struct {
	Defaults Defaults        `yaml:"defaults" json:"defaults"`
	Hosts    map[string]Host `yaml:"hosts" json:"hosts"`
	Order    []string        `yaml:"-" json:"-"`
	Path     string          `yaml:"-" json:"-"`
}

type Defaults struct {
	Sudo     string `yaml:"sudo" json:"sudo"`
	Parallel int    `yaml:"parallel" json:"parallel"`
}

type Host struct {
	SSH               string   `yaml:"ssh" json:"ssh"`
	Address           string   `yaml:"address,omitempty" json:"address,omitempty"`
	Groups            []string `yaml:"groups,omitempty" json:"groups,omitempty"`
	Config            string   `yaml:"config,omitempty" json:"config,omitempty"`
	Sudo              string   `yaml:"sudo,omitempty" json:"sudo,omitempty"`
	ConfigFingerprint string   `yaml:"-" json:"-"`
	// EffectiveConfig is resolved locally, including file-based extends, before
	// any bytes are sent to a host.
	EffectiveConfig []byte `yaml:"-" json:"-"`
}

var inventoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var sshToken = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var dnsLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func LoadInventory(path string) (*Inventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading inventory %q: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var inv Inventory
	if err := dec.Decode(&inv); err != nil {
		return nil, fmt.Errorf("parsing inventory: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("parsing inventory: expected one YAML document")
	}
	if inv.Defaults.Sudo == "" {
		inv.Defaults.Sudo = "nopasswd"
	}
	if inv.Defaults.Parallel == 0 {
		inv.Defaults.Parallel = 2
	}
	if inv.Defaults.Parallel < 1 || inv.Defaults.Parallel > 128 {
		return nil, fmt.Errorf("defaults.parallel must be between 1 and 128")
	}
	if err := validateSudo(inv.Defaults.Sudo); err != nil {
		return nil, fmt.Errorf("defaults.sudo: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	inv.Path = abs
	if len(inv.Hosts) == 0 {
		return nil, fmt.Errorf("inventory has no hosts")
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err == nil && len(root.Content) > 0 {
		for i := 0; i+1 < len(root.Content[0].Content); i += 2 {
			if root.Content[0].Content[i].Value == "hosts" {
				hn := root.Content[0].Content[i+1]
				for j := 0; j+1 < len(hn.Content); j += 2 {
					inv.Order = append(inv.Order, hn.Content[j].Value)
				}
			}
		}
	}
	for name, h := range inv.Hosts {
		if !inventoryName.MatchString(name) {
			return nil, fmt.Errorf("host %q is not a safe inventory identifier", name)
		}
		if !validSSHHost(h.SSH) {
			return nil, fmt.Errorf("host %q: ssh must be a safe destination without options or whitespace", name)
		}
		if h.Address != "" && !validAddress(h.Address) {
			return nil, fmt.Errorf("host %q: address must be an IP address or hostname", name)
		}
		if h.Sudo != "" {
			if err := validateSudo(h.Sudo); err != nil {
				return nil, fmt.Errorf("host %q: %w", name, err)
			}
		}
		for _, g := range h.Groups {
			if !inventoryName.MatchString(g) {
				return nil, fmt.Errorf("host %q: group %q is not a safe identifier", name, g)
			}
		}
		if h.Config != "" {
			p := h.Config
			if !filepath.IsAbs(p) {
				p = filepath.Join(filepath.Dir(abs), p)
			}
			cfg, err := config.LoadSite(p)
			if err != nil {
				return nil, fmt.Errorf("host %q config: %w", name, err)
			}
			if cfg.Modules.Cloudflared.TunnelToken != "" {
				return nil, fmt.Errorf("host %q config resolves an inline tunnel_token; use tunnel_token_file", name)
			}
			h.ConfigFingerprint, err = cfg.Fingerprint()
			if err != nil {
				return nil, fmt.Errorf("host %q config: fingerprinting effective config: %w", name, err)
			}
			resolved, err := yaml.Marshal(cfg)
			if err != nil {
				return nil, fmt.Errorf("host %q config: encoding effective config: %w", name, err)
			}
			h.EffectiveConfig = resolved
			inv.Hosts[name] = h
		}
	}
	return &inv, nil
}

func validSSHHost(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, " \t\r\n\x00;|&$`(){}<>\\\"'") {
		return false
	}
	parts := strings.Split(value, "@")
	if len(parts) > 2 {
		return false
	}
	host := parts[len(parts)-1]
	if len(parts) == 2 && !sshToken.MatchString(parts[0]) {
		return false
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		addr, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
		return err == nil && addr.Is6() && addr.Zone() == ""
	}
	if strings.Contains(host, ":") {
		addr, err := netip.ParseAddr(host)
		return err == nil && addr.Is6() && addr.Zone() == ""
	}
	return validAddress(host)
}

func validAddress(value string) bool {
	if strings.ContainsAny(value, " \t\r\n\x00/\\;|&$`(){}<>\"'[]@?#") {
		return false
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		return addr.Zone() == ""
	}
	name := strings.TrimSuffix(value, ".")
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !dnsLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func validateSudo(v string) error {
	switch v {
	case "nopasswd", "root", "none":
		return nil
	default:
		return fmt.Errorf("sudo must be nopasswd, root, or none")
	}
}

// Select returns hosts in stable inventory order. Host and group selectors form
// a union; selecting nothing is only accepted with all=true.
func (i *Inventory) Select(hosts, groups []string, all bool) ([]NamedHost, error) {
	if all && (len(hosts) > 0 || len(groups) > 0) {
		return nil, fmt.Errorf("--all cannot be combined with --host or --group")
	}
	if !all && len(hosts) == 0 && len(groups) == 0 {
		return nil, fmt.Errorf("select hosts with --host, --group, or --all")
	}
	wanted := map[string]bool{}
	for _, n := range hosts {
		for _, part := range strings.Split(n, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, ok := i.Hosts[part]; !ok {
				return nil, fmt.Errorf("unknown host %q", part)
			}
			wanted[part] = true
		}
	}
	groupSet := map[string]bool{}
	for _, g := range groups {
		for _, part := range strings.Split(g, ",") {
			if strings.TrimSpace(part) != "" {
				groupSet[strings.TrimSpace(part)] = true
			}
		}
	}
	names := append([]string(nil), i.Order...)
	if len(names) != len(i.Hosts) {
		names = names[:0]
		for n := range i.Hosts {
			names = append(names, n)
		}
		sort.Strings(names)
	}
	out := make([]NamedHost, 0)
	for _, n := range names {
		h := i.Hosts[n]
		match := all || wanted[n]
		for _, g := range h.Groups {
			if groupSet[g] {
				match = true
			}
		}
		if match {
			out = append(out, NamedHost{Name: n, Host: h, Sudo: effectiveSudo(i.Defaults.Sudo, h.Sudo)})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("selection matched no hosts")
	}
	return out, nil
}

func effectiveSudo(def, host string) string {
	if host != "" {
		return host
	}
	return def
}

type NamedHost struct {
	Name string `json:"name"`
	Host Host   `json:"-"`
	Sudo string `json:"-"`
}
