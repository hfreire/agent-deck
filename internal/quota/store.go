package quota

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// Store holds the latest snapshot per provider and persists them so a restart
// paints the bar from the last known numbers instead of a blank line.
type Store struct {
	path  string
	mu    sync.RWMutex
	snaps map[string]Snapshot
}

func NewStore(path string) *Store {
	return &Store{path: path, snaps: make(map[string]Snapshot)}
}

// Set records a snapshot. A failed refresh (Err set, no windows) keeps the last
// good windows and their timestamp so the UI can dim rather than blank them.
func (s *Store) Set(snap Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if snap.Err != "" && snap.Session == nil && snap.Weekly == nil {
		if prev, ok := s.snaps[snap.Provider]; ok {
			prev.Err = snap.Err
			s.snaps[snap.Provider] = prev
			return
		}
	}
	s.snaps[snap.Provider] = snap
}

// Delete forgets a provider. Used when it turns out to be signed out, so the
// bar drops it instead of painting numbers from the last session forever.
func (s *Store) Delete(provider string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.snaps, provider)
}

func (s *Store) Get(provider string) (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.snaps[provider]
	return snap, ok
}

// All returns a copy of every stored snapshot.
func (s *Store) All() map[string]Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Snapshot, len(s.snaps))
	for k, v := range s.snaps {
		out[k] = v
	}
	return out
}

// Load reads the cache. A missing cache is the ordinary first-run state.
func (s *Store) Load() error {
	data, err := os.ReadFile(s.path) // #nosec G304 -- path is agent-deck's own cache file
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read quota cache: %w", err)
	}
	var snaps map[string]Snapshot
	if err := json.Unmarshal(data, &snaps); err != nil {
		// A corrupt cache is not worth failing over: it is rebuilt on the
		// next tick.
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.snaps = snaps
	return nil
}

func (s *Store) Save() error {
	s.mu.RLock()
	data, err := json.Marshal(s.snaps)
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("marshal quota cache: %w", err)
	}
	if err := atomicfile.WriteFile(s.path, data, 0o600); err != nil {
		return fmt.Errorf("write quota cache: %w", err)
	}
	return nil
}
