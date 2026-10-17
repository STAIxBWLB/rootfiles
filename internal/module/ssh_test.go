package module

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestSSHModule_Name(t *testing.T) {
	if n := NewSSHModule().Name(); n != "ssh" {
		t.Errorf("Name() = %q, want ssh", n)
	}
}

func TestSSHModule_BuildConfig(t *testing.T) {
	m := NewSSHModule()
	cases := []struct {
		name    string
		cfg     config.SSHConfig
		must    []string
		mustNot []string
	}{
		{
			name: "root login disabled, password disabled, custom port",
			cfg:  config.SSHConfig{DisableRootLogin: true, DisablePasswordAuth: true, Port: 2222},
			must: []string{"PermitRootLogin no", "PasswordAuthentication no", "Port 2222"},
		},
		{
			name:    "all permissive",
			cfg:     config.SSHConfig{},
			mustNot: []string{"PermitRootLogin", "PasswordAuthentication", "Port"},
		},
		{
			name:    "password exceptions ignored while password auth is on",
			cfg:     config.SSHConfig{PasswordAuthUsers: []string{"bob"}},
			mustNot: []string{"Match"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := m.buildConfig(c.cfg)
			for _, s := range c.must {
				if !strings.Contains(got, s) {
					t.Errorf("config missing %q\ngot:\n%s", s, got)
				}
			}
			for _, s := range c.mustNot {
				if strings.Contains(got, s) {
					t.Errorf("config should not contain %q\ngot:\n%s", s, got)
				}
			}
		})
	}
}

func TestSSHModule_ApplyDryRun(t *testing.T) {
	rc := newDryRunRC(t)
	rc.Config.SSH = config.SSHConfig{DisableRootLogin: true, Port: 22}
	result, err := NewSSHModule().Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Changed {
		t.Error("Apply should always mark Changed=true (config is rewritten)")
	}
}

// fakePasswd points the lockout guard at a temp passwd file whose accounts
// live under dir. keyed lists accounts that get a non-empty authorized_keys.
// Every password starts locked; withPasswords gives some a usable one.
func fakePasswd(t *testing.T, keyed ...string) {
	t.Helper()
	dir := t.TempDir()
	accounts := []struct {
		name string
		uid  int
	}{{"root", 0}, {"daemon", 1}, {"alice", 1000}, {"bob", 1001}}
	var b strings.Builder
	for _, a := range accounts {
		home := filepath.Join(dir, a.name)
		os.MkdirAll(filepath.Join(home, ".ssh"), 0700)
		fmt.Fprintf(&b, "%s:x:%d:%d::%s:/bin/bash\n", a.name, a.uid, a.uid, home)
		for _, k := range keyed {
			if k == a.name {
				os.WriteFile(filepath.Join(home, ".ssh", "authorized_keys"), []byte("ssh-ed25519 AAAA test\n"), 0600)
			}
		}
	}
	p := filepath.Join(dir, "passwd")
	os.WriteFile(p, []byte(b.String()), 0644)
	old, oldShadow := passwdPath, shadowPath
	passwdPath, shadowPath = p, filepath.Join(dir, "shadow")
	t.Cleanup(func() { passwdPath, shadowPath = old, oldShadow })
	withPasswords(t)
}

