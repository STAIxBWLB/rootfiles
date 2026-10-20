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
	return `# Managed by rootfiles
[Unit]
Description=rootfiles sshpiper: one SSH port for tenant containers
After=network-online.target docker.service
Wants=network-online.target

[Service]
ExecStart=` + d + `/sshpiperd --address ` + addr + ` --port ` + strconv.Itoa(sp.ListenPort()) +
		` --server-key ` + piperConfDir + `/ssh_host_ed25519_key --server-key-generate-mode notexist ` +
		d + `/plugins/yaml --config ` + piperConfDir + `/tenants/*.yaml -- ` + d + `/plugins/failtoban
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

// piperPortTaken reports whether another process listens on the port.
func piperPortTaken(ctx context.Context, rc *RunContext, port int) bool {
	if piperActive(ctx, rc, piperUnit) {
		return false
	}
	res, err := rc.Runner.Query(ctx, "ss", "-H", "-ltn", "sport = :"+strconv.Itoa(port))
	return err == nil && strings.TrimSpace(res.Stdout) != ""
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
	if cur, err := rc.Runner.ReadFile(piperUnitPath()); err != nil || string(cur) != piperUnitContent(rc) {
		add("Write "+piperUnit, "write "+piperUnitPath())
	}
	if !unitOn(ctx, rc, piperUnit) || !piperActive(ctx, rc, piperUnit) {
		if piperPortTaken(ctx, rc, sp.ListenPort()) {
			changes = append(changes, Change{Description: fmt.Sprintf("Port %d is held by another process; free it or set modules.tenant.sshpiper.port", sp.ListenPort()), State: ChangeBlocked})
		} else {
			add("Start "+piperUnit+" on port "+strconv.Itoa(sp.ListenPort()), "systemctl enable --now "+piperUnit)
		}
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
		if piperPortTaken(ctx, rc, sp.ListenPort()) {
			return fmt.Errorf("port %d is held by another process; free it or set modules.tenant.sshpiper.port", sp.ListenPort())
		}
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
