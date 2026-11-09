package module

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// readJSONDB distinguishes an absent ledger from an unreadable or corrupt one.
func readJSONDB(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cannot read JSON database %s: %w; repair its permissions before retrying", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("cannot parse JSON database %s: %w; restore a valid backup or repair the JSON before retrying", path, err)
	}
	return true, nil
}

// writeJSONDB atomically replaces a ledger and preserves the prior destination
// through Runner.Rename. A fresh sibling avoids reusing an old temporary file's
// permissions; users metadata stays 0600, while the GPU wrapper needs 0644.
func writeJSONDB(rc *RunContext, path string, v any, mode os.FileMode) error {
	if err := refuseSymlink(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot encode JSON database %s: %w; correct the database values before retrying", path, err)
	}
	tmp := path + "." + rand.Text() + ".tmp"
	if err := rc.Runner.WriteFile(tmp, data, mode); err != nil {
		return fmt.Errorf("cannot stage JSON database %s: %w; check directory permissions and available space before retrying", path, err)
	}
	if err := rc.Runner.Rename(tmp, path); err != nil {
		cleanupErr := rc.Runner.Remove(tmp)
		return fmt.Errorf("cannot replace JSON database %s: %w; check directory and backup permissions before retrying", path, errors.Join(err, cleanupErr))
	}
	return nil
}
