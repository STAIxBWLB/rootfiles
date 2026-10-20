// Package tenant manages long-lived tenant containers: one Docker container
// per tenant, a size-capped volume on a loop-mounted image, accounts and
// host keys kept outside the container, and GPU devices from the shared
// rootfiles allocation ledger.
package tenant

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

var (
	validName    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	validAccount = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)
	validPort    = regexp.MustCompile(`^([0-9]{1,5}):([0-9]{1,5})(/(tcp|udp))?$`)
)

// MinVolumeSize keeps mkfs well clear of its own minimums.
const MinVolumeSize = 64 << 20

// Paths inside the container that rootfiles owns.
const (
	SystemMount = "/etc/tenant-system"
	initScript  = "init.sh"
)

var reservedTargets = []string{"/data", "/home", "/tmp", SystemMount}

// Spec is one tenant, stored as <home_base>/.rootfiles/tenants/<name>.yaml.
type Spec struct {
	Name       string   `yaml:"name"`
	Image      string   `yaml:"image"`
	GPUs       []int    `yaml:"gpus,omitempty"`
	SSHPort    int      `yaml:"ssh_port"`
	Ports      []string `yaml:"ports,omitempty"`
	Volume     Volume   `yaml:"volume"`
	Users      []User   `yaml:"users"`
	Operators  []string `yaml:"operators,omitempty"`
	ExtraBinds []string `yaml:"extra_binds,omitempty"`
}

// Volume is the tenant's size-capped volume.
type Volume struct {
	Size string `yaml:"size"`
	FS   string `yaml:"fs,omitempty"` // ext4 (default) or xfs
}

// User is an account created inside the container on every start.
type User struct {
	Name string `yaml:"name"`
	UID  int    `yaml:"uid"`
	GID  int    `yaml:"gid"`
	Sudo bool   `yaml:"sudo,omitempty"`
}

// Filesystem returns the volume filesystem, ext4 when unset.
func (v Volume) Filesystem() string {
	if v.FS == "" {
		return "ext4"
	}
	return v.FS
}

// Bytes returns the parsed volume size.
func (v Volume) Bytes() (uint64, error) {
	n, err := config.ParseSize(v.Size)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ContainerName is the Docker container of tenant t.
func ContainerName(t string) string { return "tenant-" + t }

// CheckName validates a tenant name.
func CheckName(name string) error {
	if strings.HasSuffix(name, "-prev") {
		return fmt.Errorf("tenant name %q: the -prev suffix names rollback containers", name)
	}
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid tenant name %q (lowercase letter, then lowercase letters, digits or '-', at most 31)", name)
	}
	return nil
}

