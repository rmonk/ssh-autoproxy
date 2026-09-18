package state

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingFileReturnsEmptyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	st, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if st.Notifications == nil {
		t.Fatal("expected an initialized empty Notifications map")
	}
	if len(st.Notifications) != 0 {
		t.Fatalf("expected no entries, got %d", len(st.Notifications))
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	st := &State{Notifications: map[string]NotificationEntry{
		"auth_publickey": {LastNotified: now},
	}}
	if err := Save(path, st); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	got := loaded.Notifications["auth_publickey"].LastNotified
	if !got.Equal(now) {
		t.Errorf("LastNotified = %v, want %v", got, now)
	}
}
