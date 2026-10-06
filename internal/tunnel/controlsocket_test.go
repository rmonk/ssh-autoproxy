package tunnel

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestParseControlPath(t *testing.T) {
	out := []byte("user rmonk\nhostname gw.example.com\nport 22\ncontrolpath /home/u/.ssh/control/rmonk@gw.example.com:22\ncontrolmaster false\n")
	got, err := parseControlPath(out)
	if err != nil || got != "/home/u/.ssh/control/rmonk@gw.example.com:22" {
		t.Errorf("parseControlPath = %q, %v", got, err)
	}
	for _, bad := range []string{"user rmonk\n", "controlpath none\n"} {
		if _, err := parseControlPath([]byte(bad)); err == nil {
			t.Errorf("parseControlPath(%q) expected error", bad)
		}
	}
}

// shortTempDir returns a temp dir with a path short enough for a Unix
// socket (limited to ~108 bytes), which t.TempDir() may not be.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestRemoveIfStale(t *testing.T) {
	dir := shortTempDir(t)

	t.Run("missing", func(t *testing.T) {
		removed, err := removeIfStale(filepath.Join(dir, "missing"))
		if removed || err != nil {
			t.Errorf("removeIfStale = %v, %v; want false, nil", removed, err)
		}
	})

	t.Run("live socket kept", func(t *testing.T) {
		path := filepath.Join(dir, "live")
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		removed, err := removeIfStale(path)
		if removed || err != nil {
			t.Errorf("removeIfStale = %v, %v; want false, nil", removed, err)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("live socket was removed: %v", err)
		}
	})

	t.Run("stale socket removed", func(t *testing.T) {
		path := filepath.Join(dir, "stale")
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		// Close without unlinking, as a SIGKILLed ssh master leaves it.
		ln.(*net.UnixListener).SetUnlinkOnClose(false)
		ln.Close()
		removed, err := removeIfStale(path)
		if !removed || err != nil {
			t.Errorf("removeIfStale = %v, %v; want true, nil", removed, err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("stale socket still present: %v", err)
		}
	})

	t.Run("regular file kept", func(t *testing.T) {
		path := filepath.Join(dir, "file")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		removed, err := removeIfStale(path)
		if removed || err != nil {
			t.Errorf("removeIfStale = %v, %v; want false, nil", removed, err)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("regular file was removed: %v", err)
		}
	})
}
