package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseEditions(t *testing.T) {
	got, err := parseEditions(" GeoLite2-City, GeoLite2-ASN ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"GeoLite2-City", "GeoLite2-ASN"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestParseEditionsRejectsDuplicatesAndEmpty(t *testing.T) {
	for _, input := range []string{"", " , ", "GeoLite2-City,GeoLite2-City"} {
		if _, err := parseEditions(input); err == nil {
			t.Fatalf("expected error for %q", input)
		}
	}
}

func TestPruneReleasesKeepsNewestAndHandlesSmallSet(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"release-20260911T000000.000000000Z-a",
		"release-20260912T000000.000000000Z-b",
	} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	// Must not panic or remove anything when fewer releases exist than the
	// retention count.
	if err := pruneReleases(dir, 3); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(entries))
	}

	for _, name := range []string{
		"release-20260913T000000.000000000Z-c",
		"release-20260914T000000.000000000Z-d",
	} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneReleases(dir, 3); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 releases after prune, got %d", len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, "release-20260911T000000.000000000Z-a")); !os.IsNotExist(err) {
		t.Fatalf("oldest release was not removed; stat err=%v", err)
	}
}

func TestPruneReleasesRejectsNegativeRetention(t *testing.T) {
	if err := pruneReleases(t.TempDir(), -1); err == nil {
		t.Fatal("expected negative retention error")
	}
}
