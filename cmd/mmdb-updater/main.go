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
	"strings"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

type target struct {
	edition string
	dest    string
	staged  string
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	license := strings.TrimSpace(os.Getenv("MAXMIND_LICENSE_KEY"))
	if license == "" {
		logger.Error("MAXMIND_LICENSE_KEY is required")
		os.Exit(1)
	}

	targets, err := targetsFromEnv()
	if err != nil {
		logger.Error("invalid updater configuration", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := updateAll(ctx, license, targets); err != nil {
		logger.Error("MMDB update failed", "error", err)
		os.Exit(1)
	}
	for _, t := range targets {
		logger.Info("MMDB updated", "edition", t.edition, "path", t.dest)
	}
}

func targetsFromEnv() ([]target, error) {
	// Keep the old single-edition mode for local/custom deployments.
	if edition := strings.TrimSpace(os.Getenv("MAXMIND_EDITION")); edition != "" {
		dest := strings.TrimSpace(os.Getenv("MMDB_PATH"))
		if dest == "" {
			dest = filepath.Join(getenv("MMDB_DIR", "/data"), edition+".mmdb")
		}
		return []target{{edition: edition, dest: dest}}, nil
	}

	dir := getenv("MMDB_DIR", "/data")
	editions := strings.Split(getenv("MAXMIND_EDITIONS", "GeoLite2-City,GeoLite2-ASN"), ",")
	seen := map[string]bool{}
	out := make([]target, 0, len(editions))
	for _, raw := range editions {
		edition := strings.TrimSpace(raw)
		if edition == "" {
			continue
		}
		if seen[edition] {
			return nil, fmt.Errorf("duplicate MaxMind edition %q", edition)
		}
		seen[edition] = true
		out = append(out, target{edition: edition, dest: filepath.Join(dir, edition+".mmdb")})
	}
	if len(out) == 0 {
		return nil, errors.New("MAXMIND_EDITIONS must contain at least one edition")
	}
	return out, nil
}

func updateAll(ctx context.Context, license string, targets []target) error {
	for i := range targets {
		if err := os.MkdirAll(filepath.Dir(targets[i].dest), 0o750); err != nil {
			cleanup(targets)
			return err
		}
		staged, err := downloadAndStage(ctx, license, targets[i].edition, filepath.Dir(targets[i].dest))
		if err != nil {
			cleanup(targets)
			return fmt.Errorf("%s: %w", targets[i].edition, err)
		}
		targets[i].staged = staged
	}
	defer cleanup(targets)

	// Every database is downloaded, fsynced, opened and verified before any live
	// file is replaced. Each final rename is atomic. A process/host crash between
	// the two renames may temporarily expose mixed build timestamps, which the API
	// tolerates and reports through per-database source versions.
	for _, t := range targets {
		if err := os.Rename(t.staged, t.dest); err != nil {
			return fmt.Errorf("replace %s: %w", t.edition, err)
		}
	}
	for _, dir := range uniqueDirs(targets) {
		f, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = f.Sync()
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("fsync directory %s: %w", dir, err)
		}
	}
	return nil
}

func downloadAndStage(ctx context.Context, license, edition, dir string) (string, error) {
	q := url.Values{"edition_id": {edition}, "license_key": {license}, "suffix": {"tar.gz"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://download.maxmind.com/app/geoip_download?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MaxMind returned %s", resp.Status)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", err
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
			return "", err
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != wanted {
			continue
		}
		tmp, err := os.CreateTemp(dir, "."+edition+"-*.mmdb")
		if err != nil {
			return "", err
		}
		name := tmp.Name()
		ok := false
		defer func() {
			if !ok {
				_ = os.Remove(name)
			}
		}()
		if _, err := io.Copy(tmp, io.LimitReader(tr, 256<<20)); err != nil {
			_ = tmp.Close()
			return "", err
		}
		if err := tmp.Chmod(0o640); err != nil {
			_ = tmp.Close()
			return "", err
		}
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			return "", err
		}
		if err := tmp.Close(); err != nil {
			return "", err
		}
		reader, err := maxminddb.Open(name)
		if err != nil {
			return "", fmt.Errorf("open staged database: %w", err)
		}
		verifyErr := reader.Verify()
		closeErr := reader.Close()
		if verifyErr != nil {
			return "", fmt.Errorf("verify staged database: %w", verifyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close staged database: %w", closeErr)
		}
		ok = true
		return name, nil
	}
	return "", fmt.Errorf("%s not found in MaxMind archive", wanted)
}

func cleanup(targets []target) {
	for _, t := range targets {
		if t.staged != "" {
			_ = os.Remove(t.staged)
		}
	}
}

func uniqueDirs(targets []target) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, t := range targets {
		dir := filepath.Dir(t.dest)
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func getenv(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}
