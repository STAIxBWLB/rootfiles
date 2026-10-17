package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"gopkg.in/yaml.v3"
)

var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

type RunRecord struct {
	Time     time.Time `json:"time"`
	Operator string    `json:"operator"`
	Command  string    `json:"command"`
	Hosts    []Result  `json:"hosts"`
}

func ValidateVersion(v string) error {
	if !releaseVersion.MatchString(v) {
		return fmt.Errorf("version must match vX.Y.Z")
	}
	return nil
}

func Apply(ctx context.Context, runner SSHRunner, hosts []NamedHost, dry bool) []Result {
	return serial(ctx, runner, hosts, "apply", func(h NamedHost) Result {
		if h.Host.EffectiveConfig == nil {
			return Result{Host: h.Name, Command: "apply", State: "error", Reason: "host has no site config", ExitCode: 1}
		}
		status := runner.ReadOnly(ctx, h, "status")
		if status.ExitCode != 0 || (status.State != "ok" && status.State != "drift" && !unverifiedStatus(status) && !ambiguousHomeBaseStatus(status)) {
			status.Command = "apply"
			return status
		}
		system, ok := statusSystem(status)
		if !ok {
			return Result{Host: h.Name, Command: "apply", State: "error", Reason: "could not verify remote operating system before apply", ExitCode: 1}
		}
		var effective config.Config
		if err := yaml.Unmarshal(h.Host.EffectiveConfig, &effective); err != nil {
			return Result{Host: h.Name, Command: "apply", State: "error", Reason: "could not load effective site config for capability validation: " + err.Error(), ExitCode: 1}
		}
		modules := module.NewRegistry().Resolve(&effective, nil)
		names := make([]string, 0, len(modules))
		for _, m := range modules {
			names = append(names, m.Name())
		}
		if err := config.ValidateCapabilities(&effective, system, names); err != nil {
			return Result{Host: h.Name, Command: "apply", State: "error", Reason: err.Error(), ExitCode: 1}
		}
		// Dry-run never writes the stable remote config file. A real run persists
		// it root-owned so state records an actual path and future checks reload it.
		if dry {
			return runner.runMutation(ctx, h, "apply", privileged(h.Sudo, remoteBinary+" apply --yes --dry-run --config -"), h.Host.EffectiveConfig)
		}
		path, err := effectiveConfigPath(h)
		if err != nil {
			return Result{Host: h.Name, Command: "apply", State: "error", Reason: err.Error(), ExitCode: 1}
		}
		sum := sha256.Sum256(h.Host.EffectiveConfig)
		contentHash := hex.EncodeToString(sum[:])
		script := persistConfigScript(path, contentHash) + "; exec " + remoteBinary + " apply --yes --config " + shellQuote(path)
		return runner.runMutation(ctx, h, "apply", privileged(h.Sudo, "/bin/sh -c "+shellQuote(script)), h.Host.EffectiveConfig)
	})
}

func effectiveConfigPath(h NamedHost) (string, error) {
	if len(h.Host.EffectiveConfig) == 0 {
		return "", fmt.Errorf("host %q has empty effective config", h.Name)
	}
	sum := sha256.Sum256(h.Host.EffectiveConfig)
	return "/etc/rootfiles/fleet/" + h.Name + "-" + hex.EncodeToString(sum[:]) + ".yaml", nil
}

func persistConfigScript(path, contentHash string) string {
	return fmt.Sprintf(`set -eu
dir=%s
target=%s
expected_sha=%s
check_dir() { [ -d "$1" ] && [ ! -L "$1" ] || return 1; [ "$(stat -c %%u:%%g "$1")" = 0:0 ] || return 1; mode=$(stat -c %%a "$1"); [ "$((0$mode & 022))" -eq 0 ]; }
check_dir /etc || { echo 'untrusted /etc directory' >&2; exit 1; }
if [ -L /etc/rootfiles ]; then echo 'untrusted config parent symlink' >&2; exit 1; fi
if [ ! -e /etc/rootfiles ]; then install -d -o root -g root -m 0755 /etc/rootfiles; fi
check_dir /etc/rootfiles || { echo 'untrusted config parent directory' >&2; exit 1; }
if [ -L "$dir" ]; then echo 'untrusted fleet config directory symlink' >&2; exit 1; fi
if [ ! -e "$dir" ]; then install -d -o root -g root -m 0755 "$dir"; fi
check_dir "$dir" || { echo 'untrusted fleet config directory' >&2; exit 1; }
umask 077
tmp=$(mktemp "$dir/.rootfiles-config.XXXXXX")
trap 'rm -f "$tmp"' EXIT
cat > "$tmp"
chown root:root "$tmp"
chmod 0600 "$tmp"
actual_sha=$(sha256sum "$tmp" | awk '{print $1}')
[ "$actual_sha" = "$expected_sha" ] || { echo 'effective config checksum mismatch' >&2; exit 1; }
if [ -L "$target" ]; then echo 'refusing symlink config target' >&2; exit 1; fi
if [ -e "$target" ]; then
  [ -f "$target" ] && [ "$(stat -c %%u:%%g "$target")" = 0:0 ] && [ "$(stat -c %%a "$target")" = 600 ] && cmp -s "$tmp" "$target" || { echo 'immutable config target collision' >&2; exit 1; }
  rm -f "$tmp"
else
  mv -f -- "$tmp" "$target"
fi`, shellQuote(filepath.Dir(path)), shellQuote(path), shellQuote(contentHash))
}

