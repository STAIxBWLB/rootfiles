package fleet

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SSHConfigImport contains concrete aliases and explanations for skipped names.
type SSHConfigImport struct {
	Hosts   []string
	Skipped []string
}

// ImportSSHConfig reads aliases without invoking ssh or resolving a connection.
// Relative Include paths are resolved from the user's .ssh directory, as in
// OpenSSH's user configuration. Include recursion is bounded and cycles fail.
func ImportSSHConfig(path, match string) (SSHConfigImport, error) {
	var result SSHConfigImport
	if match != "" {
		if _, err := filepath.Match(match, "test"); err != nil {
			return result, fmt.Errorf("invalid --match glob %q: %w", match, err)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return result, fmt.Errorf("find home directory: %w", err)
	}
	base := filepath.Join(home, ".ssh")
	seen := make(map[string]bool)
	active := make(map[string]bool)
	var read func(string, int) error
	read = func(name string, depth int) error {
		if depth > 16 {
			return fmt.Errorf("SSH config includes exceed 16 levels at %q", name)
		}
		abs, err := filepath.Abs(name)
		if err != nil {
			return err
		}
		if active[abs] {
			return fmt.Errorf("SSH config include cycle at %q", abs)
		}
		f, err := os.Open(abs)
		if err != nil {
			return fmt.Errorf("read SSH config %q: %w", abs, err)
		}
		defer f.Close()
		active[abs] = true
		defer delete(active, abs)
		scan := bufio.NewScanner(f)
		for line := 1; scan.Scan(); line++ {
			fields, err := sshConfigFields(scan.Text())
			if err != nil {
				return fmt.Errorf("SSH config %q line %d: %w", abs, line, err)
			}
			if len(fields) < 2 {
				continue
			}
			switch strings.ToLower(strings.TrimSuffix(fields[0], "=")) {
			case "host":
				for _, alias := range fields[1:] {
					if strings.ContainsAny(alias, "*?!") {
						result.Skipped = append(result.Skipped, fmt.Sprintf("%s: pattern or negation", alias))
						continue
					}
					if !inventoryName.MatchString(alias) || !validSSHHost(alias) {
						result.Skipped = append(result.Skipped, fmt.Sprintf("%s: unsafe inventory name or SSH destination", alias))
						continue
					}
					if match != "" {
						ok, _ := filepath.Match(match, alias)
						if !ok {
							continue
						}
					}
					if !seen[alias] {
						seen[alias] = true
						result.Hosts = append(result.Hosts, alias)
					}
				}
			case "include":
				for _, pattern := range fields[1:] {
					if pattern == "~" {
						pattern = home
					} else if strings.HasPrefix(pattern, "~/") {
						pattern = filepath.Join(home, pattern[2:])
					} else if !filepath.IsAbs(pattern) {
						pattern = filepath.Join(base, pattern)
					}
					matches, err := filepath.Glob(pattern)
					if err != nil {
						return fmt.Errorf("invalid SSH Include %q: %w", pattern, err)
					}
					sort.Strings(matches)
					for _, included := range matches {
						if err := read(included, depth+1); err != nil {
							return err
						}
					}
				}
			}
		}
		if err := scan.Err(); err != nil {
			return fmt.Errorf("read SSH config %q: %w", abs, err)
		}
		return nil
	}
	if err := read(path, 0); err != nil {
		return SSHConfigImport{}, err
	}
	return result, nil
}

// sshConfigFields handles the quoted words and comments used in ssh_config.
func sshConfigFields(line string) ([]string, error) {
	var fields []string
	var word strings.Builder
	var quote rune
	escape, started := false, false
	flush := func() {
		if started {
			fields = append(fields, word.String())
			word.Reset()
			started = false
		}
	}
	for _, c := range line {
		if escape {
			word.WriteRune(c)
			started = true
			escape = false
			continue
		}
		if c == '\\' {
			escape = true
			started = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				word.WriteRune(c)
			}
			started = true
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			started = true
			continue
		}
		if c == '#' {
			break
		}
		if c == ' ' || c == '\t' || c == '=' {
			flush()
			continue
		}
		word.WriteRune(c)
		started = true
	}
	if escape || quote != 0 {
		return nil, fmt.Errorf("unterminated quote or escape")
	}
	flush()
	return fields, nil
}
