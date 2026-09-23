package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current, latest string
		want            bool
	}{
		{"v0.1.1", "v0.1.2", true},
		{"v0.1.1", "v0.2.0", true},
		{"v0.9.9", "v1.0.0", true},
		{"v0.1.10", "v0.1.9", false},
		{"v0.1.1", "v0.1.1", false},
		{"v1.0.0", "v0.9.0", false},
	}
	for _, tt := range tests {
		got, err := IsNewer(tt.current, tt.latest)
		if err != nil {
			t.Fatalf("IsNewer(%q, %q) error = %v", tt.current, tt.latest, err)
		}
		if got != tt.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}

	for _, bad := range [][2]string{{"dev", "v0.1.1"}, {"v0.1.1", "0.1.2"}, {"v0.1", "v0.1.2"}, {"v0.1.1", "v0.1.x"}} {
		if _, err := IsNewer(bad[0], bad[1]); err == nil {
			t.Errorf("IsNewer(%q, %q) expected error", bad[0], bad[1])
		}
	}
}

func TestParseChecksums(t *testing.T) {
	sums := parseChecksums([]byte("abc123  a.tar.gz\ndef456 *b.tar.gz\n\ngarbage\n"))
	if sums["a.tar.gz"] != "abc123" || sums["b.tar.gz"] != "def456" || len(sums) != 2 {
		t.Errorf("parseChecksums = %v", sums)
	}
}

// fakeRelease serves a GitHub-style latest-release endpoint and its assets.
type fakeRelease struct {
	tag      string
	archive  []byte
	sums     string // "" omits SHA256SUMS from the release
	goos     string
	goarch   string
	archName string
}

func newFakeRelease(t *testing.T, tag string, binary []byte) *fakeRelease {
	t.Helper()
	f := &fakeRelease{tag: tag, goos: "linux", goarch: "amd64"}
	f.archName = ArchiveName(tag, f.goos, f.goarch)
	f.archive = makeArchive(t, binaryName(tag, f.goos, f.goarch), binary)
	sum := sha256.Sum256(f.archive)
	f.sums = fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), f.archName)
	return f
}

func (f *fakeRelease) serve(t *testing.T) *Client {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			assets := []map[string]string{{"name": f.archName, "browser_download_url": srv.URL + "/dl/archive"}}
			if f.sums != "" {
				assets = append(assets, map[string]string{"name": ChecksumsAsset, "browser_download_url": srv.URL + "/dl/sums"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": f.tag, "assets": assets})
		case "/dl/archive":
			_, _ = w.Write(f.archive)
		case "/dl/sums":
			_, _ = w.Write([]byte(f.sums))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{HTTP: srv.Client(), APIBase: srv.URL, Repo: Repo}
}

func makeArchive(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeExe(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "ssh-autoproxy")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestLatestAndApply(t *testing.T) {
	f := newFakeRelease(t, "v0.2.0", []byte("new binary"))
	c := f.serve(t)
	exe := writeExe(t)

	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if rel.Tag != "v0.2.0" {
		t.Fatalf("Tag = %q, want v0.2.0", rel.Tag)
	}
	if err := c.Apply(context.Background(), rel, f.goos, f.goarch, exe); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Errorf("exe contents = %q, want %q", got, "new binary")
	}
	info, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("exe mode = %v, want 0755", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(exe))
	if len(entries) != 1 {
		t.Errorf("expected only the exe in its dir, found %d entries (temp file left behind?)", len(entries))
	}
}

func TestApplyRejectsBadReleases(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*fakeRelease)
		goarch  string
		wantErr string
	}{
		{"checksum mismatch", func(f *fakeRelease) {
			f.sums = strings.Repeat("0", 64) + "  " + f.archName + "\n"
		}, "amd64", "checksum mismatch"},
		{"no checksums file", func(f *fakeRelease) { f.sums = "" }, "amd64", "no SHA256SUMS"},
		{"checksums missing archive", func(f *fakeRelease) {
			f.sums = strings.Repeat("0", 64) + "  other.tar.gz\n"
		}, "amd64", "no entry for"},
		{"no build for platform", func(*fakeRelease) {}, "riscv64", "no build for"},
		{"binary missing from archive", func(f *fakeRelease) {
			f.archive = makeArchive(t, "something-else", []byte("x"))
			sum := sha256.Sum256(f.archive)
			f.sums = hex.EncodeToString(sum[:]) + "  " + f.archName + "\n"
		}, "amd64", "not found in archive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeRelease(t, "v0.2.0", []byte("new binary"))
			tt.mutate(f)
			c := f.serve(t)
			exe := writeExe(t)

			rel, err := c.Latest(context.Background())
			if err != nil {
				t.Fatalf("Latest() error = %v", err)
			}
			err = c.Apply(context.Background(), rel, f.goos, tt.goarch, exe)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Apply() error = %v, want containing %q", err, tt.wantErr)
			}
			if got, _ := os.ReadFile(exe); string(got) != "old" {
				t.Errorf("exe was modified on failure: %q", got)
			}
		})
	}
}
