package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

const remoteBinary = "/usr/local/bin/rootfiles"
const defaultTimeout = 45 * time.Second
const DefaultMutationTimeout = 15 * time.Minute
const MaxMutationTimeout = 24 * time.Hour

type SSHRunner struct {
	Binary          string
	Timeout         time.Duration // read-only override, capped at 45s
	MutationTimeout time.Duration // mutating SSH deadline; defaults to 15m
}

// RunRemote executes one already-constructed remote command. Callers must use
// fixed command tokens and validate any values embedded in the shell string.
func (r SSHRunner) RunRemote(ctx context.Context, h NamedHost, op, remote string, input []byte) Result {
	return r.run(ctx, h, op, remote, input)
}

// RunMutationRemote applies the bounded mutation deadline to one remote write.
func (r SSHRunner) RunMutationRemote(ctx context.Context, h NamedHost, op, remote string, input []byte) Result {
	return r.runMutation(ctx, h, op, remote, input)
}

func ValidateMutationTimeout(timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("mutation timeout must be greater than zero")
	}
	if timeout > MaxMutationTimeout {
		return fmt.Errorf("mutation timeout must not exceed 24h")
	}
	return nil
}

func (r SSHRunner) readTimeout() time.Duration {
	if r.Timeout > 0 && r.Timeout < defaultTimeout {
		return r.Timeout
	}
	return defaultTimeout
}
func (r SSHRunner) mutationTimeout() time.Duration {
	if r.MutationTimeout > 0 && r.MutationTimeout <= MaxMutationTimeout {
		return r.MutationTimeout
	}
	return DefaultMutationTimeout
}

type Result struct {
	Host        string          `json:"host"`
	Command     string          `json:"command"`
	State       string          `json:"state"`
	Reason      string          `json:"reason,omitempty"`
	HealthState string          `json:"health_state,omitempty"`
	Remote      json.RawMessage `json:"remote,omitempty"`
	Output      string          `json:"output,omitempty"`
	ExitCode    int             `json:"exit_code"`
}

func (r SSHRunner) ReadOnly(ctx context.Context, h NamedHost, op string) Result {
	if op != "status" && op != "check" && op != "doctor" {
		return Result{Host: h.Name, Command: op, State: "error", Reason: "unsupported read-only command", ExitCode: 1}
	}
	cmdline := remoteBinary + " " + op + " -o json"
	if h.Sudo == "nopasswd" {
		cmdline = "sudo -n " + cmdline
	}
	res := r.run(ctx, h, op, cmdline, nil)
	res = inspectStatusHealth(res)
	res = compareFingerprint(res, h.Host.ConfigFingerprint)
	if res.State == "error" && op == "status" && len(res.Remote) == 0 && missingRemoteBinary(res) {
		probe := "hostname; cat /etc/os-release; uptime; " + remoteBinary + " --version 2>&1"
		p := r.run(ctx, h, op, "/bin/sh -c "+shellQuote(probe), nil)
		if p.State != "unreachable" && p.State != "needs-privilege" && len(p.Remote) == 0 && missingRemoteBinary(p) {
			p.State = "missing"
			p.Reason = "rootfiles is not installed"
			p.Remote = nil
		}
		return p
	}
	return res
}

