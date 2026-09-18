package state

import (
	"testing"
	"time"
)

func TestShouldNotify(t *testing.T) {
	st := &State{Notifications: map[string]NotificationEntry{}}
	cooldown := 20 * time.Minute
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	if !ShouldNotify(st, "auth_publickey", now, cooldown) {
		t.Fatal("first notification for a category should always fire")
	}

	RecordNotified(st, "auth_publickey", now)

	if ShouldNotify(st, "auth_publickey", now.Add(5*time.Minute), cooldown) {
		t.Error("should still be suppressed within the cooldown window")
	}
	if !ShouldNotify(st, "auth_publickey", now.Add(21*time.Minute), cooldown) {
		t.Error("should fire again once the cooldown has elapsed")
	}

	// A different category must not be suppressed by auth_publickey's cooldown.
	if !ShouldNotify(st, "hostkey_changed", now.Add(time.Minute), cooldown) {
		t.Error("a distinct category should notify promptly, independent of another category's cooldown")
	}
}

func TestRecordNotifiedInitializesNilMap(t *testing.T) {
	st := &State{}
	RecordNotified(st, "auth_agent", time.Now())
	if st.Notifications == nil {
		t.Fatal("RecordNotified should initialize a nil Notifications map")
	}
	if _, ok := st.Notifications["auth_agent"]; !ok {
		t.Fatal("expected entry for auth_agent")
	}
}
