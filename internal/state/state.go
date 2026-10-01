// Package state persists what the sweep saw last time it ran.
//
// The Hue API exposes no "when did this light turn on" timestamp, so the only
// way to know a light has been on for a while is to remember seeing it on.
// That memory is what lets the sweep leave a just-switched-on light alone.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/brybry192/hue/internal/hue"
)

// LightState is what we remember about one light.
type LightState struct {
	Name string `json:"name"`
	// OnSince is when the light was first observed on in an unbroken run of
	// observations.
	OnSince time.Time `json:"on_since"`
	// LastSeen is the most recent observation that found it on.
	LastSeen time.Time `json:"last_seen"`
}

// data is the on-disk format.
type data struct {
	Version   int                   `json:"version"`
	UpdatedAt time.Time             `json:"updated_at"`
	Lights    map[string]LightState `json:"lights"`
}

const currentVersion = 1

// Store is a loaded state file.
type Store struct {
	path string
	d    data
}

// DefaultPath is the state file location, honouring XDG_STATE_HOME.
func DefaultPath() (string, error) {
	if p := os.Getenv("HUE_STATE"); p != "" {
		return p, nil
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "hue", "sweep-state.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "hue", "sweep-state.json"), nil
}

// Load reads the state file, falling back to DefaultPath when path is empty.
// A missing or unreadable-but-corrupt file yields an empty store rather than
// an error, since losing this state is harmless.
func Load(path string) (*Store, error) {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	s := &Store{
		path: path,
		d:    data{Version: currentVersion, Lights: map[string]LightState{}},
	}

	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}

	var loaded data
	if err := json.Unmarshal(raw, &loaded); err != nil {
		// Treat a corrupt file as a fresh start; the next save rewrites it.
		return s, nil
	}
	if loaded.Lights != nil {
		s.d.Lights = loaded.Lights
	}
	s.d.UpdatedAt = loaded.UpdatedAt
	return s, nil
}

// Path reports the backing file.
func (s *Store) Path() string { return s.path }

// Observe records the current on/off state of every light.
//
// Lights that are on gain an OnSince on first sight and keep it afterwards.
// Lights that are off are forgotten, so the next time one comes on its timer
// restarts.
func (s *Store) Observe(lights []hue.LightView, now time.Time) {
	seen := make(map[string]bool, len(lights))
	for _, l := range lights {
		seen[l.ID] = true
		if !l.On {
			delete(s.d.Lights, l.ID)
			continue
		}
		entry, ok := s.d.Lights[l.ID]
		if !ok || entry.OnSince.IsZero() {
			entry.OnSince = now
		}
		entry.Name = l.Name
		entry.LastSeen = now
		s.d.Lights[l.ID] = entry
	}
	// Drop lights that no longer exist on the bridge.
	for id := range s.d.Lights {
		if !seen[id] {
			delete(s.d.Lights, id)
		}
	}
	s.d.UpdatedAt = now
}

// OnSince reports when a light was first observed on. ok is false when the
// light has no recorded history, which means this is the first sweep to see it
// on.
func (s *Store) OnSince(lightID string) (time.Time, bool) {
	entry, ok := s.d.Lights[lightID]
	if !ok || entry.OnSince.IsZero() {
		return time.Time{}, false
	}
	return entry.OnSince, true
}

// Len reports how many lights are currently remembered as on.
func (s *Store) Len() int { return len(s.d.Lights) }

// Forget drops a light's history, used after switching it off so its timer
// restarts cleanly.
func (s *Store) Forget(lightID string) { delete(s.d.Lights, lightID) }

// Save writes the state file atomically.
func (s *Store) Save() error {
	if s.path == "" {
		return errors.New("no state path configured")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	s.d.Version = currentVersion
	raw, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	raw = append(raw, '\n')

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}