func inspectStatusHealth(res Result) Result {
	if res.Command != "status" || len(res.Remote) == 0 || (res.State != "ok" && res.State != "drift") {
		return res
	}
	var report struct {
		ConfigError      string `json:"config_error"`
		ModuleCheckError string `json:"module_check_error"`
		Modules          []struct {
			Satisfied bool `json:"satisfied"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(res.Remote, &report); err != nil {
		res.State = "error"
		res.Reason = joinReason(res.Reason, "invalid status JSON")
		return res
	}
	if report.ConfigError != "" {
		res.State = "error"
		res.Reason = joinReason(res.Reason, "remote config error: "+report.ConfigError)
		return res
	}
	if report.ModuleCheckError != "" {
		res.State = "error"
		res.Reason = joinReason(res.Reason, "remote module check error: "+report.ModuleCheckError)
		return res
	}
	for _, module := range report.Modules {
		if !module.Satisfied {
			res.State = "drift"
			res.Reason = joinReason(res.Reason, "one or more remote modules are unsatisfied")
			break
		}
	}
	return res
}

func statusSystem(res Result) (*config.SystemInfo, bool) {
	var report struct {
		System *config.SystemInfo `json:"system"`
	}
	if len(res.Remote) == 0 || json.Unmarshal(res.Remote, &report) != nil || report.System == nil || strings.TrimSpace(report.System.OS) == "" {
		return nil, false
	}
	return report.System, true
}

// ambiguousHomeBaseStatus reports a remote status whose config error is a
// home base the host refused to guess. Site configs always set home_base,
// so a rollout settles it.
func ambiguousHomeBaseStatus(res Result) bool {
	if res.Command != "status" || res.ExitCode != 0 || len(res.Remote) == 0 {
		return false
	}
	var report struct {
		Ambiguous bool `json:"home_base_ambiguous"`
	}
	return json.Unmarshal(res.Remote, &report) == nil && report.Ambiguous
}

func unverifiedStatus(res Result) bool {
	return res.Command == "status" && res.State == "error" && res.ExitCode == 0 && strings.Contains(res.Reason, "inventory configuration is unverified")
}

func compareFingerprint(res Result, expected string) Result {
	if expected == "" || (res.State != "ok" && res.State != "drift" && res.State != "findings") {
		return res
	}
	var report struct {
		AppliedConfigSHA256 string `json:"applied_config_sha256"`
	}
	if len(res.Remote) == 0 || json.Unmarshal(res.Remote, &report) != nil || report.AppliedConfigSHA256 == "" {
		if res.State != "ok" {
			res.HealthState = res.State
		}
		res.State = "error"
		res.Reason = joinReason(res.Reason, "remote config fingerprint is missing; inventory configuration is unverified")
		return res
	}
	if report.AppliedConfigSHA256 != expected {
		if res.State != "ok" {
			res.HealthState = res.State
		}
		res.State = "drift"
		res.Reason = joinReason("remote applied config fingerprint differs from inventory", res.Reason)
	}
	return res
}

func joinReason(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

func (r SSHRunner) run(ctx context.Context, h NamedHost, op, remote string, input []byte) Result {
	return r.runWithTimeout(ctx, h, op, remote, input, r.readTimeout())
}

func (r SSHRunner) runMutation(ctx context.Context, h NamedHost, op, remote string, input []byte) Result {
	return r.runWithTimeout(ctx, h, op, remote, input, r.mutationTimeout())
}

func (r SSHRunner) runWithTimeout(ctx context.Context, h NamedHost, op, remote string, input []byte, timeout time.Duration) Result {
	name := r.Binary
	if name == "" {
		name = "ssh"
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "--", h.Host.SSH, remote}
	cmd := exec.CommandContext(cctx, name, args...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	combined := out.String()
	if stderr.Len() > 0 {
		if combined != "" {
			combined += "\n"
		}
		combined += stderr.String()
	}
	result := Result{Host: h.Name, Command: op, Output: strings.TrimSpace(combined), ExitCode: 0}
	if err != nil {
		result.ExitCode = 1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			result.ExitCode = ee.ExitCode()
		}
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			result.State = "unreachable"
			result.Reason = "ssh timed out"
			return result
		}
		if result.ExitCode == 255 {
			result.State = "unreachable"
			result.Reason = "ssh connection failed"
			return result
		}
		if looksPrivilege(combined) {
			result.State = "needs-privilege"
			result.Reason = "remote sudo requires additional privilege"
			return result
		}
		if result.ExitCode == 2 && (op == "check" || op == "doctor") {
			return decodeRemote(result, out.Bytes(), 2)
		}
		result.State = "error"
		result.Reason = shortReason(combined)
		return result
	}
	return decodeRemote(result, out.Bytes(), result.ExitCode)
}

func decodeRemote(res Result, data []byte, exit int) Result {
	res.ExitCode = exit
	trim := bytes.TrimSpace(data)
	if len(trim) > 0 && json.Valid(trim) {
		res.Remote = append(json.RawMessage(nil), trim...)
	}
	if exit == 0 && isReadOnlyCommand(res.Command) && len(res.Remote) == 0 {
		res.State = "error"
		res.Reason = "remote command did not return JSON"
		return res
	}
	switch {
	case exit == 0:
		res.State = "ok"
		res.Reason = ""
	case exit == 2 && res.Command == "check":
		res.State = "drift"
		res.Reason = "configuration drift detected"
	case exit == 2 && res.Command == "doctor":
		res.State = "findings"
		res.Reason = "doctor reported findings"
	default:
		res.State = "error"
		if res.Reason == "" {
			res.Reason = shortReason(string(data))
		}
	}
	return res
}

func isReadOnlyCommand(command string) bool {
	return command == "status" || command == "check" || command == "doctor"
}

func statusProvesInstalled(res Result) bool {
	if res.Command != "status" || res.ExitCode != 0 {
		return false
	}
	system, ok := statusSystem(res)
	if !ok || strings.TrimSpace(system.Version) == "" {
		return false
	}
	var report struct {
		ConfigError      string `json:"config_error"`
		ModuleCheckError string `json:"module_check_error"`
	}
	if json.Unmarshal(res.Remote, &report) != nil || (report.ConfigError != "" && !ambiguousHomeBaseStatus(res)) || report.ModuleCheckError != "" {
		return false
	}
	return true
}

func missingRemoteBinary(res Result) bool {
	if len(res.Remote) > 0 {
		return false
	}
	s := strings.ToLower(res.Output)
	if !strings.Contains(s, strings.ToLower(remoteBinary)) {
		return false
	}
	return strings.Contains(s, "not found") || strings.Contains(s, "no such file") || strings.Contains(s, "command not found")
}
func looksPrivilege(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "a password is required") || strings.Contains(s, "password is required") || strings.Contains(s, "not allowed to execute") || strings.Contains(s, "not in the sudoers") || strings.Contains(s, "sudo: a terminal is required")
}
func shortReason(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "remote command failed"
	}
	if len(s) > 240 {
		s = s[:240]
	}
	return s
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
