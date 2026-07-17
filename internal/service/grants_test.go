package service

import (
	"os"
	"path/filepath"
	"testing"
)

func grantsPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "fs-grants.json")
}

func TestLoadGrantStoreMissingFile(t *testing.T) {
	g, err := LoadGrantStore(grantsPath(t))
	if err != nil {
		t.Fatalf("load missing file: %v", err)
	}
	if len(g.Dirs()) != 0 {
		t.Fatalf("expected empty store, got %v", g.Dirs())
	}
}

func TestLoadGrantStoreCorruptJSON(t *testing.T) {
	p := grantsPath(t)
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGrantStore(p); err == nil {
		t.Fatal("expected error for corrupt JSON")
	}
}

func TestGrantStoreAddPersistsAndReloads(t *testing.T) {
	p := grantsPath(t)
	granted := t.TempDir()

	g, err := LoadGrantStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Add(granted); err != nil {
		t.Fatalf("add: %v", err)
	}

	g2, err := LoadGrantStore(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !g2.Contains(filepath.Join(granted, "sub", "file.txt")) {
		t.Fatalf("reloaded store should contain child of %q", granted)
	}
}

func TestGrantStoreContains(t *testing.T) {
	g, err := LoadGrantStore(grantsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "work")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := g.Add(dir); err != nil {
		t.Fatal(err)
	}

	if !g.Contains(dir) {
		t.Error("exact granted dir should match")
	}
	if !g.Contains(filepath.Join(dir, "a", "b.txt")) {
		t.Error("child should match")
	}
	if g.Contains(dir + "-evil") {
		t.Error("sibling prefix must not match")
	}
	if g.Contains(filepath.Dir(dir)) {
		t.Error("parent must not match")
	}
}

func TestGrantStoreNilSafe(t *testing.T) {
	var g *GrantStore
	if g.Contains(`C:\anything`) {
		t.Error("nil store must contain nothing")
	}
	if g.Dirs() != nil {
		t.Error("nil store dirs must be nil")
	}
}

func TestGrantStoreDedupe(t *testing.T) {
	g, err := LoadGrantStore(grantsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := g.Add(dir); err != nil {
		t.Fatal(err)
	}
	if err := g.Add(dir); err != nil {
		t.Fatal(err)
	}
	if err := g.Add(filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	if len(g.Dirs()) != 1 {
		t.Fatalf("expected 1 grant after dedupe, got %v", g.Dirs())
	}
}
