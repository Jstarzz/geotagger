package maxmindgeo

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStampDetectsRollbackGeneration(t *testing.T) {
	newer := fileStamp{resolvedPath: "/data/releases/release-new/GeoLite2-City.mmdb", modTime: time.Unix(200, 0), size: 10}
	older := fileStamp{resolvedPath: "/data/releases/release-old/GeoLite2-City.mmdb", modTime: time.Unix(100, 0), size: 10}
	if !older.changed(newer) {
		t.Fatal("switching to an older release path must still be detected as a change")
	}
}

func TestSnapshotTracksAtomicBundleSymlink(t *testing.T) {
	root := t.TempDir()
	releases := filepath.Join(root, "releases")
	if err := os.MkdirAll(releases, 0o755); err != nil {
		t.Fatal(err)
	}

	oldRelease := filepath.Join(releases, "release-old")
	newRelease := filepath.Join(releases, "release-new")
	for _, dir := range []string{oldRelease, newRelease} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"GeoLite2-City.mmdb", "GeoLite2-ASN.mmdb"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Make the rollback target intentionally older. Detection must be based on
	// generation identity as well as time, not ModTime().After(previous).
	oldTime := time.Now().Add(-24 * time.Hour)
	newTime := time.Now()
	for _, name := range []string{"GeoLite2-City.mmdb", "GeoLite2-ASN.mmdb"} {
		if err := os.Chtimes(filepath.Join(oldRelease, name), oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(newRelease, name), newTime, newTime); err != nil {
			t.Fatal(err)
		}
	}

	current := filepath.Join(root, "current")
	if err := os.Symlink(filepath.Join("releases", "release-new"), current); err != nil {
		t.Fatal(err)
	}
	m := &MaxMind{
		cityPath: filepath.Join(current, "GeoLite2-City.mmdb"),
		asnPath:  filepath.Join(current, "GeoLite2-ASN.mmdb"),
	}

	newCity, newASN, err := m.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(newCity.resolvedPath) != newRelease || filepath.Dir(newASN.resolvedPath) != newRelease {
		t.Fatalf("new snapshot is not coherent: city=%q asn=%q", newCity.resolvedPath, newASN.resolvedPath)
	}

	// Replace the bundle pointer the same way the updater does: create another
	// symlink and atomically rename it over current.
	tmpLink := filepath.Join(root, ".current-rollback")
	if err := os.Symlink(filepath.Join("releases", "release-old"), tmpLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpLink, current); err != nil {
		t.Fatal(err)
	}

	oldCity, oldASN, err := m.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(oldCity.resolvedPath) != oldRelease || filepath.Dir(oldASN.resolvedPath) != oldRelease {
		t.Fatalf("rollback snapshot is not coherent: city=%q asn=%q", oldCity.resolvedPath, oldASN.resolvedPath)
	}
	if !oldCity.changed(newCity) || !oldASN.changed(newASN) {
		t.Fatal("rollback to older bundle generation was not detected")
	}
}
