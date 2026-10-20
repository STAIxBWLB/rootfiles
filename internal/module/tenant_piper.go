package module

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/STAIxBWLB/rootfiles/internal/tenant"
)

// Overridable in tests.
var (
	piperLibDir  = "/usr/local/lib/rootfiles/sshpiper"
	piperConfDir = "/etc/rootfiles/sshpiper"
	piperGOARCH  = runtime.GOARCH
	piperURL     = "https://github.com/tg123/sshpiper/releases/download/%s/sshpiperd_with_plugins_linux_%s.tar.gz"
)

const piperUnit = "rootfiles-sshpiper.service"

// piperSums pins the release archives rootfiles installs (from the
// release's checksums.txt). A version not listed here is refused.
var piperSums = map[string]map[string]string{
	"v1.6.1": {
		"x86_64": "95d423a70e843a7512a72fbb16c0fa5dc59217cf064cb3e277520fddffded1e1",
		"arm64":  "ceb92a4b91e8d163c3dafc3d40bc678a32046f076a557ea5b83ff573cf932c6f",
	},
}

var piperFiles = []string{"sshpiperd", "plugins/yaml", "plugins/failtoban"}

func piperArch() (string, error) {
	switch piperGOARCH {
	case "amd64":
		return "x86_64", nil
	case "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("sshpiper: unsupported architecture %s", piperGOARCH)
}

func piperSum(version string) (string, string, error) {
	arch, err := piperArch()
	if err != nil {
		return "", "", err
	}
	sum, ok := piperSums[version][arch]
	if !ok {
		var known []string
		for v := range piperSums {
			known = append(known, v)
		}
		return "", "", fmt.Errorf("modules.tenant.sshpiper.version %s is not pinned by this rootfiles (known: %s)", version, strings.Join(known, ", "))
	}
	return arch, sum, nil
}

func piperDir(version string) string { return filepath.Join(piperLibDir, version) }

func piperInstalled(version string) bool {
	for _, f := range piperFiles {
		if fi, err := os.Stat(filepath.Join(piperDir(version), f)); err != nil || !fi.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func piperUnitPath() string { return filepath.Join(tenantUnitDir, piperUnit) }

func piperUnitContent(rc *RunContext) string {
	sp := rc.Config.Modules.Tenant.SSHPiper
	d := piperDir(sp.Release())
	addr := sp.Address
	if addr == "" {
		addr = "0.0.0.0"
	}
	ignore := ""
	for _, ip := range sp.FailtobanIgnore {
		ignore += " --ignore-ip " + ip
	}
	return `# Managed by rootfiles
[Unit]
Description=rootfiles sshpiper: one SSH port for tenant containers
After=network-online.target docker.service
Wants=network-online.target

[Service]
ExecStart=` + d + `/sshpiperd --address ` + addr + ` --port ` + strconv.Itoa(sp.ListenPort()) +
		` --server-key ` + piperConfDir + `/ssh_host_ed25519_key --server-key-generate-mode notexist ` +
		d + `/plugins/yaml --config ` + piperConfDir + `/tenants/*.yaml -- ` + d + `/plugins/failtoban` + ignore + `
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`
}

func newPiper(rc *RunContext) *tenant.Piper {
	sp := rc.Config.Modules.Tenant.SSHPiper
	if !sp.Enabled {
		return nil
	}
	return &tenant.Piper{ConfigDir: piperConfDir, Port: sp.ListenPort()}
}

func piperRouteFiles() []string {
	files, _ := filepath.Glob(filepath.Join(piperConfDir, "tenants", "*.yaml"))
	return files
}

func piperActive(ctx context.Context, rc *RunContext, unit string) bool {
	res, err := rc.Runner.Query(ctx, "systemctl", "is-active", unit)
	return err == nil && strings.TrimSpace(res.Stdout) == "active"
}

// piperPortTaken reports whether a process other than sshpiperd listens
// on the port, so a port change never restarts sshpiper onto a held port.
func piperPortTaken(ctx context.Context, rc *RunContext, port int) bool {
	res, err := rc.Runner.Query(ctx, "ss", "-H", "-ltnp", "sport = :"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	out := strings.TrimSpace(res.Stdout)
	return out != "" && !strings.Contains(out, `"sshpiperd"`)
}

// piperBlock names what would keep the shared port from working: another
// process holding it, an active UFW without an allowance that the network
// module will not add in this run, or a UFW whose state cannot be read.
func piperBlock(ctx context.Context, rc *RunContext, port int) string {
	if piperPortTaken(ctx, rc, port) {
		return fmt.Sprintf("port %d is held by another process; free it or set modules.tenant.sshpiper.port", port)
	}
	if !rc.Runner.CommandExists("ufw") {
		return ""
	}
	res, err := rc.Runner.Query(ctx, "ufw", "status")
	if err != nil {
		return fmt.Sprintf("cannot read the UFW state to admit port %d: %s", port, firstLine(err.Error()))
	}
	st := parseUFWStatus(res.Stdout)
	if !st.Active || st.Allowed[port] || rc.Config.Modules.Network.UFW && rc.moduleSucceeding("network") {
		return ""
	}
	return fmt.Sprintf("UFW is active and does not allow port %d; enable modules.network with ufw, or run `ufw allow %d/tcp`", port, port)
}

func piperChanges(ctx context.Context, rc *RunContext) ([]Change, error) {
	var changes []Change
	add := func(desc, cmd string) { changes = append(changes, Change{Description: desc, Command: cmd}) }
	sp := rc.Config.Modules.Tenant.SSHPiper
	if !sp.Enabled {
		if rc.Runner.FileExists(piperUnitPath()) {
			add("Remove "+piperUnit, "systemctl disable --now "+piperUnit)
		}
		if len(piperRouteFiles()) > 0 {
			add("Remove sshpiper tenant routes", "remove "+piperConfDir+"/tenants/*.yaml")
		}
		return changes, nil
	}
	if _, _, err := piperSum(sp.Release()); err != nil {
		return nil, err
	}
	if !piperInstalled(sp.Release()) {
		add("Install sshpiper "+sp.Release(), "download and verify into "+piperDir(sp.Release()))
	}
	if !rc.Runner.FileExists(filepath.Join(piperConfDir, "upstream_ed25519")) {
		add("Generate sshpiper upstream key", "ssh-keygen -f "+piperConfDir+"/upstream_ed25519")
	}
	if block := piperBlock(ctx, rc, sp.ListenPort()); block != "" {
		return append(changes, Change{Description: block, State: ChangeBlocked}), nil
	}
	if cur, err := rc.Runner.ReadFile(piperUnitPath()); err != nil || string(cur) != piperUnitContent(rc) {
		add("Write "+piperUnit, "write "+piperUnitPath())
	}
	if !unitOn(ctx, rc, piperUnit) || !piperActive(ctx, rc, piperUnit) {
		add("Start "+piperUnit+" on port "+strconv.Itoa(sp.ListenPort()), "systemctl enable --now "+piperUnit)
	}
	return changes, nil
}

func applyPiper(ctx context.Context, rc *RunContext, note func(string)) error {
	sp := rc.Config.Modules.Tenant.SSHPiper
	if !sp.Enabled {
		if rc.Runner.FileExists(piperUnitPath()) {
			_, _ = rc.Runner.Run(ctx, "systemctl", "disable", "--now", piperUnit)
			if err := rc.Runner.Remove(piperUnitPath()); err != nil {
				return err
			}
			_ = systemdReload(ctx, rc)
			note("removed " + piperUnit)
		}
		for _, f := range piperRouteFiles() {
			if err := rc.Runner.Remove(f); err != nil {
				return err
			}
			note("removed " + f)
		}
		return nil
	}
	if !piperInstalled(sp.Release()) {
		if err := installPiper(ctx, rc, sp.Release()); err != nil {
			return err
		}
		note("installed sshpiper " + sp.Release())
	}
	if _, err := rc.Runner.Run(ctx, "install", "-d", "-m", "0700", piperConfDir, filepath.Join(piperConfDir, "tenants")); err != nil {
		return err
	}
	key := filepath.Join(piperConfDir, "upstream_ed25519")
	if !rc.Runner.FileExists(key) {
		if _, err := rc.Runner.Run(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "rootfiles-sshpiper", "-f", key); err != nil {
			return fmt.Errorf("generating sshpiper upstream key: %w", err)
		}
		note("generated sshpiper upstream key")
	}
	// Checked before the unit changes, so a bad port never stops a working
	// listener.
	if block := piperBlock(ctx, rc, sp.ListenPort()); block != "" {
		return fmt.Errorf("%s", block)
	}
	restart := false
	if cur, err := rc.Runner.ReadFile(piperUnitPath()); err != nil || string(cur) != piperUnitContent(rc) {
		if err := rc.Runner.WriteFile(piperUnitPath(), []byte(piperUnitContent(rc)), 0o644); err != nil {
			return err
		}
		if w := systemdReload(ctx, rc); w != "" {
			return fmt.Errorf("%s", w)
		}
		restart = err == nil // an existing unit changed
		note("wrote " + piperUnitPath())
	}
	switch {
	case !unitOn(ctx, rc, piperUnit) || !piperActive(ctx, rc, piperUnit):
		if _, err := rc.Runner.Run(ctx, "systemctl", "enable", "--now", piperUnit); err != nil {
			return err
		}
		note(fmt.Sprintf("started %s on port %d", piperUnit, sp.ListenPort()))
	case restart:
		if _, err := rc.Runner.Run(ctx, "systemctl", "restart", piperUnit); err != nil {
			return err
		}
		note("restarted " + piperUnit)
	}
	return nil
}

// installPiper downloads the pinned release, checks its SHA-256 and moves
// sshpiperd and the yaml and failtoban plugins into place.
func installPiper(ctx context.Context, rc *RunContext, version string) error {
	arch, sum, err := piperSum(version)
	if err != nil {
		return err
	}
	dst := piperDir(version)
	if rc.Runner.FileExists(dst) {
		return fmt.Errorf("%s exists but is incomplete; remove it and apply again", dst)
	}
	tmp := filepath.Join(piperLibDir, ".download-"+version)
	archive := filepath.Join(tmp, "sshpiper.tar.gz")
	cleanup := func() { _, _ = rc.Runner.Run(ctx, "rm", "-rf", "--one-file-system", tmp) }
	cleanup()
	if _, err := rc.Runner.Run(ctx, "install", "-d", "-m", "0755", filepath.Join(tmp, "x")); err != nil {
		return err
	}
	if _, err := rc.Runner.Run(ctx, "curl", "-fsSL", "--retry", "3", "-o", archive, fmt.Sprintf(piperURL, version, arch)); err != nil {
		cleanup()
		return fmt.Errorf("downloading sshpiper %s: %w", version, err)
	}
	if !rc.Runner.DryRun {
		got, err := fileSHA256(archive)
		if err != nil || got != sum {
			cleanup()
			return fmt.Errorf("sshpiper %s archive SHA-256 is %s, want %s; not installed", version, got, sum)
		}
	}
	args := append([]string{"-xzf", archive, "--no-same-owner", "-C", filepath.Join(tmp, "x")}, piperFiles...)
	if _, err := rc.Runner.Run(ctx, "tar", args...); err != nil {
		cleanup()
		return fmt.Errorf("unpacking sshpiper: %w", err)
	}
	if err := rc.Runner.Rename(filepath.Join(tmp, "x"), dst); err != nil {
		cleanup()
		return err
	}
	cleanup()
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