func Update(ctx context.Context, runner SSHRunner, hosts []NamedHost, version string, dry bool) ([]Result, error) {
	if err := ValidateVersion(version); err != nil {
		return nil, err
	}
	return serial(ctx, runner, hosts, "update", func(h NamedHost) Result {
		if dry {
			return Result{Host: h.Name, Command: "update", State: "ok", Reason: "dry-run: would update to " + version}
		}
		cmd := remoteBinary + " update --version " + version
		r := runner.runMutation(ctx, h, "update", privileged(h.Sudo, cmd), nil)
		if r.State != "ok" {
			return r
		}
		return postUpdateCheck(ctx, runner, h, r, version)
	}), nil
}

// postUpdateCheck runs the new binary's read-only check: an update replaces
// only the binary, so a release that changes generated files leaves the
// host apply-pending. It never applies.
func postUpdateCheck(ctx context.Context, runner SSHRunner, h NamedHost, updated Result, version string) Result {
	check := runner.ReadOnly(ctx, h, "check")
	if check.State == "ok" {
		return updated
	}
	// Keep both commands' evidence: the update output and the check's exit
	// code and output.
	updated.ExitCode = check.ExitCode
	if updated.ExitCode == 0 {
		updated.ExitCode = 1
	}
	updated.Output = joinReason(updated.Output, "post-update check: "+check.Output)
	switch check.State {
	case "drift":
		updated.State = "apply-pending"
		updated.Reason = "updated to " + version + "; apply pending"
		if modules := unsatisfiedModules(check.Remote); len(modules) > 0 {
			updated.Reason += ": " + strings.Join(modules, ", ")
		} else {
			updated.Reason += ": " + check.Reason
		}
		updated.Remote = check.Remote
		return updated
	default:
		updated.State = "error"
		updated.Reason = "updated to " + version + "; post-update check " + check.State + ": " + check.Reason
		return updated
	}
}

func unsatisfiedModules(remote json.RawMessage) []string {
	var report struct {
		Modules []struct {
			Name      string `json:"name"`
			Satisfied bool   `json:"satisfied"`
		} `json:"modules"`
	}
	if json.Unmarshal(remote, &report) != nil {
		return nil
	}
	var names []string
	for _, m := range report.Modules {
		if !m.Satisfied {
			names = append(names, m.Name)
		}
	}
	return names
}

func Schedule(ctx context.Context, runner SSHRunner, hosts []NamedHost, action string, dry bool) ([]Result, error) {
	if action != "enable" && action != "disable" {
		return nil, fmt.Errorf("schedule action must be enable or disable")
	}
	return serial(ctx, runner, hosts, "schedule "+action, func(h NamedHost) Result {
		if dry {
			return Result{Host: h.Name, Command: "schedule", State: "ok", Reason: "dry-run: would " + action + " scheduled reports"}
		}
		return runner.runMutation(ctx, h, "schedule", privileged(h.Sudo, remoteBinary+" schedule "+action), nil)
	}), nil
}

