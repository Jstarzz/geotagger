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
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	license := os.Getenv("MAXMIND_LICENSE_KEY")
	if license == "" {
		logger.Error("MAXMIND_LICENSE_KEY is required")
		os.Exit(1)
	}
	edition := getenv("MAXMIND_EDITION", "GeoLite2-Country")
	dest := getenv("MMDB_PATH", "/data/GeoLite2-Country.mmdb")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := update(ctx, license, edition, dest); err != nil {
		logger.Error("MMDB update failed", "error", err)
		os.Exit(1)
	}
	logger.Info("MMDB updated", "edition", edition, "path", dest)
}

func update(ctx context.Context, license, edition, dest string) error {
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
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(dest), ".mmdb-*")
		if err != nil {
			return err
		}
		name := tmp.Name()
		defer os.Remove(name)
		if _, err := io.Copy(tmp, io.LimitReader(tr, 128<<20)); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Chmod(0o640); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		return os.Rename(name, dest)
	}
	return fmt.Errorf("%s not found in MaxMind archive", wanted)
}

func getenv(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}
