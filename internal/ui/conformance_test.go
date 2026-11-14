package ui

import (
	"bytes"
	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
)

func cleanOutputEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"NO_COLOR", "FORCE_COLOR", "CLICOLOR_FORCE", "CLICOLOR", "COLUMNS"} {
		t.Setenv(name, "")
	}
	t.Setenv("LC_ALL", "C.UTF-8")
}

func TestSemanticRoleGolden(t *testing.T) {
	cleanOutputEnv(t)
	var plain, colored bytes.Buffer
	render := func(out *bytes.Buffer, mode string) {
		w := ConfigureWriter(out, mode, false, false)
		WriteHeader(w, "rootfiles status")
		WriteSection(w, "System")
		WriteKV(w, "OS", "ubuntu")
		WriteBullet(w, OKMark(), "healthy")
	}
	render(&plain, "never")
	render(&colored, "always")
	const want = " rootfiles status\n\n▸ System\n  OS:             ubuntu\n  ✓  healthy\n"
	if plain.String() != want {
		t.Fatalf("plain golden:\n%q\nwant:\n%q", plain.String(), want)
	}
	if ansi.Strip(colored.String()) != want {
		t.Fatalf("colored text changed: %q", colored.String())
	}
	for _, code := range []string{"34", "35", "32"} {
		found := false
		for _, sequence := range strings.Split(colored.String(), "\x1b[") {
			if strings.HasPrefix(sequence, code+"m") || strings.HasPrefix(sequence, "1;"+code+"m") || strings.HasPrefix(sequence, code+";1m") {
				found = true
			}
		}
		if !found {
			t.Errorf("missing ANSI %s in %q", code, colored.String())
		}
	}
	roles := []struct {
		name string
		text string
		code string
		bold bool
	}{
		{"header", StyleHeader.Render("role"), "34", true}, {"section", StyleSection.Render("role"), "35", true},
		{"key", StyleKey.Render("role"), "34", false}, {"value", StyleValue.Render("role"), "", false},
		{"success", StyleSuccess.Render("role"), "32", true}, {"warning", StyleWarning.Render("role"), "33", true},
		{"error", StyleError.Render("role"), "31", true}, {"hint", StyleHint.Render("role"), "90", false}, {"accent", StyleAccent.Render("role"), "36", false},
	}
	for _, role := range roles {
		if role.code != "" && !strings.Contains(role.text, role.code) {
			t.Errorf("%s role missing code: %q", role.name, role.text)
		}

		bold := false
		for _, sequence := range strings.Split(role.text, "\x1b[")[1:] {
			code, _, ok := strings.Cut(sequence, "m")
			if !ok {
				continue
			}
			for _, part := range strings.Split(code, ";") {
				if part == "1" {
					bold = true
				}
			}
		}
		if bold != role.bold {
			t.Errorf("%s bold=%v, want %v: %q", role.name, bold, role.bold, role.text)
		}
		if role.code == "" && strings.ContainsRune(role.text, 27) {
			t.Errorf("default role decorated: %q", role.text)
		}
	}
}

func TestColorPrecedenceAndJSON(t *testing.T) {
	cleanOutputEnv(t)
	var sink bytes.Buffer
	t.Setenv("FORCE_COLOR", "1")
	if !ResolveColor(&sink, "auto") {
		t.Fatal("force color must color a pipe")
	}
	t.Setenv("NO_COLOR", "1")
	if ResolveColor(&sink, "auto") {
		t.Fatal("NO_COLOR must beat force color")
	}
	if !ResolveColor(&sink, "always") {
		t.Fatal("explicit always must beat NO_COLOR")
	}
	if ResolveColor(&sink, "never") {
		t.Fatal("explicit never must disable color")
	}
	WriteLine(ConfigureWriter(&sink, "auto", false, false), StyleError.Render("failure"))
	if strings.ContainsRune(sink.String(), 27) {
		t.Fatal("NO_COLOR emitted ESC")
	}
	sink.Reset()
	raw := "{\"value\":\"✓\"}\n"
	if _, err := ConfigureWriter(&sink, "always", true, true).Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if sink.String() != raw {
		t.Fatalf("JSON changed: %q", sink.String())
	}
	t.Setenv("NO_COLOR", "")
	for _, pair := range []struct {
		force, cliForce, cli string
		want                 bool
	}{{"0", "1", "0", true}, {"0", "", "", false}, {"", "0", "0", false}, {"", "", "", false}} {
		t.Setenv("FORCE_COLOR", pair.force)
		t.Setenv("CLICOLOR_FORCE", pair.cliForce)
		t.Setenv("CLICOLOR", pair.cli)
		if got := ResolveColor(&sink, "auto"); got != pair.want {
			t.Errorf("force=%q cliForce=%q cli=%q: %v", pair.force, pair.cliForce, pair.cli, got)
		}
	}
}

