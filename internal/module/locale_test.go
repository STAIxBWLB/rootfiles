package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

func TestLocaleModule_Name(t *testing.T) {
	if n := NewLocaleModule().Name(); n != "locale" {
		t.Errorf("Name() = %q, want locale", n)
	}
}

func TestLocaleModule_CheckEmptyConfigIsSatisfied(t *testing.T) {
	rc := newDryRunRC(t) // config has empty Locale and Timezone
	result, err := NewLocaleModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Satisfied {
		t.Errorf("Check with empty locale/timezone should be satisfied, got changes: %+v", result.Changes)
	}
}

// fakeLocaleFS redirects locale/timezone paths into a temp dir.
func fakeLocaleFS(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	zi := filepath.Join(dir, "zoneinfo")
	for _, tz := range []string{"UTC", "Asia/Seoul"} {
		os.MkdirAll(filepath.Dir(filepath.Join(zi, tz)), 0755)
		os.WriteFile(filepath.Join(zi, tz), []byte("TZif"), 0644)
	}
	saved := []string{defaultLocalePath, rockyLocalePath, timezonePath, localtimePath, zoneinfoDir, timedatectlBin}
	// Never touch the host clock settings: force the no-systemd fallback.
	timedatectlBin = filepath.Join(dir, "no-timedatectl")
	defaultLocalePath = filepath.Join(dir, "locale")
	rockyLocalePath = filepath.Join(dir, "locale.conf")
	timezonePath = filepath.Join(dir, "timezone")
	localtimePath = filepath.Join(dir, "localtime")
	zoneinfoDir = zi
	t.Cleanup(func() {
		defaultLocalePath, rockyLocalePath, timezonePath, localtimePath, zoneinfoDir, timedatectlBin = saved[0], saved[1], saved[2], saved[3], saved[4], saved[5]
	})
	return dir
}

func TestLocaleModule_RockyUsesNativeLocaleAndTimezonePaths(t *testing.T) {
	dir := fakeLocaleFS(t)
	os.WriteFile(rockyLocalePath, []byte("LANG=C\n"), 0644)
	os.Symlink(filepath.Join(dir, "zoneinfo", "UTC"), localtimePath)
	fakeCommandOutput(t, map[string]string{"localectl": "en_US.utf8\nC\n"})
	rc := newRealRC(t)
	rc.Config.System = &config.SystemInfo{OS: "rocky", Version: "9.5"}
	rc.Config.Locale = "en_US.UTF-8"
	rc.Config.Timezone = "Asia/Seoul"
	result, err := NewLocaleModule().Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Changed || currentLocale(rc) != "en_US.UTF-8" || currentTimezone(rc) != "Asia/Seoul" {
		t.Fatalf("Rocky locale result=%+v locale=%q timezone=%q", result, currentLocale(rc), currentTimezone(rc))
	}
	if _, err := os.Stat(timezonePath); !os.IsNotExist(err) {
		t.Errorf("Rocky apply should not create Debian timezone file, stat error=%v", err)
	}
	if _, err := os.Stat(defaultLocalePath); !os.IsNotExist(err) {
		t.Errorf("Rocky apply should not create Debian locale file, stat error=%v", err)
	}
}

type localePackageManager struct {
	installed bool
	logPath   string
	marker    string
}

func (p *localePackageManager) Update(context.Context) error {
	return appendRockyCall(p.logPath, "dnf makecache")
}
func (p *localePackageManager) Install(_ context.Context, packages []string) error {
	if err := appendRockyCall(p.logPath, "dnf install "+strings.Join(packages, " ")); err != nil {
		return err
	}
	p.installed = true
	return os.WriteFile(p.marker, []byte("installed"), 0600)
}
func (p *localePackageManager) IsInstalled(pkg string) bool {
	return pkg == "glibc-langpack-en" && p.installed
}
func (p *localePackageManager) Installed(packages []string) map[string]bool {
	installed := make(map[string]bool, len(packages))
	for _, pkg := range packages {
		installed[pkg] = p.IsInstalled(pkg)
	}
	return installed
}
func (*localePackageManager) AddKeyring(context.Context, string, string) error { return nil }
func (*localePackageManager) AddSourceList(context.Context, string, string) error {
	return nil
}

