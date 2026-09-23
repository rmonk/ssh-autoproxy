// Package update implements self-update from this project's GitHub
// releases: find the latest release, download the archive for the running
// platform, verify it against the release's SHA256SUMS, and atomically
// replace the running executable with the binary inside it.
package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// DefaultAPIBase is the GitHub REST API root.
	DefaultAPIBase = "https://api.github.com"
	// Repo is the GitHub owner/name releases are published under.
	Repo = "rmonk/ssh-autoproxy"
	// ChecksumsAsset is the name of the checksum file the release workflow
	// publishes alongside the archives.
	ChecksumsAsset = "SHA256SUMS"

	// maxArchiveSize bounds how much is downloaded/extracted, so a bad or
	// hostile response can't fill memory or disk.
	maxArchiveSize   = 100 << 20
	maxChecksumsSize = 1 << 20
)

// Release is the subset of a GitHub release this package needs.
type Release struct {
	Tag    string
	Assets map[string]string // asset name -> download URL
}

// Client fetches release metadata and assets.
type Client struct {
	HTTP    *http.Client
	APIBase string
	Repo    string
}

// NewClient returns a Client for this project's releases on github.com.
func NewClient() *Client {
	return &Client{HTTP: http.DefaultClient, APIBase: DefaultAPIBase, Repo: Repo}
}

// Latest returns the repo's latest (non-draft, non-prerelease) release.
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimSuffix(c.APIBase, "/"), c.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("querying latest release: %s", resp.Status)
	}

	var body struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxChecksumsSize)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding latest release: %w", err)
	}
	if body.TagName == "" {
		return nil, errors.New("latest release has no tag")
	}
	rel := &Release{Tag: body.TagName, Assets: make(map[string]string, len(body.Assets))}
	for _, a := range body.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

// ArchiveName is the release asset name for a platform, matching the
// naming in .github/workflows/release.yml.
func ArchiveName(tag, goos, goarch string) string {
	return fmt.Sprintf("%s.tar.gz", binaryName(tag, goos, goarch))
}

func binaryName(tag, goos, goarch string) string {
	return fmt.Sprintf("ssh-autoproxy-%s-%s-%s", tag, goos, goarch)
}

// IsNewer reports whether latest is a higher vMAJOR.MINOR.PATCH version
// than current. A current that isn't a release version (e.g. "dev") is an
// error, since there's nothing meaningful to compare against.
func IsNewer(current, latest string) (bool, error) {
	cur, err := parseVersion(current)
	if err != nil {
		return false, fmt.Errorf("current version: %w", err)
	}
	lat, err := parseVersion(latest)
	if err != nil {
		return false, fmt.Errorf("latest version: %w", err)
	}
	for i := range cur {
		if lat[i] != cur[i] {
			return lat[i] > cur[i], nil
		}
	}
	return false, nil
}

func parseVersion(v string) ([3]int, error) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if !strings.HasPrefix(v, "v") || len(parts) != 3 {
		return out, fmt.Errorf("%q is not a vMAJOR.MINOR.PATCH version", v)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, fmt.Errorf("%q is not a vMAJOR.MINOR.PATCH version", v)
		}
		out[i] = n
	}
	return out, nil
}

// Apply downloads rel's archive for goos/goarch, verifies it against the
// release's SHA256SUMS, and atomically replaces exePath with the binary it
// contains. exePath is left untouched on any failure.
func (c *Client) Apply(ctx context.Context, rel *Release, goos, goarch, exePath string) error {
	archive := ArchiveName(rel.Tag, goos, goarch)
	archiveURL, ok := rel.Assets[archive]
	if !ok {
		return fmt.Errorf("release %s has no build for %s/%s (%s)", rel.Tag, goos, goarch, archive)
	}
	sumsURL, ok := rel.Assets[ChecksumsAsset]
	if !ok {
		return fmt.Errorf("release %s has no %s file; refusing to install an unverifiable build", rel.Tag, ChecksumsAsset)
	}

	sumsData, err := c.download(ctx, sumsURL, maxChecksumsSize)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", ChecksumsAsset, err)
	}
	want, ok := parseChecksums(sumsData)[archive]
	if !ok {
		return fmt.Errorf("%s has no entry for %s", ChecksumsAsset, archive)
	}

	data, err := c.download(ctx, archiveURL, maxArchiveSize)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", archive, err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", archive, got, want)
	}

	bin, err := extractFile(data, binaryName(rel.Tag, goos, goarch))
	if err != nil {
		return fmt.Errorf("extracting %s: %w", archive, err)
	}
	return replaceFile(exePath, bin)
}

func (c *Client) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("larger than %d bytes", limit)
	}
	return data, nil
}

// parseChecksums parses sha256sum output ("<hex>  <name>" per line, with
// an optional '*' binary-mode marker before the name).
func parseChecksums(data []byte) map[string]string {
	sums := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		sums[strings.TrimPrefix(fields[1], "*")] = fields[0]
	}
	return sums
}

// extractFile returns the contents of the regular file called name at the
// top level of a gzipped tar archive.
func extractFile(archive []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s not found in archive", name)
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name != name || hdr.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxArchiveSize+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxArchiveSize {
			return nil, fmt.Errorf("%s is larger than %d bytes", name, maxArchiveSize)
		}
		return data, nil
	}
}

// replaceFile writes data to a temp file beside path and renames it over
// path, so path is never observed half-written and a running copy of the
// old binary keeps working until it's restarted.
func replaceFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".update-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}