func TestASCIIAndMarkerCells(t *testing.T) {
	cleanOutputEnv(t)
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C")
	var out bytes.Buffer
	w := ConfigureWriter(&out, "never", false, false)
	for _, marker := range []string{MarkOK, MarkFail, MarkWarn, MarkAbsent, MarkUnknown, MarkPending, MarkActive, MarkNext} {
		WriteBullet(w, marker, "state")
	}
	want := "  ok state\n  x  state\n  !  state\n  -  state\n  ?  state\n  ~  state\n  *  state\n  -> state\n"
	if out.String() != want {
		t.Fatalf("ASCII markers: %q want %q", out.String(), want)
	}
	for _, marker := range []string{MarkOK, MarkFail, MarkWarn, MarkNext} {
		for _, ascii := range []bool{false, true} {
			if n := ansi.StringWidth(MarkerCell(marker, ascii)); n != 2 {
				t.Errorf("%q cell width %d", marker, n)
			}
		}
	}
	out.Reset()
	WriteKV(w, "Name", "a✓b")
	if !strings.Contains(out.String(), "a✓b") {
		t.Fatal("ASCII policy changed data")
	}
	t.Setenv("LC_CTYPE", "en_US.UTF-8")
	if ASCIIMarkers(false) {
		t.Fatal("LC_CTYPE must precede LANG")
	}
	t.Setenv("LC_ALL", "C")
	if !ASCIIMarkers(false) {
		t.Fatal("LC_ALL must precede LC_CTYPE")
	}
}

func TestTextWidthAt80(t *testing.T) {
	cleanOutputEnv(t)
	t.Setenv("COLUMNS", "80")
	var out bytes.Buffer
	if OutputWidth(&out) != 80 {
		t.Fatal("pipe ignores COLUMNS")
	}
	for _, line := range strings.Split(WrapText(strings.Repeat("한국어 text ", 30), 80), "\n") {
		if n := ansi.StringWidth(line); n > 80 {
			t.Fatalf("line width %d", n)
		}
	}
}

func TestPipedErrorAndNextStep(t *testing.T) {
	cleanOutputEnv(t)
	var out bytes.Buffer
	WriteError(ConfigureWriter(&out, "never", false, false), "open file: permission denied", "sudo rootfiles status")
	if out.String() != "rootfiles: open file: permission denied\n  → sudo rootfiles status\n" {
		t.Fatalf("error format: %q", out.String())
	}
	out.Reset()
	WriteError(&out, "server refused", "")
	if out.String() != "rootfiles: server refused\n" {
		t.Fatalf("invented fix: %q", out.String())
	}
}

func TestTableAt80PreservesColumns(t *testing.T) {
	cleanOutputEnv(t)
	t.Setenv("COLUMNS", "80")
	var out bytes.Buffer
	WriteTable(ConfigureWriter(&out, "never", false, false), []string{"id", "state", "finding", "empty"}, [][]string{{"peer-123", "failed", strings.Repeat("long finding ", 20), "-"}, {strings.Repeat("path/", 20), "ok", "done", ""}}, 2)
	if strings.Contains(out.String(), "EMPTY") {
		t.Fatal("empty column not dropped")
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if width := ansi.StringWidth(line); width > 80 {
			t.Fatalf("table width %d: %q", width, line)
		}
	}
	if !strings.Contains(out.String(), "peer-123") || !strings.Contains(out.String(), "failed") || !strings.Contains(out.String(), "...") {
		t.Fatalf("missing preserved cells: %q", out.String())
	}
}

func TestHumanRowsWrapOnlyFreeText(t *testing.T) {
	cleanOutputEnv(t)
	t.Setenv("COLUMNS", "80")
	var out bytes.Buffer
	w := ConfigureWriter(&out, "never", false, false)
	text := strings.Repeat("한국어 finding ", 20)
	WriteKV(w, "Profile", text)
	WriteHint(w, text)
	WriteBullet(w, WarnMark(), text)
	for _, line := range strings.Split(out.String(), "\n") {
		if width := ansi.StringWidth(line); width > 80 {
			t.Fatalf("row width %d: %q", width, line)
		}
	}
}

type testFDWriter struct {
	bytes.Buffer
	fd uintptr
}

func (w *testFDWriter) Fd() uintptr { return w.fd }

func TestIndependentStreamPolicyAndTTYError(t *testing.T) {
	cleanOutputEnv(t)
	t.Setenv("TERM", "xterm")
	old := terminalFD
	terminalFD = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { terminalFD = old })
	stdout := &testFDWriter{fd: 42}
	stderr := &testFDWriter{fd: 43}
	out := ConfigureWriter(stdout, "auto", false, false)
	errOut := ConfigureWriter(stderr, "auto", false, false)
	WriteLine(out, StyleSuccess.Render("healthy"))
	WriteError(errOut, "open: denied", "")
	if !strings.ContainsRune(stdout.String(), 27) || strings.ContainsRune(stderr.String(), 27) {
		t.Fatalf("stream policy stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.HasPrefix(stderr.String(), "rootfiles: ") {
		t.Fatalf("piped error prefix: %q", stderr.String())
	}
	stderr.fd = 42
	stderr.Reset()
	WriteError(ConfigureWriter(stderr, "never", false, false), "open: denied", "")
	if stderr.String() != MarkFail+" rootfiles: open: denied\n" {
		t.Fatalf("TTY error without color: %q", stderr.String())
	}
}

func TestASCIIColoredNextStep(t *testing.T) {
	cleanOutputEnv(t)
	t.Setenv("LC_ALL", "C")
	var out bytes.Buffer
	WriteError(ConfigureWriter(&out, "always", false, false), "open: denied", "rootfiles status")
	if strings.Contains(out.String(), MarkNext) || !strings.Contains(ansi.Strip(out.String()), "  -> rootfiles status") {
		t.Fatalf("colored next step ignored ASCII locale: %q", out.String())
	}
}
