// Package state persists small daemon state (currently: notification
// dedup timestamps) across restarts.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type NotificationEntry struct {
	LastNotified time.Time `json:"last_notified"`
}

type State struct {
	Notifications map[string]NotificationEntry `json:"notifications"`
}

// FilePath returns the default state file location, honoring
// XDG_STATE_HOME and falling back to ~/.local/state.
func FilePath() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "ssh-autoproxy", "state.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "ssh-autoproxy", "state.json"), nil
}

// Load reads the state file at path (or the default location if path is
// empty). A missing file yields an empty State, not an error.
func Load(path string) (*State, error) {
	if path == "" {
		p, err := FilePath()
		if err != nil {
			return nil, err
		}
		path = p
	}

	st := &State{Notifications: map[string]NotificationEntry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, err
	}
	if st.Notifications == nil {
		st.Notifications = map[string]NotificationEntry{}
	}
	return st, nil
}

// Save atomically writes the state file (write to a temp file, then
// rename), creating parent directories as needed.
func Save(path string, st *State) error {
	if path == "" {
		p, err := FilePath()
		if err != nil {
			return err
		}
		path = p
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