func Bootstrap(ctx context.Context, runner SSHRunner, hosts []NamedHost, version string, dry bool) ([]Result, error) {
	if err := ValidateVersion(version); err != nil {
		return nil, err
	}
	return serial(ctx, runner, hosts, "bootstrap", func(h NamedHost) Result {
		if dry {
			return Result{Host: h.Name, Command: "bootstrap", State: "ok", Reason: "dry-run: would install " + version}
		}
		status := runner.ReadOnly(ctx, h, "status")
		_, hasSystem := statusSystem(status)
		if statusProvesInstalled(status) {
			return Result{Host: h.Name, Command: "bootstrap", State: "ok", Reason: "rootfiles is already installed; skipped"}
		}
		if status.State != "missing" && !(hasSystem && unverifiedStatus(status)) {
			status.Command = "bootstrap"
			return status
		}
		// Release artifacts and checksums are selected from a pinned tag, then
		// verified on the destination before extraction or installation.
		script := `set -eu
v="$ROOTFILES_VERSION"
case "$(uname -m)" in x86_64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo "unsupported architecture" >&2; exit 1;; esac
ver=${v#v}
name="rootfiles_${ver}_linux_${arch}.tar.gz"
base="https://github.com/STAIxBWLB/rootfiles/releases/download/${v}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "$base/$name" -o "$tmp/$name"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"
grep -E "^[0-9a-fA-F]{64}[[:space:]]+\*?${name}$" "$tmp/checksums.txt" > "$tmp/expected"
(cd "$tmp" && sha256sum -c expected)
tar -xzf "$tmp/$name" -C "$tmp" rootfiles
install -o root -g root -m 0755 "$tmp/rootfiles" /usr/local/bin/rootfiles
`
		cmd := "env ROOTFILES_VERSION=" + shellQuote(version) + " /bin/sh -c " + shellQuote(script)
		return runner.runMutation(ctx, h, "bootstrap", privileged(h.Sudo, cmd), nil)
	}), nil
}

func serial(ctx context.Context, runner SSHRunner, hosts []NamedHost, op string, fn func(NamedHost) Result) []Result {
	out := make([]Result, 0, len(hosts))
	stopped := false
	for _, h := range hosts {
		if stopped {
			out = append(out, Result{Host: h.Name, Command: op, State: "skipped", Reason: "an earlier host failed", ExitCode: -1})
			continue
		}
		r := fn(h)
		if r.State == "unreachable" && r.Reason == reasonConnectFailed && runner.SkipUnreachable {
			r.State = "skipped-unreachable"
		}
		out = append(out, r)
		// apply-pending is a finished update the operator still has to apply.
		if r.State != "ok" && r.State != "apply-pending" && r.State != "skipped-unreachable" {
			stopped = true
		}
	}
	return out
}

func privileged(sudo, command string) string {
	if sudo == "nopasswd" {
		return "sudo -n " + command
	}
	return command
}

func AppendRunLog(path, command string, results []Result) error {
	if path == "" {
		path = defaultRunLog()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("LOGNAME")
	}
	line, err := json.Marshal(RunRecord{Time: time.Now().UTC(), Operator: user, Command: command, Hosts: results})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

func defaultRunLog() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "rootfiles", "fleet.jsonl")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "rootfiles", "fleet.jsonl")
}

type target struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

func RenderTargets(inv *Inventory) ([]byte, error) {
	if inv == nil {
		return nil, fmt.Errorf("inventory is required")
	}
	entries := make([]target, 0)
	names := make([]string, 0, len(inv.Hosts))
	for n := range inv.Hosts {
		names = append(names, n)
	}
	sortStrings(names)
	for _, name := range names {
		h := inv.Hosts[name]
		if h.Address == "" {
			continue
		}
		var mc config.MonitoringConfig
		if len(h.EffectiveConfig) > 0 {
			var cfg config.Config
			if err := yamlUnmarshal(h.EffectiveConfig, &cfg); err != nil {
				return nil, fmt.Errorf("host %q effective config: %w", name, err)
			}
			mc = cfg.Modules.Monitoring
		}
		labels := map[string]string{"host": name}
		for _, g := range h.Groups {
			labels["group_"+safeLabel(g)] = "true"
		}
		if mc.NodeExporter {
			entries = append(entries, target{Targets: []string{netJoin(h.Address, mc.NodePort())}, Labels: cloneLabels(labels, "exporter", "node")})
		}
		if mc.DCGMExporter {
			entries = append(entries, target{Targets: []string{netJoin(h.Address, mc.DCGMPort())}, Labels: cloneLabels(labels, "exporter", "dcgm")})
		}
	}
	if entries == nil {
		entries = []target{}
	}
	return json.MarshalIndent(entries, "", "  ")
}

func cloneLabels(src map[string]string, k, v string) map[string]string {
	out := map[string]string{}
	for a, b := range src {
		out[a] = b
	}
	out[k] = v
	return out
}
func safeLabel(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
func netJoin(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s:%d", host, port)
}
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
func yamlUnmarshal(b []byte, v any) error { return yaml.Unmarshal(b, v) }