// Validate reports every problem in the spec at once.
func (s *Spec) Validate() error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if err := CheckName(s.Name); err != nil {
		add("%v", err)
	}
	if s.Image == "" || strings.ContainsAny(s.Image, " \t\n") || strings.HasPrefix(s.Image, "-") {
		add("image: %q is not a valid image reference", s.Image)
	}
	seenGPU := map[int]bool{}
	for _, g := range s.GPUs {
		if g < 0 || seenGPU[g] {
			add("gpus: invalid or duplicate index %d", g)
		}
		seenGPU[g] = true
	}
	if s.SSHPort < 1 || s.SSHPort > 65535 {
		add("ssh_port: %d out of range", s.SSHPort)
	}
	hostPorts := map[string]bool{strconv.Itoa(s.SSHPort) + "/tcp": true}
	for _, p := range s.Ports {
		m := validPort.FindStringSubmatch(p)
		if m == nil {
			add("ports: %q (want HOST:CONTAINER[/tcp|/udp])", p)
			continue
		}
		hp, _ := strconv.Atoi(m[1])
		cp, _ := strconv.Atoi(m[2])
		if hp < 1 || hp > 65535 || cp < 1 || cp > 65535 {
			add("ports: %q out of range", p)
		}
		proto := m[4]
		if proto == "" {
			proto = "tcp"
		}
		key := m[1] + "/" + proto
		if hostPorts[key] {
			add("ports: host port %s used twice", key)
		}
		hostPorts[key] = true
	}
	size, err := s.Volume.Bytes()
	switch {
	case err != nil || s.Volume.Size == "":
		add("volume.size: %q (want e.g. 500G, 2T)", s.Volume.Size)
	case size < MinVolumeSize:
		add("volume.size: %s is below the 64M minimum", s.Volume.Size)
	}
	if fs := s.Volume.Filesystem(); fs != "ext4" && fs != "xfs" {
		add("volume.fs: %q (want ext4 or xfs)", fs)
	}
	if len(s.Users) == 0 {
		add("users: at least one user is required")
	}
	seenUser, seenUID := map[string]bool{}, map[int]bool{}
	for _, u := range s.Users {
		if !validAccount.MatchString(u.Name) || u.Name == "root" {
			add("users: invalid name %q", u.Name)
		}
		if u.UID < 1000 || u.UID > 65533 || u.GID < 1000 || u.GID > 65533 {
			add("users: %s uid/gid must be 1000-65533", u.Name)
		}
		if seenUser[u.Name] || seenUID[u.UID] {
			add("users: %s or uid %d listed twice", u.Name, u.UID)
		}
		seenUser[u.Name], seenUID[u.UID] = true, true
	}
	for _, o := range s.Operators {
		if !validAccount.MatchString(o) || o == "root" {
			add("operators: invalid account %q", o)
		}
	}
	for _, b := range s.ExtraBinds {
		if _, _, _, err := ParseBind(b); err != nil {
			add("extra_binds: %v", err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("tenant %s: %s", s.Name, strings.Join(errs, "; "))
	}
	return nil
}

// ParseBind reads SRC:DST[:ro|rw] with absolute paths outside the targets
// rootfiles mounts itself.
func ParseBind(b string) (src, dst string, readOnly bool, err error) {
	parts := strings.Split(b, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return "", "", false, fmt.Errorf("%q (want SRC:DST[:ro|rw])", b)
	}
	src, dst = parts[0], parts[1]
	if len(parts) == 3 {
		switch parts[2] {
		case "ro":
			readOnly = true
		case "rw":
		default:
			return "", "", false, fmt.Errorf("%q: mode must be ro or rw", b)
		}
	}
	if !filepath.IsAbs(src) || !filepath.IsAbs(dst) || strings.ContainsAny(b, ", \n") {
		return "", "", false, fmt.Errorf("%q: paths must be absolute without commas or spaces", b)
	}
	dst = filepath.Clean(dst)
	for _, r := range reservedTargets {
		if dst == r || strings.HasPrefix(dst, r+"/") || dst == "/" {
			return "", "", false, fmt.Errorf("%q: %s is managed by rootfiles", b, dst)
		}
	}
	return filepath.Clean(src), dst, readOnly, nil
}

// Digest identifies the settings that only a new container can change. A
// running container whose label differs is out of date.
func (s *Spec) Digest() string {
	gpus := append([]int(nil), s.GPUs...)
	sort.Ints(gpus)
	b, _ := json.Marshal(struct {
		Name, Image string
		GPUs        []int
		SSH         int
		Ports       []string
		Binds       []string
	}{s.Name, s.Image, gpus, s.SSHPort, s.Ports, s.ExtraBinds})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// User returns the named user.
func (s *Spec) User(name string) (User, bool) {
	for _, u := range s.Users {
		if u.Name == name {
			return u, true
		}
	}
	return User{}, false
}

// HasOperator reports whether account may run `tenant ops` on s.
func (s *Spec) HasOperator(account string) bool {
	for _, o := range s.Operators {
		if o == account {
			return true
		}
	}
	return false
}

// Marshal renders the spec file.
func (s *Spec) Marshal() ([]byte, error) {
	b, err := yaml.Marshal(s)
	if err != nil {
		return nil, err
	}
	return append([]byte("# Managed by rootfiles: edit, then run `rootfiles tenant apply "+s.Name+"`\n"), b...), nil
}

// SpecDir is where tenant specs live under a home base.
func SpecDir(homeBase string) string {
	return filepath.Join(homeBase, ".rootfiles", "tenants")
}

// LoadSpec reads and validates tenant name from dir.
func LoadSpec(dir, name string) (*Spec, error) {
	if err := CheckName(name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, name+".yaml"))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("tenant %s not found (no %s)", name, filepath.Join(dir, name+".yaml"))
	}
	if err != nil {
		return nil, err
	}
	var s Spec
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parsing tenant %s: %w", name, err)
	}
	if s.Name != name {
		return nil, fmt.Errorf("tenant file %s.yaml names tenant %q", name, s.Name)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// ListSpecs returns every tenant name in dir, sorted.
func ListSpecs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		n, ok := strings.CutSuffix(e.Name(), ".yaml")
		if ok && e.Type().IsRegular() && CheckName(n) == nil {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}
