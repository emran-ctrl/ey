// Package state persists alert-policy state across process restarts so that
// "once" and "cooldown" modes do not re-fire after a daemon restart or between
// systemd timer invocations.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"ey/internal/fsutil"
)

// State is the persisted alert-policy snapshot. An empty LastSent means no
// alert has ever been delivered.
type State struct {
	LastSent time.Time `json:"last_sent,omitzero"`
	Armed    bool      `json:"armed"`
}

// Store is a lazily-flushed, atomic JSON state file. A nil *Store is valid and
// disables persistence.
type Store struct {
	path   string
	logger *slog.Logger

	mu    sync.Mutex
	state State
	dirty bool
}

// DefaultPath returns the per-user state file location:
// $XDG_STATE_HOME/ey/state.json on Linux, otherwise inside the user config dir.
func DefaultPath() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "ey", "state.json"), nil
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "freebsd" || runtime.GOOS == "openbsd" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "state", "ey", "state.json"), nil
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "ey", "state.json"), nil
	}
	return "", errors.New("cannot determine a state directory")
}

// Open loads the state file at path, starting from zero value if it does not
// exist or cannot be parsed. A nil logger falls back to slog.Default.
func Open(path string, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{path: path, logger: logger, state: State{Armed: true}}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s
	}
	if err != nil {
		logger.Warn("cannot read state file; starting fresh", "path", path, "err", err)
		return s
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		logger.Warn("corrupt state file; starting fresh", "path", path, "err", err)
		s.state = State{Armed: true}
	}
	return s
}

// Snapshot returns a copy of the current state.
func (s *Store) Snapshot() State {
	if s == nil {
		return State{Armed: true}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Set updates the state, marking it dirty only when a value actually changes.
func (s *Store) Set(lastSent time.Time, armed bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.LastSent.Equal(lastSent) && s.state.Armed == armed {
		return
	}
	s.state.LastSent = lastSent
	s.state.Armed = armed
	s.dirty = true
}

// Dirty reports whether there are unsaved changes.
func (s *Store) Dirty() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dirty
}

// Save atomically writes the state to disk if there are unsaved changes.
func (s *Store) Save() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}

	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := fsutil.WriteFileAtomic(s.path, data, 0o600); err != nil {
		return err
	}
	s.dirty = false
	s.logger.Debug("state saved", "path", s.path)
	return nil
}
