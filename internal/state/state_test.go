package state

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDefaultPathHonorsXDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	want := filepath.Join(dir, "ey", "state.json")
	if got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}
}

func TestDefaultPathFallback(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if filepath.Base(got) != "state.json" {
		t.Errorf("DefaultPath = %q, want .../state.json", got)
	}
}

func TestOpenMissingStartsFresh(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "nope.json"), testLogger())
	snap := s.Snapshot()
	if !snap.Armed {
		t.Error("fresh state must be armed")
	}
	if !snap.LastSent.IsZero() {
		t.Errorf("fresh LastSent = %s, want zero", snap.LastSent)
	}
}

func TestOpenCorruptStartsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(path, testLogger())
	if !s.Snapshot().Armed {
		t.Error("corrupt state must recover to armed=true")
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	sent := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	s := Open(path, testLogger())
	s.Set(sent, false)
	if !s.Dirty() {
		t.Error("Set with a change must mark dirty")
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("state file perm = %o, want 600", perm)
	}

	reopened := Open(path, testLogger()).Snapshot()
	if !reopened.LastSent.Equal(sent) {
		t.Errorf("LastSent = %s, want %s", reopened.LastSent, sent)
	}
	if reopened.Armed {
		t.Error("Armed = true, want false")
	}
}

func TestDirtyOnlyOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := Open(path, testLogger())

	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Save with no changes must not create a file")
	}

	s.Set(time.Time{}, true) // matches the default state
	if s.Dirty() {
		t.Error("setting identical state must not mark dirty")
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := Open(path, testLogger())
	s.Set(time.Now(), true)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("dir contains %v, want only state.json", names)
	}
}

func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	if !s.Snapshot().Armed {
		t.Error("nil store snapshot must be armed")
	}
	s.Set(time.Now(), false)
	if s.Dirty() {
		t.Error("nil store must never be dirty")
	}
	if err := s.Save(); err != nil {
		t.Errorf("nil store Save = %v, want nil", err)
	}
}
