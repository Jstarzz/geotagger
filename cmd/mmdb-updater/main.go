package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

const retainedReleases = 3

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	license := strings.TrimSpace(os.Getenv("MAXMIND_LICENSE_KEY"))
	if license == "" {
		logger.Error("MAXMIND_LICENSE_KEY is required")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// Legacy/custom single-edition mode remains supported. Production uses the
	// bundle mode below so City and ASN become visible as one atomic generation.
	if edition := strings.TrimSpace(os.Getenv("MAXMIND_EDITION")); edition != "" {
		dest := strings.TrimSpace(os.Getenv("MMDB_PATH"))
		if dest == "" {
			dest = filepath.Join(getenv("MMDB_DIR", "/data"), edition+".mmdb")
		}
		if err := updateSingle(ctx, license, edition, dest); err != nil {
			logger.Error("MMDB update failed", "edition", edition, "error", err)
			os.Exit(1)
		}
		logger.Info("MMDB updated", "edition", edition, "path", dest)
		return
	}

	dir := getenv("MMDB_DIR", "/data")
	editions, err := parseEditions(getenv("MAXMIND_EDITIONS", "GeoLite2-City,GeoLite2-ASN"))
	if err != nil {
		logger.Error("invalid updater configuration", "error", err)
		os.Exit(1)
	}
	active, err := updateBundle(ctx, license, dir, editions)
	if err != nil {
		logger.Error("MMDB bundle update failed", "error", err)
		os.Exit(1)
	}
	logger.Info("MMDB bundle activated", "current", active, "editions", strings.Join(editions, ","))
}

func parseEditions(raw string) ([]string, error) {
	seen := map[string]bool{}
	var editions []string
	for _, part := range strings.Split(raw, ",") {
		edition := strings.TrimSpace(part)
		if edition == "" {
			continue
		}
		if seen[edition] {
			return nil, fmt.Errorf("duplicate MaxMind edition %q", edition)
		}
		seen[edition] = true
		editions = append(editions, edition)
	}
	if len(editions) == 0 {
		return nil, errors.New("MAXMIND_EDITIONS must contain at least one edition")
	}
	return editions, nil
}

// updateBundle stages and verifies every requested database in a versioned
// release directory, then atomically swaps one `current` symlink. API readers
// therefore observe either the previous complete bundle or the new complete
// bundle, never a half-updated City/ASN pair.
func updateBundle(ctx context.Context, license, dir string, editions []string) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	releasesDir := filepath.Join(dir, "releases")
	if err := os.MkdirAll(releasesDir, 0o750); err != nil {
		return "", err
	}

	stageDir, err := os.MkdirTemp(releasesDir, ".staging-")
	if err != nil {
		return "", err
	}
	stageLive := true
	defer func() {
		if stageLive {
			_ = os.RemoveAll(stageDir)
		}
	}()

	for _, edition := range editions {
		dest := filepath.Join(stageDir, edition+".mmdb")
		if err := downloadEdition(ctx, license, edition, dest); err != nil {
			return "", fmt.Errorf("%s: %w", edition, err)
		}
	}
	if err := fsyncDir(stageDir); err != nil {
		return "", fmt.Errorf("fsync staged bundle: %w", err)
	}

	suffix := strings.TrimPrefix(filepath.Base(stageDir), ".staging-")
	releaseName := "release-" + time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + suffix
	releaseDir := filepath.Join(releasesDir, releaseName)
	if err := os.Rename(stageDir, releaseDir); err != nil {
		return "", fmt.Errorf("publish release directory: %w", err)
	}
	stageLive = false
	if err := fsyncDir(releasesDir); err != nil {
		return "", fmt.Errorf("fsync releases directory: %w", err)
	}

	// Use a relative symlink so the data directory remains relocatable inside
	// the hostPath mount. Renaming a symlink over an existing symlink is atomic
	// on the Linux filesystem used by the deployment.
	relTarget := filepath.Join("releases", releaseName)
	tmpLink := filepath.Join(dir, ".current-"+suffix)
	_ = os.Remove(tmpLink)
	if err := os.Symlink(relTarget, tmpLink); err != nil {
		return "", fmt.Errorf("create current symlink: %w", err)
	}
	if err := os.Rename(tmpLink, filepath.Join(dir, "current")); err != nil {
		_ = os.Remove(tmpLink)
		return "", fmt.Errorf("activate MMDB bundle: %w", err)
	}
	if err := fsyncDir(dir); err != nil {
		return "", fmt.Errorf("fsync MMDB directory: %w", err)
	}

	// Keep a few prior complete generations for rollback/forensics while
	// bounding local disk growth. Open mmap readers remain valid on Linux even
	// if an older release is unlinked after a successful atomic switch.
	_ = pruneReleases(releasesDir, retainedReleases)
	return filepath.Join(dir, "current"), nil
}

func updateSingle(ctx context.Context, license, edition, dest string) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	stageDir, err := os.MkdirTemp(dir, ".single-mmdb-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)
	staged := filepath.Join(stageDir, edition+".mmdb")
	if err := downloadEdition(ctx, license, edition, staged); err != nil {
		return err
	}
	if err := os.Rename(staged, dest); err != nil {
		return err
	}
	return fsyncDir(dir)
}

func downloadEdition(ctx context.Context, license, edition, dest string) error {
	q := url.Values{"edition_id": {edition}, "license_key": {license}, "suffix": {"tar.gz"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://download.maxmind.com/app/geoip_download?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MaxMind returned %s", resp.Status)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	wanted := edition + ".mmdb"
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != wanted {
			continue
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, io.LimitReader(tr, 256<<20)); err != nil {
			_ = f.Close()
			_ = os.Remove(dest)
			return err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = os.Remove(dest)
			return err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(dest)
			return err
		}

		reader, err := maxminddb.Open(dest)
		if err != nil {
			_ = os.Remove(dest)
			return fmt.Errorf("open staged database: %w", err)
		}
		verifyErr := reader.Verify()
		closeErr := reader.Close()
		if verifyErr != nil {
			_ = os.Remove(dest)
			return fmt.Errorf("verify staged database: %w", verifyErr)
		}
		if closeErr != nil {
			_ = os.Remove(dest)
			return fmt.Errorf("close staged database: %w", closeErr)
		}
		return nil
	}
	return fmt.Errorf("%s not found in MaxMind archive", wanted)
}

func pruneReleases(releasesDir string, keep int) error {
	if keep < 0 {
		return errors.New("release retention must not be negative")
	}
	entries, err := os.ReadDir(releasesDir)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "release-") {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) <= keep {
		return fsyncDir(releasesDir)
	}
	for _, name := range names[keep:] {
		if err := os.RemoveAll(filepath.Join(releasesDir, name)); err != nil {
			return err
		}
	}
	return fsyncDir(releasesDir)
}

func fsyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func getenv(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}