func TestRockyLocaleCheckReportsMissingNativeLangpack(t *testing.T) {
	fakeLocaleFS(t)
	fakeCommandOutput(t, map[string]string{"localectl": "C.utf8\n", "rpm": ""})
	rc := newDryRunRC(t)
	rc.Config.System = &config.SystemInfo{OS: "rocky", Version: "9.5"}
	rc.Config.Locale = "en_US.UTF-8"
	rc.APT = exec.NewRPM(rc.Runner)
	result, err := NewLocaleModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	var installPending bool
	for _, change := range result.Changes {
		if change.Command == "dnf install glibc-langpack-en" {
			installPending = true
		}
	}
	if !installPending {
		t.Fatalf("Check did not report missing native langpack: %+v", result.Changes)
	}
}

func TestRockyLocaleApplyInstallsLangpackThenRechecksBeforeWriting(t *testing.T) {
	dir := fakeLocaleFS(t)
	marker := filepath.Join(dir, "langpack-installed")
	localectlPath := filepath.Join(dir, "localectl")
	localectl := "#!/bin/sh\nif test -f '" + marker + "'; then echo en_US.utf8; else echo C.utf8; fi\n"
	if err := os.WriteFile(localectlPath, []byte(localectl), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	logPath := filepath.Join(dir, "package-calls")
	pm := &localePackageManager{logPath: logPath, marker: marker}
	rc := newRealRC(t)
	rc.Config.System = &config.SystemInfo{OS: "rocky", Version: "9.5"}
	rc.Config.Locale = "en_US.UTF-8"
	rc.APT = pm
	result, err := NewLocaleModule().Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Changed || currentLocale(rc) != "en_US.UTF-8" || !pm.installed {
		t.Fatalf("Apply result=%+v locale=%q installed=%t", result, currentLocale(rc), pm.installed)
	}
	data, err := os.ReadFile(rockyLocalePath)
	if err != nil || !strings.Contains(string(data), "LANG=en_US.UTF-8") {
		t.Fatalf("Rocky locale config = %q, %v", data, err)
	}
	calls, _ := os.ReadFile(logPath)
	if string(calls) != "dnf makecache\ndnf install glibc-langpack-en\n" {
		t.Fatalf("unexpected language-pack operations: %q", calls)
	}
}

func TestRockyLocaleUnknownDoesNotWriteConfig(t *testing.T) {
	dir := fakeLocaleFS(t)
	marker := filepath.Join(dir, "langpack-installed")
	localectl := "#!/bin/sh\necho C.utf8\n"
	if err := os.WriteFile(filepath.Join(dir, "localectl"), []byte(localectl), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	os.WriteFile(rockyLocalePath, []byte("LANG=C\n"), 0644)
	pm := &localePackageManager{logPath: filepath.Join(dir, "package-calls"), marker: marker}
	rc := newRealRC(t)
	rc.Config.System = &config.SystemInfo{OS: "rocky", Version: "9.5"}
	rc.Config.Locale = "en_US.UTF-8"
	rc.APT = pm
	if _, err := NewLocaleModule().Apply(context.Background(), rc); err == nil {
		t.Fatal("Apply accepted locale absent after installing its language pack")
	}
	data, err := os.ReadFile(rockyLocalePath)
	if err != nil || string(data) != "LANG=C\n" {
		t.Fatalf("locale config changed for unavailable locale: %q, %v", data, err)
	}
}

func TestRockyLocaleLanguagePackMapping(t *testing.T) {
	cases := map[string]string{
		"en_US.UTF-8":   "glibc-langpack-en",
		"zh_Hant.UTF-8": "glibc-langpack-zh",
		"C":             "",
		"C.UTF8":        "",
		"C.UTF-8":       "",
		"POSIX":         "",
	}
	for locale, want := range cases {
		got, err := rockyLocaleLanguagePack(locale)
		if err != nil || got != want {
			t.Errorf("rockyLocaleLanguagePack(%q) = %q, %v; want %q", locale, got, err, want)
		}
	}
	for _, locale := range []string{"a_US.UTF-8", "../x", "éé.UTF-8", "en\\nBAD", "en\nBAD"} {
		if got, err := rockyLocaleLanguagePack(locale); err == nil || got != "" {
			t.Errorf("rockyLocaleLanguagePack(%q) = %q, %v; want invalid", locale, got, err)
		}
	}
}

func TestLocaleListedRecognizesBuiltInLocaleAliases(t *testing.T) {
	for _, locale := range []string{"C", "C.UTF8", "C.UTF-8", "POSIX"} {
		if !localeListed("C.utf8\n", locale) {
			t.Errorf("C.utf8 should satisfy built-in locale %q", locale)
		}
	}
}

func TestLocaleModule_TimezoneFromLocaltimeSymlink(t *testing.T) {
	dir := fakeLocaleFS(t)
	// No /etc/timezone (as on stock Ubuntu 24.04), only the symlink.
	os.Symlink(filepath.Join(dir, "zoneinfo", "Asia/Seoul"), localtimePath)
	os.WriteFile(defaultLocalePath, []byte("LANG=en_US.UTF-8\n"), 0644)

	rc := newDryRunRC(t)
	rc.Config.Locale = "en_US.UTF-8"
	rc.Config.Timezone = "Asia/Seoul"
	res, err := NewLocaleModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Satisfied {
		t.Errorf("Check should be satisfied, got %+v", res.Changes)
	}
}

func TestLocaleModule_ApplyWithoutSystemd(t *testing.T) {
	dir := fakeLocaleFS(t)
	os.WriteFile(defaultLocalePath, []byte("LANG=C\nLC_ALL=C\nLANGUAGE=en\n"), 0644)
	os.Symlink(filepath.Join(dir, "zoneinfo", "UTC"), localtimePath)

	rc := newRealRC(t)
	rc.Config.Timezone = "Asia/Seoul"
	res, err := NewLocaleModule().Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Changed {
		t.Error("Apply should report a timezone change")
	}
	if got := currentTimezone(rc); got != "Asia/Seoul" {
		t.Errorf("timezone = %q, want Asia/Seoul", got)
	}

	res, err = NewLocaleModule().Apply(context.Background(), rc)
	if err != nil || res.Changed {
		t.Errorf("second Apply should be a no-op, got %+v, %v", res, err)
	}
}

func TestLocaleModule_UnknownTimezone(t *testing.T) {
	fakeLocaleFS(t)
	rc := newRealRC(t)
	// Never install packages on the test host.
	rc.APT = exec.NewAPT(newDryRunRC(t).Runner)
	rc.Config.Timezone = "Mars/Olympus"
	if _, err := NewLocaleModule().Apply(context.Background(), rc); err == nil {
		t.Error("Apply should reject an unknown timezone")
	}
}

func TestSetDefaultLocale(t *testing.T) {
	fakeLocaleFS(t)
	os.WriteFile(defaultLocalePath, []byte("LANG=C\nLC_ALL=C\nLANGUAGE=en\n"), 0644)
	rc := newRealRC(t)
	if err := setDefaultLocale(rc, "ko_KR.UTF-8"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(defaultLocalePath)
	s := string(data)
	if !strings.Contains(s, "LANG=ko_KR.UTF-8") || strings.Contains(s, "LC_ALL") || !strings.Contains(s, "LANGUAGE=en") {
		t.Errorf("unexpected locale file:\n%s", s)
	}
	if currentLocale(rc) != "ko_KR.UTF-8" {
		t.Errorf("currentLocale = %q", currentLocale(rc))
	}
}
