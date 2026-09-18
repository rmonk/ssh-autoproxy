package notify

import "testing"

func TestShouldAttemptNotify(t *testing.T) {
	always := func(string) bool { return true }
	never := func(string) bool { return false }

	if !shouldAttemptNotify("unix:path=/run/user/1000/bus", never) {
		t.Error("should attempt when DBUS_SESSION_BUS_ADDRESS is set, even if the fallback socket check fails")
	}
	if !shouldAttemptNotify("", always) {
		t.Error("should attempt via fallback socket path when env var is unset but socket exists")
	}
	if shouldAttemptNotify("", never) {
		t.Error("should not attempt when neither the env var nor the fallback socket is present")
	}
}