// withPasswords rewrites the fake shadow so only names have a usable hash.
func withPasswords(t *testing.T, names ...string) {
	t.Helper()
	var b strings.Builder
	for _, n := range []string{"root", "daemon", "alice", "bob"} {
		hash := "!"
		for _, w := range names {
			if w == n {
				hash = "$6$salt$hash"
			}
		}
		fmt.Fprintf(&b, "%s:%s:19000:0:99999:7:::\n", n, hash)
	}
	if err := os.WriteFile(shadowPath, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSSHModule_LockoutGuard(t *testing.T) {
	cases := []struct {
		name    string
		cfg     config.SSHConfig
		keyed   []string
		pending []string
		wantErr bool
	}{
		{"password auth kept", config.SSHConfig{DisablePasswordAuth: false, DisableRootLogin: true}, nil, nil, false},
		{"no keys anywhere", config.SSHConfig{DisablePasswordAuth: true}, nil, nil, true},
		{"root key, root login allowed", config.SSHConfig{DisablePasswordAuth: true}, []string{"root"}, nil, false},
		{"root key only, root login disabled", config.SSHConfig{DisablePasswordAuth: true, DisableRootLogin: true}, []string{"root"}, nil, true},
		{"user key", config.SSHConfig{DisablePasswordAuth: true, DisableRootLogin: true}, []string{"alice"}, nil, false},
		{"system account key ignored", config.SSHConfig{DisablePasswordAuth: true, DisableRootLogin: true}, []string{"daemon"}, nil, true},
		{"declared account with key", config.SSHConfig{DisablePasswordAuth: true, DisableRootLogin: true}, nil, []string{"bob"}, false},
	}
	pwCases := []struct {
		name    string
		cfg     config.SSHConfig
		pending []string
		wantErr bool
	}{
		{"password-only user stranded", config.SSHConfig{DisablePasswordAuth: true}, nil, true},
		{"password-only user excepted", config.SSHConfig{DisablePasswordAuth: true, PasswordAuthUsers: []string{"bob"}}, nil, false},
		{"password-only user gets a declared key", config.SSHConfig{DisablePasswordAuth: true}, []string{"bob"}, false},
		{"password auth kept", config.SSHConfig{}, nil, false},
	}
	for _, c := range pwCases {
		t.Run(c.name, func(t *testing.T) {
			fakePasswd(t, "alice")
			withPasswords(t, "alice", "bob")
			rc := newDryRunRC(t)
			rc.Config.SSH = c.cfg
			rc.Config.Modules.Users.Enabled = true
			for _, n := range c.pending {
				rc.Config.Users.Accounts = append(rc.Config.Users.Accounts, config.AccountConfig{Name: n, SSHPubkeys: []string{"ssh-ed25519 AAAA k"}})
			}
			err := NewSSHModule().lockoutGuard(rc)
			if (err != nil) != c.wantErr {
				t.Errorf("lockoutGuard() err = %v, wantErr %v", err, c.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "bob") {
				t.Errorf("error should name the stranded account: %v", err)
			}
		})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakePasswd(t, c.keyed...)
			rc := newDryRunRC(t)
			rc.Config.SSH = c.cfg
			rc.Config.Modules.Users.Enabled = true
			for _, n := range c.pending {
				rc.Config.Users.Accounts = append(rc.Config.Users.Accounts, config.AccountConfig{Name: n, SSHPubkeys: []string{"ssh-ed25519 AAAA k"}})
			}
			err := NewSSHModule().lockoutGuard(rc)
			if (err != nil) != c.wantErr {
				t.Errorf("lockoutGuard() err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestSSHModule_ApplyRefusesLockoutWithoutForce(t *testing.T) {
	fakePasswd(t)
	dropIn := filepath.Join(t.TempDir(), "00-rootfiles.conf")
	old := sshdDropInPath
	sshdDropInPath = dropIn
	t.Cleanup(func() { sshdDropInPath = old })

	rc := newDryRunRC(t)
	rc.Config.SSH = config.SSHConfig{DisablePasswordAuth: true, DisableRootLogin: true}
	if _, err := NewSSHModule().Apply(context.Background(), rc); err == nil {
		t.Fatal("Apply should refuse to disable password auth with no key login")
	}

	check, _ := NewSSHModule().Check(context.Background(), rc)
	if check.Satisfied || !strings.Contains(check.Changes[0].Description, "blocked") {
		t.Errorf("Check should flag the blocked change, got %+v", check.Changes)
	}

	rc.Force = true
	if _, err := NewSSHModule().Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply with Force: %v", err)
	}
}

func TestSSHModule_ApplyNoopWhenUnchanged(t *testing.T) {
	dropIn := filepath.Join(t.TempDir(), "00-rootfiles.conf")
	old := sshdDropInPath
	sshdDropInPath = dropIn
	t.Cleanup(func() { sshdDropInPath = old })

	m := NewSSHModule()
	cfg := config.SSHConfig{DisableRootLogin: true}
	os.WriteFile(dropIn, []byte(m.buildConfig(cfg)), 0644)

	rc := newDryRunRC(t)
	rc.Config.SSH = cfg
	res, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Changed {
		t.Error("Apply should be a no-op when the drop-in already matches")
	}
}

func TestPortLine(t *testing.T) {
	if portLine("# x\nPermitRootLogin no\nPort 2222\n") != "Port 2222" {
		t.Error("portLine should find Port directive")
	}
	if portLine("# x\n") != "" {
		t.Error("portLine should be empty without Port")
	}
}

func sshCfg(noPassword bool, maxTries int) config.SSHConfig {
	return config.SSHConfig{DisablePasswordAuth: noPassword, MaxAuthTries: maxTries}
}

func TestSSHModule_BuildConfigPasswordExceptionsLast(t *testing.T) {
	got := NewSSHModule().buildConfig(config.SSHConfig{DisablePasswordAuth: true, Port: 2222, PasswordAuthUsers: []string{"bob", "carol"}})
	match := strings.Index(got, "Match User bob,carol\n")
	if match < 0 {
		t.Fatalf("missing Match block:\n%s", got)
	}
	if strings.Index(got, "PasswordAuthentication no") > match || strings.Index(got, "Port 2222") > match {
		t.Errorf("global directives must precede the Match block:\n%s", got)
	}
	tail := got[match:]
	for _, s := range []string{"\tPasswordAuthentication yes\n", "\tKbdInteractiveAuthentication yes\n"} {
		if !strings.Contains(tail, s) {
			t.Errorf("Match block missing %q:\n%s", s, tail)
		}
	}
}

func TestPasswordOnlyAccounts(t *testing.T) {
	fakePasswd(t, "alice")
	withPasswords(t, "alice", "bob")
	got, known := passwordOnlyAccounts()
	if !known || strings.Join(got, ",") != "bob" {
		t.Errorf("passwordOnlyAccounts() = %v, %v; want [bob], true", got, known)
	}

	withPasswords(t, "alice")
	if got, _ := passwordOnlyAccounts(); len(got) != 0 {
		t.Errorf("locked password should not count, got %v", got)
	}

	// Unreadable shadow (not root): every keyless account is listed.
	shadowPath = filepath.Join(t.TempDir(), "absent")
	if got, known := passwordOnlyAccounts(); known || strings.Join(got, ",") != "bob" {
		t.Errorf("unknown shadow: got %v, %v; want [bob], false", got, known)
	}
}

func TestSSHModule_LockoutGuardUnreadableShadow(t *testing.T) {
	fakePasswd(t, "alice")
	shadowPath = filepath.Join(t.TempDir(), "absent")
	rc := newDryRunRC(t)
	rc.Config.SSH = config.SSHConfig{DisablePasswordAuth: true}
	err := NewSSHModule().lockoutGuard(rc)
	if err == nil || !strings.Contains(err.Error(), "shadow") || !strings.Contains(err.Error(), "bob") {
		t.Errorf("unreadable shadow: want a conservative error naming bob, got %v", err)
	}
}

// A declared account does not count as a key login when the users module
// will refuse the home base, because it is never created.
func TestSSHModule_LockoutGuardIgnoresAccountsOnRefusedHomeBase(t *testing.T) {
	fakePasswd(t)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	rc := newDryRunRC(t)
	rc.Config.SSH = config.SSHConfig{DisablePasswordAuth: true, DisableRootLogin: true}
	rc.Config.Modules.Users.Enabled = true
	rc.Config.Users = config.UsersConfig{
		HomeBase: filepath.Join(parent, "home"),
		Accounts: []config.AccountConfig{{Name: "bob", SSHPubkeys: []string{"ssh-ed25519 AAAA k"}}},
	}
	if err := NewSSHModule().lockoutGuard(rc); err == nil {
		t.Error("lockoutGuard counted an account the users module will not create")
	}
}

// stubModule is a module whose Check and Apply only report what it is told,
// and that can probe the run context while checked.
type stubModule struct {
	name      string
	checkErr  error
	applyErr  error
	satisfied bool
	probe     func(*RunContext)
}

func (s stubModule) Name() string { return s.name }
func (s stubModule) Check(_ context.Context, rc *RunContext) (*CheckResult, error) {
	if s.probe != nil {
		s.probe(rc)
	}
	if s.checkErr != nil {
		return nil, s.checkErr
	}
	return &CheckResult{Satisfied: s.satisfied, Changes: []Change{{Description: "stub"}}}, nil
}
func (s stubModule) Apply(context.Context, *RunContext) (*ApplyResult, error) {
	return &ApplyResult{}, s.applyErr
}

// RunAll and CheckAll tell later modules whether users is in the run and
// has not failed so far.
func TestRunAllTracksModuleOutcomes(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name  string
		users []Module
		dry   bool
		want  bool
	}{
		{"users succeeds", []Module{stubModule{name: "users", satisfied: true}}, false, true},
		{"users pending in dry run", []Module{stubModule{name: "users"}}, true, true},
		{"users check fails", []Module{stubModule{name: "users", checkErr: boom}}, false, false},
		{"users apply fails", []Module{stubModule{name: "users", applyErr: boom}}, false, false},
		{"users not in the run", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc := newDryRunRC(t)
			rc.DryRun = tc.dry
			rc.Config.Modules.Users.Enabled = true
			var got bool
			probe := stubModule{name: "ssh", satisfied: true, probe: func(rc *RunContext) { got = rc.moduleSucceeding("users") }}
			_, _ = RunAll(context.Background(), append(tc.users, probe), rc)
			if got != tc.want {
				t.Errorf("RunAll: users succeeding = %v, want %v", got, tc.want)
			}
			if rc.run != nil {
				t.Error("RunAll left its run record behind")
			}
		})
	}
	rc := newDryRunRC(t)
	var got bool
	_, _ = CheckAll(context.Background(), []Module{stubModule{name: "ssh", probe: func(rc *RunContext) { got = rc.moduleSucceeding("users") }}}, rc)
	if got {
		t.Error("CheckAll: users counted although not in the run")
	}
}

// #34 AC1-AC3: declared accounts count for the lockout guard only while
// the users module is in the run and has not failed.
func TestSSHModule_LockoutGuardFollowsUsersOutcome(t *testing.T) {
	cases := []struct {
		name    string
		run     map[string]bool
		wantErr bool
	}{
		{"outside a run, users enabled", nil, false},
		{"users succeeding", map[string]bool{"users": false, "ssh": false}, false},
		{"users failed", map[string]bool{"users": true, "ssh": false}, true},
		{"users not in the run", map[string]bool{"ssh": false}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakePasswd(t)
			rc := newDryRunRC(t)
			rc.Config.SSH = config.SSHConfig{DisablePasswordAuth: true, DisableRootLogin: true}
			rc.Config.Modules.Users.Enabled = true
			rc.Config.Users.Accounts = []config.AccountConfig{{Name: "bob", SSHPubkeys: []string{"ssh-ed25519 AAAA k"}}}
			rc.run = tc.run
			err := NewSSHModule().lockoutGuard(rc)
			if (err != nil) != tc.wantErr {
				t.Fatalf("lockoutGuard() err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "declared accounts bob") {
				t.Errorf("error should name the declared accounts: %v", err)
			}
		})
	}
}
