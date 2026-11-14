package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteHeader_FramesTitle(t *testing.T) {
	var buf bytes.Buffer
	WriteHeader(&buf, "rootfiles status")
	out := buf.String()
	if !strings.Contains(out, "rootfiles status") {
		t.Errorf("header missing title, got %q", out)
	}
	// The title is the first report line.
	lines := strings.Split(out, "\n")
	if len(lines) != 2 || lines[0] != " rootfiles status" {
		t.Errorf("expected one title line, got %q", out)
	}
}

func TestWriteSection_PrefixesWithArrow(t *testing.T) {
	t.Setenv("LC_ALL", "C.UTF-8")
	var buf bytes.Buffer
	WriteSection(&buf, "System")
	out := buf.String()
	if !strings.Contains(out, "▸ System") {
		t.Errorf("section missing '▸ ' prefix, got %q", out)
	}
}

func TestWriteKV_UnsetPlaceholderForEmpty(t *testing.T) {
	var buf bytes.Buffer
	WriteKV(&buf, "Tunnel", "")
	out := buf.String()
	if !strings.Contains(out, "(unset)") {
		t.Errorf("empty value should render as (unset), got %q", out)
	}
	if !strings.Contains(out, "Tunnel:") {
		t.Errorf("key should include trailing colon, got %q", out)
	}
}

func TestWriteKV_RealValue(t *testing.T) {
	var buf bytes.Buffer
	WriteKV(&buf, "OS", "ubuntu 24.04")
	out := buf.String()
	if !strings.Contains(out, "OS:") {
		t.Errorf("missing key, got %q", out)
	}
	if !strings.Contains(out, "ubuntu 24.04") {
		t.Errorf("missing value, got %q", out)
	}
	if strings.Contains(out, "(unset)") {
		t.Errorf("real value should not render as (unset), got %q", out)
	}
}

func TestWriteBullet_IndentAndMarker(t *testing.T) {
	t.Setenv("LC_ALL", "C.UTF-8")
	var buf bytes.Buffer
	WriteBullet(&buf, MarkOK, "locale")
	out := buf.String()
	if !strings.HasPrefix(out, "  "+MarkOK) {
		t.Errorf("bullet should start with 2-space indent + marker, got %q", out)
	}
	if !strings.Contains(out, "locale") {
		t.Errorf("bullet missing text, got %q", out)
	}
}

func TestWriteHint_DimmedLine(t *testing.T) {
	var buf bytes.Buffer
	WriteHint(&buf, "run rootfiles check for details")
	out := buf.String()
	if !strings.Contains(out, "run rootfiles check for details") {
		t.Errorf("hint missing text, got %q", out)
	}
	if !strings.HasPrefix(out, "  ") {
		t.Errorf("hint should be 2-space indented, got %q", out)
	}
}
