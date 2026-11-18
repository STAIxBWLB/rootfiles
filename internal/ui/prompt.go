package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"
)

// TerminalAttached reports whether the prompts below can run: huh's
// accessible mode (TERM=dumb) needs no terminal; otherwise bubbletea's rule
// applies, stdin when it is a terminal, else /dev/tty.
// Without either (a plain ssh command, systemd timer, CI) huh fails with
// "bubbletea: could not open TTY". A variable so tests can pin it.
var TerminalAttached = func() bool {
	if os.Getenv("TERM") == "dumb" {
		return true
	}
	if term.IsTerminal(os.Stdin.Fd()) {
		return true
	}
	f, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// ErrNoTerminal is what a prompt returns when it has no terminal to run on.
var ErrNoTerminal = errors.New("no terminal for an interactive prompt; pass --yes, or run from a terminal")

// ErrNoAnswer is what a TERM=dumb prompt returns when stdin ends before the
// prompt is answered.
var ErrNoAnswer = errors.New("stdin ended before an answer; pass --yes, or answer the prompt")

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func noTerminal(message string) error {
	if TerminalAttached() {
		return nil
	}
	return fmt.Errorf("%s: %w", promptLabel(message), ErrNoTerminal)
}

func promptLabel(message string) string {
	return strings.TrimSpace(ansiRe.ReplaceAllString(message, ""))
}

// runPrompt runs one field. huh's accessible mode (TERM=dumb) takes EOF,
// or a line its scanner cannot hold, as the default answer, which would let
// `apply` proceed with nobody there. There an answer is a full stdin line,
// and a prompt that gets none fails with the reason instead.
func runPrompt(message string, field huh.Field) (err error) {
	if os.Getenv("TERM") != "dumb" {
		return huh.Run(field)
	}
	in := &lineReader{r: os.Stdin}
	defer func() {
		// ponytail: huh v1.0.0's Select indexes a rejected answer once its
		// input ends and panics; drop this when huh reports EOF itself.
		if r := recover(); r != nil {
			if in.err == nil {
				panic(r)
			}
			err = fmt.Errorf("%s: %w", promptLabel(message), in.err)
		}
	}()
	if err := huh.NewForm(huh.NewGroup(field)).WithShowHelp(false).WithInput(in).Run(); err != nil {
		return err
	}
	if in.err != nil {
		return fmt.Errorf("%s: %w", promptLabel(message), in.err)
	}
	return nil
}

// maxAnswer bounds one answer line, well under huh's 64 KiB scanner limit.
const maxAnswer = 16 << 10

var errLongAnswer = fmt.Errorf("answer longer than %d bytes; give the value in the config or on the command line instead", maxAnswer)

// lineReader hands huh one stdin line per read, reading byte by byte so a
// prompt never consumes a later prompt's answer. Stdin ending, a read error
// or a line longer than maxAnswer sets err, and huh then reads EOF.
type lineReader struct {
	r    io.Reader
	line []byte
	err  error
}

func (l *lineReader) Read(p []byte) (int, error) {
	if l.err != nil {
		return 0, io.EOF
	}
	if len(l.line) == 0 {
		var b [1]byte
		for len(l.line) == 0 || l.line[len(l.line)-1] != '\n' {
			n, err := l.r.Read(b[:])
			l.line = append(l.line, b[:n]...)
			switch {
			case errors.Is(err, io.EOF):
				l.err = ErrNoAnswer
			case err != nil:
				l.err = fmt.Errorf("reading the answer from stdin: %w; pass --yes, or run from a terminal", err)
			case len(l.line) > maxAnswer:
				l.err = errLongAnswer
			}
			if l.err != nil {
				l.line = nil
				return 0, io.EOF
			}
		}
	}
	n := copy(p, l.line)
	l.line = l.line[n:]
	return n, nil
}

// Confirm asks for yes/no. Returns true immediately if unattended.
func Confirm(message string, unattended bool) (bool, error) {
	if unattended {
		return true, nil
	}
	if err := noTerminal(message); err != nil {
		return false, err
	}
	var confirmed bool
	err := runPrompt(message, huh.NewConfirm().
		Title(message).
		Value(&confirmed))
	return confirmed, err
}

// ConfirmDefault asks for yes/no with def pre-selected. Returns def
// immediately if unattended, so profile values survive both paths.
func ConfirmDefault(message string, def, unattended bool) (bool, error) {
	if unattended {
		return def, nil
	}
	if err := noTerminal(message); err != nil {
		return def, err
	}
	confirmed := def
	err := runPrompt(message, huh.NewConfirm().
		Title(message).
		Value(&confirmed))
	if err != nil {
		return def, err
	}
	return confirmed, nil
}

// Select presents a choice. Returns defaultVal if unattended.
func Select(message string, options []string, defaultVal string, unattended bool) (string, error) {
	if unattended {
		return defaultVal, nil
	}
	if err := noTerminal(message); err != nil {
		return defaultVal, err
	}
	selected := defaultVal
	opts := make([]huh.Option[string], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o, o)
	}
	err := runPrompt(message, huh.NewSelect[string]().
		Title(message).
		Options(opts...).
		Value(&selected))
	if err != nil {
		return defaultVal, err
	}
	return selected, nil
}

// Input asks for text input. Returns defaultVal if unattended.
func Input(message, defaultVal string, unattended bool) (string, error) {
	if unattended {
		return defaultVal, nil
	}
	if err := noTerminal(message); err != nil {
		return defaultVal, err
	}
	var value string
	err := runPrompt(message, huh.NewInput().
		Title(message).
		Value(&value).
		Placeholder(defaultVal))
	if err != nil {
		return defaultVal, err
	}
	if value == "" {
		return defaultVal, nil
	}
	return value, nil
}

// InputInt asks for an integer input. Returns defaultVal if unattended or empty.
func InputInt(message string, defaultVal int, unattended bool) (int, error) {
	s, err := Input(message, strconv.Itoa(defaultVal), unattended)
	if err != nil {
		return defaultVal, err
	}
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return defaultVal, fmt.Errorf("invalid number %q: %w", s, err)
	}
	return v, nil
}

// InputIntSlice asks for a comma-separated list of integers.
func InputIntSlice(message string, defaultVal []int, unattended bool) ([]int, error) {
	parts := make([]string, len(defaultVal))
	for i, v := range defaultVal {
		parts[i] = strconv.Itoa(v)
	}
	s, err := Input(message, strings.Join(parts, ","), unattended)
	if err != nil {
		return defaultVal, err
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultVal, nil
	}
	tokens := strings.Split(s, ",")
	result := make([]int, 0, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		v, err := strconv.Atoi(t)
		if err != nil {
			return defaultVal, fmt.Errorf("invalid number %q: %w", t, err)
		}
		result = append(result, v)
	}
	return result, nil
}
