package ui

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestDefaultSemanticRolePalette(t *testing.T) {
	cases := []struct {
		role  Role
		color string
		bold  bool
	}{
		{RoleHeader, "4", true}, {RoleSection, "5", true}, {RoleKey, "4", false},
		{RoleValue, "", false}, {RoleSuccess, "2", true}, {RoleWarning, "3", true},
		{RoleError, "1", true}, {RoleHint, "8", false}, {RoleAccent, "6", false},
	}
	if len(cases) != len(roles) {
		t.Fatal("role inventory incomplete")
	}
	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			style := roleStyle(palettes["ansi"], tc.role)
			var want any = lipgloss.NoColor{}
			if tc.color != "" {
				want = lipgloss.Color(tc.color)
			}
			if !reflect.DeepEqual(style.GetForeground(), want) || style.GetBold() != tc.bold {
				t.Fatalf("role %s color=%v bold=%v", tc.role, style.GetForeground(), style.GetBold())
			}
			rendered := style.Render("role")
			if strings.Contains(rendered, "38;") || strings.Contains(rendered, "48;") {
				t.Fatalf("ANSI role emitted extended color: %q", rendered)
			}
			if !reflect.DeepEqual(RoleStyle(tc.role), style) {
				t.Fatalf("RoleStyle(%s) mismatch", tc.role)
			}
		})
	}
}

func relativeLuminance(t *testing.T, hex string) float64 {
	t.Helper()
	if len(hex) != 7 || hex[0] != '#' {
		t.Fatalf("invalid color %q", hex)
	}
	var channels [3]float64
	for i := range channels {
		n, err := strconv.ParseUint(hex[1+i*2:3+i*2], 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		x := float64(n) / 255
		if x <= 0.04045 {
			channels[i] = x / 12.92
		} else {
			channels[i] = math.Pow((x+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*channels[0] + 0.7152*channels[1] + 0.0722*channels[2]
}

func TestOptionalTokyoNightThemesMeetContrast(t *testing.T) {
	for _, name := range []string{"tokyo-night-dark", "tokyo-night-light"} {
		t.Run(name, func(t *testing.T) {
			p := palettes[name]
			bg := relativeLuminance(t, p.background)
			for _, role := range roles {
				foreground := p.colors[role]
				if role == RoleValue {
					foreground = p.foreground
				}
				fg := relativeLuminance(t, foreground)
				ratio := (max(fg, bg) + 0.05) / (min(fg, bg) + 0.05)
				minimum := 4.5
				if role == RoleHint {
					minimum = 3
				}
				if ratio < minimum {
					t.Errorf("%s contrast %.3f < %.1f on %s", role, ratio, minimum, p.background)
				}
			}
		})
	}
}

func TestThemeIsExplicitAndRestoresOnExit(t *testing.T) {
	before := captureStyles()
	restore, warning := SetPresentation(func(k string) string {
		if k == "ROOTFILES_COLOR_THEME" {
			return "tokyo-night-light"
		}
		return ""
	})
	if warning != "" {
		t.Fatal(warning)
	}
	if !reflect.DeepEqual(StyleKey.GetForeground(), lipgloss.Color(palettes["tokyo-night-light"].colors[RoleKey])) {
		t.Fatal("explicit ROOTFILES_COLOR_THEME was not selected")
	}
	restore()
	if !reflect.DeepEqual(captureStyles(), before) {
		t.Fatal("theme escaped invocation")
	}

	dotRestore, warning := SetPresentation(func(k string) string {
		if k == "DOT_COLOR_THEME" {
			return "tokyo-night-dark"
		}
		return ""
	})
	if warning != "" {
		t.Fatal(warning)
	}
	if !reflect.DeepEqual(StyleKey.GetForeground(), lipgloss.Color(palettes["tokyo-night-dark"].colors[RoleKey])) {
		t.Fatal("DOT_COLOR_THEME fallback was not selected")
	}

	fallbackRestore, warning := SetPresentation(func(k string) string {
		if k == "ROOTFILES_COLOR_THEME" {
			return "typo"
		}
		return ""
	})
	for _, want := range []string{"typo", "using ansi", "tokyo-night-dark", "tokyo-night-light"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning lacks %q: %q", want, warning)
		}
	}
	if !reflect.DeepEqual(StyleKey.GetForeground(), lipgloss.Color("4")) {
		t.Fatal("unknown theme did not fall back to ANSI")
	}
	fallbackRestore()
	if !reflect.DeepEqual(StyleKey.GetForeground(), lipgloss.Color(palettes["tokyo-night-dark"].colors[RoleKey])) {
		t.Fatal("fallback scope lost prior theme")
	}
	dotRestore()
	if !reflect.DeepEqual(captureStyles(), before) {
		t.Fatal("fallback theme escaped invocation")
	}
}

func TestThemesLeaveDefaultForegroundAndBackgroundToTerminal(t *testing.T) {
	for name, p := range palettes {
		if !reflect.DeepEqual(roleStyle(p, RoleValue).GetForeground(), lipgloss.NoColor{}) {
			t.Errorf("%s paints the value role", name)
		}
		for _, role := range roles {
			if !reflect.DeepEqual(roleStyle(p, role).GetBackground(), lipgloss.NoColor{}) {
				t.Errorf("%s/%s paints a background", name, role)
			}
		}
	}
}
