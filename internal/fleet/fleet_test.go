package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadInventoryStrictAndSelectUnion(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "fleet.yaml")
	data := `defaults:
  sudo: nopasswd
  parallel: 2
hosts:
  gpu-a:
    ssh: gpu-a
    groups: [gpu]
  store-a:
    ssh: store-a
    groups: [storage]
`
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	inv, err := LoadInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	h, err := inv.Select([]string{"gpu-a"}, []string{"storage"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[0].Name != "gpu-a" || h[1].Name != "store-a" {
		t.Fatalf("unexpected selection: %#v", h)
	}
	withoutParallel := strings.Replace(data, "  parallel: 2\n", "", 1)
	if err := os.WriteFile(p, []byte(withoutParallel), 0600); err != nil {
		t.Fatal(err)
	}
	defaults, err := LoadInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Defaults.Parallel != 2 {
		t.Fatalf("default parallel = %d, want 2", defaults.Defaults.Parallel)
	}
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := inv.Select(nil, nil, false); err == nil {
		t.Fatal("expected empty selection to fail")
	}
	if _, err := inv.Select(nil, nil, true); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(data, "ssh: gpu-a", "ssh: gpu-a\n    ssh_typo: x", 1)
	if err := os.WriteFile(p, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadInventory(p); err == nil {
		t.Fatal("unknown inventory key was accepted")
	}
	bad = strings.Replace(data, "ssh: gpu-a", "ssh: -oProxyCommand=evil", 1)
	if err := os.WriteFile(p, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadInventory(p); err == nil {
		t.Fatal("option-like ssh destination was accepted")
	}
}

func TestLoadInventoryRejectsUnsafeEndpointValues(t *testing.T) {
	base := `hosts:
  node1:
    ssh: node1
    address: 10.0.0.1
`
	cases := []struct{ name, data string }{
		{"ssh whitespace", strings.Replace(base, "ssh: node1", "ssh: node1 -oProxyCommand=bad", 1)},
		{"ssh control", strings.Replace(base, "ssh: node1", "ssh: \"node1\\nmalicious\"", 1)},
		{"address url", strings.Replace(base, "address: 10.0.0.1", "address: \"https://bad/path\"", 1)},
		{"address whitespace", strings.Replace(base, "address: 10.0.0.1", "address: \"node one\"", 1)},
		{"unsafe name", strings.Replace(base, "node1:", "../node:", 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "fleet.yaml")
			if err := os.WriteFile(p, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadInventory(p); err == nil {
				t.Fatal("unsafe inventory value was accepted")
			}
		})
	}
}

func TestCompareAppliedFingerprint(t *testing.T) {
	matching := Result{State: "ok", Remote: json.RawMessage(`{"applied_config_sha256":"abc"}`)}
	if got := compareFingerprint(matching, "abc"); got.State != "ok" {
		t.Fatalf("matching fingerprint changed state: %#v", got)
	}
	mismatch := Result{State: "findings", Reason: "doctor reported findings", Remote: json.RawMessage(`{"applied_config_sha256":"old"}`)}
	got := compareFingerprint(mismatch, "new")
	if got.State != "drift" || got.HealthState != "findings" || !strings.Contains(got.Reason, "doctor reported findings") {
		t.Fatalf("mismatch did not preserve health findings: %#v", got)
	}
	missing := Result{State: "ok", Remote: json.RawMessage(`{"hostname":"n1"}`)}
	if got := compareFingerprint(missing, "abc"); got.State != "error" || !strings.Contains(got.Reason, "unverified") {
		t.Fatalf("missing fingerprint was not reported: %#v", got)
	}
}

func TestUnverifiedStatusCanProceedOnlyWithRemoteSystemEvidence(t *testing.T) {
	res := Result{Command: "status", State: "ok", ExitCode: 0, Remote: json.RawMessage(`{"system":{"os":"ubuntu","version":"24.04"}}`)}
	res = compareFingerprint(res, "expected")
	if !unverifiedStatus(res) {
		t.Fatalf("missing fingerprint should be explicitly unverified: %#v", res)
	}
	if sys, ok := statusSystem(res); !ok || sys.OS != "ubuntu" {
		t.Fatalf("remote OS was not retained: %#v", res)
	}
	bad := Result{Command: "status", State: "error", ExitCode: 0, Reason: "remote config error: invalid YAML", Remote: res.Remote}
	if unverifiedStatus(bad) {
		t.Fatal("config error was mistaken for fingerprint-only unverified state")
	}
	noOS := Result{Command: "status", State: "error", ExitCode: 0, Reason: "remote config fingerprint is missing; inventory configuration is unverified", Remote: json.RawMessage(`{"hostname":"n1"}`)}
	if _, ok := statusSystem(noOS); ok {
		t.Fatal("missing remote OS evidence was accepted")
	}
}

// AC4c: an ambiguous native home base does not block a rollout whose site
// config sets home_base; any other config error still does.
func TestApplyProceedsOnAmbiguousHomeBaseStatus(t *testing.T) {
	callsFile := filepath.Join(t.TempDir(), "calls")
	run := func(status string) []Result {
		t.Helper()
		bin := writeFakeSSH(t, "#!/bin/sh\n"+
			"printf '%s\\n' \"$7\" >> "+shellQuote(callsFile)+"\n"+
			"case \"$7\" in\n"+
			"  *\"status -o json\"*) printf '%s\\n' "+shellQuote(status)+" ;;\n"+
			"  *) echo applied ;;\n"+
			"esac\n")
		hosts := []NamedHost{{Name: "n1", Host: Host{SSH: "n1", EffectiveConfig: []byte("users:\n  home_base: /home\n")}, Sudo: "none"}}
		return Apply(context.Background(), SSHRunner{Binary: bin}, hosts, true)
	}
	system := `"system":{"os":"ubuntu","version":"24.04"}`
	got := run(`{"config_error":"users.home_base: ambiguous home base: detected /raid/home, but /home/.rootfiles/users.json exists","home_base_ambiguous":true,` + system + `}`)
	if len(got) != 1 || got[0].State != "ok" {
		t.Fatalf("ambiguous home base blocked the rollout: %#v", got)
	}
	calls, _ := os.ReadFile(callsFile)
	if !strings.Contains(string(calls), "apply --yes --dry-run --config -") {
		t.Fatalf("apply was not attempted: %q", calls)
	}
	got = run(`{"config_error":"invalid YAML",` + system + `}`)
	if len(got) != 1 || got[0].State != "error" || !strings.Contains(got[0].Reason, "invalid YAML") {
		t.Fatalf("other config errors must still block: %#v", got)
	}
}

func TestStatusProvesInstalledWithAmbiguousHomeBase(t *testing.T) {
	system := `"system":{"os":"ubuntu","version":"24.04"}`
	ambiguous := Result{Command: "status", ExitCode: 0, Remote: json.RawMessage(`{"config_error":"users.home_base: ambiguous home base","home_base_ambiguous":true,` + system + `}`)}
	if !statusProvesInstalled(ambiguous) {
		t.Fatal("an ambiguous home base hid an installed binary from bootstrap")
	}
	other := Result{Command: "status", ExitCode: 0, Remote: json.RawMessage(`{"config_error":"invalid YAML",` + system + `}`)}
	if statusProvesInstalled(other) {
		t.Fatal("other config errors must not prove a healthy install")
	}
}

func TestInspectStatusHealth(t *testing.T) {
	for _, tc := range []struct{ name, payload, wantState, wantReason string }{
		{"config error", `{"config_error":"bad config","system":{"os":"ubuntu"}}`, "error", "remote config error"},
		{"module error", `{"module_check_error":"probe failed","system":{"os":"ubuntu"}}`, "error", "remote module check error"},
		{"unsatisfied module", `{"modules":[{"name":"packages","satisfied":false}],"system":{"os":"ubuntu"}}`, "drift", "unsatisfied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(tc.payload)
			got := inspectStatusHealth(Result{Command: "status", State: "ok", ExitCode: 0, Remote: raw})
			if got.State != tc.wantState || !strings.Contains(got.Reason, tc.wantReason) {
				t.Fatalf("unexpected status classification: %#v", got)
			}
			if string(got.Remote) != tc.payload {
				t.Fatalf("remote payload changed: %s", got.Remote)
			}
		})
	}
}

func TestImmutableRemoteConfigPathAndScript(t *testing.T) {
	content := []byte("locale: en_US.UTF-8\n")
	h := NamedHost{Name: "node1", Host: Host{EffectiveConfig: content}}
	path, err := effectiveConfigPath(h)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	wantHash := hex.EncodeToString(sum[:])
	if !strings.HasSuffix(path, "node1-"+wantHash+".yaml") {
		t.Fatalf("config path %q is not content-addressed", path)
	}
	script := persistConfigScript(path, wantHash)
	if !strings.Contains(script, "expected_sha='"+wantHash+"'") {
		t.Fatal("remote persistence script does not verify the content-addressed digest")
	}
	for _, part := range []string{"check_dir /etc", "check_dir /etc/rootfiles", "check_dir \"$dir\"", "[ -L \"$target\" ]", "mktemp \"$dir/.rootfiles-config.XXXXXX\"", "chown root:root \"$tmp\"", "chmod 0600 \"$tmp\"", "actual_sha=$(sha256sum \"$tmp\" | awk '{print $1}')", "[ \"$actual_sha\" = \"$expected_sha\" ]", "cmp -s \"$tmp\" \"$target\"", "mv -f -- \"$tmp\" \"$target\""} {
		if !strings.Contains(script, part) {
			t.Errorf("persistence script lacks %q", part)
		}
	}
	if strings.Contains(script, "cat > \"$target\"") || strings.Contains(script, string(content)) {
		t.Fatal("remote command exposes or truncates the config target directly")
	}
}

func TestReadOnlySSHExitClassificationWithFakeSSH(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ssh")
	argsFile := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(argsFile) + "\nprintf '{\"modules\":[]}'\nexit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	h := NamedHost{Name: "n1", Host: Host{SSH: "alias"}, Sudo: "none"}
	r := SSHRunner{Binary: bin}.ReadOnly(context.Background(), h, "check")
	if r.State != "drift" || r.ExitCode != 2 || string(r.Remote) != "{\"modules\":[]}" {
		t.Fatalf("unexpected result: %#v", r)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(args); !strings.Contains(got, "--\nalias\n"+remoteBinary+" check -o json") {
		t.Fatalf("ssh did not receive a separated destination and fixed remote command: %q", got)
	}
	r = SSHRunner{Binary: bin}.ReadOnly(context.Background(), h, "doctor")
	if r.State != "findings" {
		t.Fatalf("unexpected doctor state: %#v", r)
	}
}

func TestStatusConfigErrorCannotTriggerMissingBinaryFallback(t *testing.T) {
	callsFile := filepath.Join(t.TempDir(), "calls")
	bin := writeFakeSSH(t, "#!/bin/sh\nprintf '%s\\n' called >> \"$FLEET_FAKE_SSH_CALLS\"\nprintf '%s\\n' '{\"config_error\":\"/etc/rootfiles/site.yaml: no such file\"}'\n")
	t.Setenv("FLEET_FAKE_SSH_CALLS", callsFile)
	h := NamedHost{Name: "n1", Host: Host{SSH: "alias"}, Sudo: "none"}
	got := SSHRunner{Binary: bin}.ReadOnly(context.Background(), h, "status")
	if got.State != "error" || !strings.Contains(got.Reason, "remote config error") {
		t.Fatalf("valid status config error was misclassified: %#v", got)
	}
	calls, err := os.ReadFile(callsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "called") != 1 {
		t.Fatalf("missing-binary probe ran after valid status JSON: %q", calls)
	}
}

func TestMutatingCommandsAcceptPlaintextSSHSuccess(t *testing.T) {
	bin := writeFakeSSH(t, `#!/bin/sh
case "$7" in *"check -o json") printf '{"modules":[]}'; exit 0 ;; esac
printf '%s\\n' "remote success"
exit 0
`)
	h := []NamedHost{{Name: "one", Host: Host{SSH: "one"}, Sudo: "none"}}
	updated, err := Update(context.Background(), SSHRunner{Binary: bin}, h, "v1.2.3", false)
	if err != nil || len(updated) != 1 || updated[0].State != "ok" {
		t.Fatalf("plaintext update success was rejected: results=%#v err=%v", updated, err)
	}
	scheduled, err := Schedule(context.Background(), SSHRunner{Binary: bin}, h, "enable", false)
	if err != nil || len(scheduled) != 1 || scheduled[0].State != "ok" {
		t.Fatalf("plaintext schedule success was rejected: results=%#v err=%v", scheduled, err)
	}
}

func TestUpdateStopsAfterFakeSSHFailure(t *testing.T) {
	callsFile := filepath.Join(t.TempDir(), "calls")
	bin := writeFakeSSH(t, "#!/bin/sh\n"+
		"case \"$7\" in *'check -o json') printf '{\"modules\":[]}'; exit 0 ;; esac\n"+
		"printf '%s\\n' \"$6\" >> "+shellQuote(callsFile)+"\n"+
		"[ \"$6\" != failing ] || { echo failed >&2; exit 23; }\n"+
		"echo updated\n")
	hosts := []NamedHost{{Name: "first", Host: Host{SSH: "first"}, Sudo: "none"}, {Name: "failure", Host: Host{SSH: "failing"}, Sudo: "none"}, {Name: "skipped", Host: Host{SSH: "last"}, Sudo: "none"}}
	results, err := Update(context.Background(), SSHRunner{Binary: bin}, hosts, "v1.2.3", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0].State != "ok" || results[1].State != "error" || results[2].State != "skipped" {
		t.Fatalf("unexpected stop-on-failure results: %#v", results)
	}
	calls, err := os.ReadFile(callsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(calls)); got != "first\nfailing" {
		t.Fatalf("unexpected SSH contacts: %q", got)
	}
}

func TestBootstrapMissingBinaryAndSkipsInstalledWithoutProvenance(t *testing.T) {
	bin := writeFakeSSH(t, `#!/bin/sh
remote="$7"
case "$remote" in
  *"status -o json"*) echo "/usr/local/bin/rootfiles: not found" >&2; exit 127 ;;
  *"/usr/local/bin/rootfiles --version"*) echo "host fake; OS Linux"; echo "/usr/local/bin/rootfiles: not found"; exit 127 ;;
  *"checksums.txt"*) echo "bootstrap complete"; exit 0 ;;
  *) echo "unexpected fake SSH command: $remote" >&2; exit 9 ;;
esac
`)
	hosts := []NamedHost{{Name: "one", Host: Host{SSH: "one"}, Sudo: "none"}}
	results, err := Bootstrap(context.Background(), SSHRunner{Binary: bin}, hosts, "v1.2.3", false)
	if err != nil || len(results) != 1 || results[0].State != "ok" {
		t.Fatalf("checksum bootstrap success failed: %#v err=%v", results, err)
	}

	callsFile := filepath.Join(t.TempDir(), "installed-calls")
	installedBin := writeFakeSSH(t, "#!/bin/sh\n"+
		"printf '%s\\n' \"$7\" >> "+shellQuote(callsFile)+"\n"+
		"printf '%s\\n' '{\"system\":{\"os\":\"ubuntu\",\"version\":\"24.04\"}}'\n")
	hosts[0].Host.ConfigFingerprint = "expected-but-not-recorded"
	results, err = Bootstrap(context.Background(), SSHRunner{Binary: installedBin}, hosts, "v1.2.3", false)
	if err != nil || len(results) != 1 || results[0].State != "ok" || !strings.Contains(results[0].Reason, "already installed") {
		t.Fatalf("installed binary without provenance should be skipped: %#v err=%v", results, err)
	}
	calls, err := os.ReadFile(callsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "checksums.txt") {
		t.Fatalf("bootstrap reinstalled a healthy existing binary: %s", calls)
	}
}

func writeFakeSSH(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSerialStopsAndMarksRemainingHostsSkipped(t *testing.T) {
	hosts := []NamedHost{{Name: "one"}, {Name: "two"}, {Name: "three"}}
	calls := 0
	got := serial(context.Background(), SSHRunner{}, hosts, "update", func(h NamedHost) Result {
		calls++
		state := "ok"
		if h.Name == "two" {
			state = "error"
		}
		return Result{Host: h.Name, State: state}
	})
	if calls != 2 || len(got) != 3 || got[2].State != "skipped" {
		t.Fatalf("calls=%d results=%#v", calls, got)
	}
}

func TestVersionValidation(t *testing.T) {
	for _, v := range []string{"v1.2.3", "v0.0.1"} {
		if err := ValidateVersion(v); err != nil {
			t.Errorf("%q rejected: %v", v, err)
		}
	}
	for _, v := range []string{"1.2.3", "v1.2", "v1.2.3;id", "latest"} {
		if err := ValidateVersion(v); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}

func TestMutationTimeoutBoundsAndSeparateDefaults(t *testing.T) {
	for _, duration := range []time.Duration{time.Nanosecond, DefaultMutationTimeout, MaxMutationTimeout} {
		if err := ValidateMutationTimeout(duration); err != nil {
			t.Errorf("timeout %s rejected: %v", duration, err)
		}
	}
	for _, duration := range []time.Duration{0, -time.Second, MaxMutationTimeout + time.Nanosecond} {
		if err := ValidateMutationTimeout(duration); err == nil {
			t.Errorf("timeout %s accepted", duration)
		}
	}
	r := SSHRunner{}
	if got := r.readTimeout(); got != 45*time.Second {
		t.Errorf("read-only timeout=%s, want 45s", got)
	}
	if got := r.mutationTimeout(); got != 15*time.Minute {
		t.Errorf("mutation timeout=%s, want 15m", got)
	}
	r.Timeout = 20 * time.Second
	r.MutationTimeout = 2 * time.Hour
	if got := r.readTimeout(); got != 20*time.Second {
		t.Errorf("read-only override=%s, want 20s", got)
	}
	if got := r.mutationTimeout(); got != 2*time.Hour {
		t.Errorf("mutation override=%s, want 2h", got)
	}
	r.Timeout = 2 * time.Hour
	if got := r.readTimeout(); got != 45*time.Second {
		t.Errorf("read timeout override escaped 45s cap: %s", got)
	}
	r.MutationTimeout = MaxMutationTimeout + time.Second
	if got := r.mutationTimeout(); got != DefaultMutationTimeout {
		t.Errorf("invalid mutation override was not bounded: %s", got)
	}
}

func TestShortReasonPrefersRemoteError(t *testing.T) {
	// The h100-1 shape: a long module progress list, then cobra's error.
	progress := strings.Repeat("  ✓ users: read-only check passed\n  → monitoring: Install Prometheus node exporter\n", 10)
	stderr := "Error: 1 module(s) failed: [monitoring: DCGM exporter prerequisites are missing: Docker NVIDIA runtime is unavailable]\n"
	if got := shortReason(progress, stderr); !strings.HasPrefix(got, "Error: 1 module(s) failed: [monitoring: DCGM") {
		t.Fatalf("reason should be the remote error line, got %q", got)
	}
	if got := shortReason(progress, "warning: something\nsudo: unable to resolve host\n"); got != "sudo: unable to resolve host" {
		t.Fatalf("reason without an Error: line should be the last stderr line, got %q", got)
	}
	if got := shortReason("only stdout\n", ""); got != "only stdout" {
		t.Fatalf("got %q", got)
	}
}

func TestSerialSkipUnreachableContinuesOnlyPastConnectionFailures(t *testing.T) {
	hosts := []NamedHost{{Name: "one"}, {Name: "down"}, {Name: "three"}, {Name: "broken"}, {Name: "five"}}
	results := map[string]Result{
		"down":   {State: "unreachable", Reason: reasonConnectFailed, ExitCode: 255},
		"broken": {State: "error", Reason: "Error: apply failed", ExitCode: 1},
	}
	run := func(skip bool) ([]Result, []string) {
		var called []string
		got := serial(context.Background(), SSHRunner{SkipUnreachable: skip}, hosts, "update", func(h NamedHost) Result {
			called = append(called, h.Name)
			r, ok := results[h.Name]
			if !ok {
				r = Result{State: "ok"}
			}
			r.Host = h.Name
			return r
		})
		return got, called
	}
	got, called := run(true)
	if strings.Join(called, ",") != "one,down,three,broken" || got[1].State != "skipped-unreachable" || got[3].State != "error" || got[4].State != "skipped" {
		t.Fatalf("with the flag: called=%v results=%#v", called, got)
	}
	got, called = run(false)
	if strings.Join(called, ",") != "one,down" || got[1].State != "unreachable" || got[2].State != "skipped" {
		t.Fatalf("without the flag: called=%v results=%#v", called, got)
	}
	timedOut := serial(context.Background(), SSHRunner{SkipUnreachable: true}, hosts[:2], "update", func(h NamedHost) Result {
		if h.Name == "one" {
			return Result{Host: h.Name, State: "unreachable", Reason: "ssh timed out"}
		}
		return Result{Host: h.Name, State: "ok"}
	})
	if timedOut[0].State != "unreachable" || timedOut[1].State != "skipped" {
		t.Fatalf("a timed-out mutation may have run and must stop the rollout: %#v", timedOut)
	}
}

func TestUpdateReportsApplyPendingFromPostUpdateCheck(t *testing.T) {
	bin := writeFakeSSH(t, `#!/bin/sh
case "$6:$7" in
  pending:*"check -o json") printf '{"modules":[{"name":"security","satisfied":true},{"name":"monitoring","satisfied":false}]}'; exit 2 ;;
  *:*"check -o json") printf '{"modules":[{"name":"security","satisfied":true}]}'; exit 0 ;;
  *:*" apply"*) echo "apply must never run" >&2; exit 9 ;;
esac
echo "Updated to v1.2.3"
`)
	hosts := []NamedHost{{Name: "pending", Host: Host{SSH: "pending"}, Sudo: "none"}, {Name: "clean", Host: Host{SSH: "clean"}, Sudo: "none"}}
	results, err := Update(context.Background(), SSHRunner{Binary: bin}, hosts, "v1.2.3", false)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != "apply-pending" || results[0].ExitCode != 2 || !strings.Contains(results[0].Reason, "monitoring") || strings.Contains(results[0].Reason, "security") {
		t.Fatalf("pending host: %#v", results[0])
	}
	if results[1].State != "ok" {
		t.Fatalf("apply-pending must not stop the rollout, and a clean host stays ok: %#v", results[1])
	}
}

func TestUpdateKeepsFailedPostUpdateCheckEvidence(t *testing.T) {
	bin := writeFakeSSH(t, `#!/bin/sh
case "$7" in *"check -o json") echo "Error: loading config: bad key" >&2; exit 1 ;; esac
echo "Updated to v1.2.3"
`)
	hosts := []NamedHost{{Name: "one", Host: Host{SSH: "one"}, Sudo: "none"}}
	results, _ := Update(context.Background(), SSHRunner{Binary: bin}, hosts, "v1.2.3", false)
	r := results[0]
	if r.State != "error" || r.ExitCode != 1 || !strings.Contains(r.Output, "Updated to v1.2.3") || !strings.Contains(r.Output, "bad key") || !strings.Contains(r.Reason, "bad key") {
		t.Fatalf("failed check should carry its exit code and output: %#v", r)
	}
}

func TestFleetApplyRunsWithDefaultUmask(t *testing.T) {
	script := persistAndApplyScript("/etc/rootfiles/fleet/n1-abc.yaml", "abc")
	store := strings.Index(script, "umask 077")
	restore := strings.Index(script, "umask 022; exec "+remoteBinary+" apply")
	if store < 0 || restore < store {
		t.Fatalf("apply must run with umask 022 after the 077 config store:\n%s", script)
	}
}
