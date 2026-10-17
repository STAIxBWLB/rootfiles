package module

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

type LocaleModule struct{}

func NewLocaleModule() *LocaleModule { return &LocaleModule{} }
func (m *LocaleModule) Name() string { return "locale" }

// Paths are overridable in tests.
var (
	defaultLocalePath = "/etc/default/locale"
	rockyLocalePath   = "/etc/locale.conf"
	timezonePath      = "/etc/timezone"
	localtimePath     = "/etc/localtime"
	zoneinfoDir       = "/usr/share/zoneinfo"
	timedatectlBin    = "timedatectl"
)

func localeConfigPath(rc *RunContext) string {
	if rc.Config != nil && config.IsRocky(rc.Config.System) {
		return rockyLocalePath
	}
	return defaultLocalePath
}

// currentLocale returns the LANG value from the distro's locale config.
func currentLocale(rc *RunContext) string {
	data, _ := rc.Runner.ReadFile(localeConfigPath(rc))
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "LANG="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// currentTimezone resolves the system timezone from the /etc/localtime
// symlink (authoritative on systemd hosts, where /etc/timezone may be
// missing), falling back to /etc/timezone.
func currentTimezone(rc *RunContext) string {
	if target, err := os.Readlink(localtimePath); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			return target[i+len("zoneinfo/"):]
		}
	}
	data, _ := rc.Runner.ReadFile(timezonePath)
	return strings.TrimSpace(string(data))
}

func (m *LocaleModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	var changes []Change
	cfg := rc.Config

	if config.IsRocky(cfg.System) && cfg.Locale != "" {
		langpack, err := rockyLocaleLanguagePack(cfg.Locale)
		if err != nil {
			return nil, err
		}
		res, err := rc.Runner.Query(ctx, "localectl", "list-locales")
		if err != nil {
			return nil, fmt.Errorf("listing available Rocky locales: %w", err)
		}
		if !localeListed(res.Stdout, cfg.Locale) {
			if langpack == "" {
				return nil, fmt.Errorf("built-in locale %q is not available on this Rocky host", cfg.Locale)
			}
			if rc.APT.IsInstalled(langpack) {
				return nil, fmt.Errorf("locale %q is not provided by installed package %s", cfg.Locale, langpack)
			}
			changes = append(changes, Change{
				Description: fmt.Sprintf("Install Rocky locale language pack %s", langpack),
				Command:     packageInstallCommand(rc, []string{langpack}),
			})
		}
	}

	if cfg.Locale != "" && currentLocale(rc) != cfg.Locale {
		command := fmt.Sprintf("locale-gen %s && update-locale LANG=%s", cfg.Locale, cfg.Locale)
		if config.IsRocky(cfg.System) {
			command = "write " + rockyLocalePath
		}
		changes = append(changes, Change{
			Description: fmt.Sprintf("Set locale to %s", cfg.Locale),
			Command:     command,
		})
	}

	if cfg.Timezone != "" && currentTimezone(rc) != cfg.Timezone {
		changes = append(changes, Change{
			Description: fmt.Sprintf("Set timezone to %s", cfg.Timezone),
			Command:     fmt.Sprintf("timedatectl set-timezone %s", cfg.Timezone),
		})
	}

	return &CheckResult{
		Satisfied: len(changes) == 0,
		Changes:   changes,
	}, nil
}

