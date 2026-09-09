package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestrictDBPermissionsErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	if err := restrictDBPermissions(path); err == nil {
		t.Fatal("missing primary database must fail")
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := restrictDBPermissions(path); err != nil {
		t.Fatalf("absent sidecars: %v", err)
	}
	// A symlink loop deterministically fails chmod even when tests run as root.
	if err := os.Symlink(path+"-wal", path+"-wal"); err != nil {
		t.Fatal(err)
	}
	if err := restrictDBPermissions(path); err == nil {
		t.Fatal("sidecar chmod error ignored")
	}
	if db, err := Open(path); err == nil {
		db.Close()
		t.Fatal("Open ignored permission failure")
	}
}
