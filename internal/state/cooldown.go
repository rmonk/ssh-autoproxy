package state

import "time"

// ShouldNotify reports whether a notification for category should fire
// now: true if there's no record yet, or the cooldown has elapsed since
// the last notification for that category. Tracked independently per
// category, so a brand-new failure type is never suppressed by another
// category's cooldown.
func ShouldNotify(st *State, category string, now time.Time, cooldown time.Duration) bool {
	entry, ok := st.Notifications[category]
	if !ok || entry.LastNotified.IsZero() {
		return true
	}
	return now.Sub(entry.LastNotified) >= cooldown
}

// RecordNotified marks category as notified at now.
func RecordNotified(st *State, category string, now time.Time) {
	if st.Notifications == nil {
		st.Notifications = map[string]NotificationEntry{}
	}
	st.Notifications[category] = NotificationEntry{LastNotified: now}
}