func (m *LocaleModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	cfg := rc.Config
	var messages, warnings []string
	changed := false

	isRocky := config.IsRocky(rc.Config.System)
	if isRocky && cfg.Locale != "" {
		langpack, err := rockyLocaleLanguagePack(cfg.Locale)
		if err != nil {
			return nil, err
		}
		res, err := rc.Runner.Query(ctx, "localectl", "list-locales")
		if err != nil {
			return nil, fmt.Errorf("listing available Rocky locales: %w", err)
		}
		if !localeListed(res.Stdout, cfg.Locale) {
			if langpack == "" {
				return nil, fmt.Errorf("built-in locale %q is not available on this Rocky host", cfg.Locale)
			}
			if rc.APT.IsInstalled(langpack) {
				return nil, fmt.Errorf("locale %q is not provided by installed package %s", cfg.Locale, langpack)
			}
			if err := rc.APT.Update(ctx); err != nil {
				return nil, fmt.Errorf("dnf makecache: %w", err)
			}
			if err := rc.APT.Install(ctx, []string{langpack}); err != nil {
				return nil, fmt.Errorf("installing locale language pack %s: %w", langpack, err)
			}
			messages = append(messages, "installed "+langpack)
			changed = true
			res, err = rc.Runner.Query(ctx, "localectl", "list-locales")
			if err != nil || !localeListed(res.Stdout, cfg.Locale) {
				return nil, fmt.Errorf("locale %q is still unavailable after installing %s", cfg.Locale, langpack)
			}
		}
		if currentLocale(rc) != cfg.Locale {
			if err := setLocaleConfig(rc, localeConfigPath(rc), cfg.Locale); err != nil {
				return nil, fmt.Errorf("writing locale: %w", err)
			}
			messages = append(messages, fmt.Sprintf("locale set to %s", cfg.Locale))
			changed = true
		}
	} else if cfg.Locale != "" && currentLocale(rc) != cfg.Locale {
		// locale-gen comes from the locales package.
		if !rc.Runner.CommandExists("locale-gen") {
			if err := rc.APT.Update(ctx); err != nil {
				return nil, fmt.Errorf("apt update: %w", err)
			}
			if err := rc.APT.Install(ctx, []string{"locales"}); err != nil {
				return nil, fmt.Errorf("installing locales: %w", err)
			}
		}
		if _, err := rc.Runner.Run(ctx, "locale-gen", cfg.Locale); err != nil {
			warnings = append(warnings, fmt.Sprintf("locale-gen %s: %s", cfg.Locale, firstLine(err.Error())))
		}
		// Only LANG is set: forcing LC_ALL system-wide overrides every
		// per-user locale choice.
		if err := setLocaleConfig(rc, localeConfigPath(rc), cfg.Locale); err != nil {
			return nil, fmt.Errorf("writing locale: %w", err)
		}
		messages = append(messages, fmt.Sprintf("locale set to %s", cfg.Locale))
		changed = true
	}

	if cfg.Timezone != "" && currentTimezone(rc) != cfg.Timezone {
		zoneFile := filepath.Join(zoneinfoDir, cfg.Timezone)
		if !rc.DryRun && !rc.Runner.FileExists(zoneFile) && !rc.APT.IsInstalled("tzdata") {
			// Minimal images ship without zone data.
			if err := rc.APT.Update(ctx); err != nil {
				return nil, fmt.Errorf("apt update: %w", err)
			}
			if err := rc.APT.Install(ctx, []string{"tzdata"}); err != nil {
				return nil, fmt.Errorf("installing tzdata: %w", err)
			}
		}
		if !rc.DryRun && !rc.Runner.FileExists(zoneFile) {
			return nil, fmt.Errorf("unknown timezone %q (not in %s)", cfg.Timezone, zoneinfoDir)
		}
		if _, err := rc.Runner.Run(ctx, timedatectlBin, "set-timezone", cfg.Timezone); err != nil {
			// No systemd (containers): set the symlink and /etc/timezone directly.
			if err := rc.Runner.Remove(localtimePath); err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("replacing %s: %w", localtimePath, err)
			}
			if err := rc.Runner.Symlink(filepath.Join(zoneinfoDir, cfg.Timezone), localtimePath); err != nil {
				return nil, fmt.Errorf("linking %s: %w", localtimePath, err)
			}
		}
		if !config.IsRocky(rc.Config.System) {
			if err := rc.Runner.WriteFile(timezonePath, []byte(cfg.Timezone+"\n"), 0644); err != nil {
				return nil, fmt.Errorf("writing %s: %w", timezonePath, err)
			}
		}
		messages = append(messages, fmt.Sprintf("timezone set to %s", cfg.Timezone))
		changed = true
	}

	return &ApplyResult{Changed: changed, Messages: messages, Warnings: warnings}, nil
}

// setDefaultLocale sets LANG in /etc/default/locale, keeping other lines
// but dropping a stale LC_ALL that earlier versions wrote.
func setDefaultLocale(rc *RunContext, locale string) error {
	return setLocaleConfig(rc, defaultLocalePath, locale)
}

func setLocaleConfig(rc *RunContext, path, locale string) error {
	data, _ := rc.Runner.ReadFile(path)
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "LANG=") || strings.HasPrefix(t, "LC_ALL=") {
			continue
		}
		lines = append(lines, line)
	}
	lines = append([]string{"LANG=" + locale}, lines...)
	return rc.Runner.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

func localeListed(list, want string) bool {
	normalize := func(s string) string {
		value := strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.TrimSpace(s)))
		switch value {
		case "c", "cutf8", "posix", "posixutf8":
			return "c"
		default:
			return value
		}
	}
	for _, line := range strings.Split(list, "\n") {
		if normalize(line) == normalize(want) {
			return true
		}
	}
	return false
}

func rockyLocaleLanguagePack(locale string) (string, error) {
	value := locale
	if value == "" || strings.TrimSpace(value) != value {
		return "", fmt.Errorf("Rocky locale %q does not map to a safe glibc language pack", locale)
	}
	for _, r := range value {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '@' || r == '-') {
			return "", fmt.Errorf("Rocky locale %q does not map to a safe glibc language pack", locale)
		}
	}
	upper := strings.ToUpper(value)
	special := map[string]bool{
		"C": true, "POSIX": true, "C.UTF8": true, "C.UTF-8": true,
		"C.UTF_8": true, "POSIX.UTF8": true, "POSIX.UTF-8": true, "POSIX.UTF_8": true,
	}
	if special[upper] {
		return "", nil
	}
	language := value
	if i := strings.IndexAny(value, "_.@"); i >= 0 {
		language = value[:i]
	}
	if len(language) != 2 && len(language) != 3 {
		return "", fmt.Errorf("Rocky locale %q does not map to a safe glibc language pack", locale)
	}
	for _, r := range language {
		if r < 'A' || (r > 'Z' && r < 'a') || r > 'z' {
			return "", fmt.Errorf("Rocky locale %q does not map to a safe glibc language pack", locale)
		}
	}
	return "glibc-langpack-" + strings.ToLower(language), nil
}
