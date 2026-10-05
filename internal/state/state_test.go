package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brybry192/hue/internal/hue"
)

var base = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func light(id string, on bool) hue.LightView {
	return hue.LightView{ID: id, Name: "Light " + id, Kind: hue.KindLight, On: on}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HUE_STATE", "")
	t.Setenv("XDG_STATE_HOME", "")
	s, err := Load(filepath.Join(t.TempDir(), "sweep-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestObserveRecordsFirstSighting(t *testing.T) {
	s := newStore(t)
	s.Observe([]hue.LightView{light("a", true), light("b", false)}, base)

	got, ok := s.OnSince("a")
	if !ok {
		t.Fatal("light a should be recorded as on")
	}
	if !got.Equal(base) {
		t.Errorf("OnSince = %v, want %v", got, base)
	}
	if _, ok := s.OnSince("b"); ok {
		t.Error("light b is off and should not be recorded")
	}
	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1", s.Len())
	}
}

func TestObserveKeepsTheOriginalOnSince(t *testing.T) {
	s := newStore(t)
	s.Observe([]hue.LightView{light("a", true)}, base)
	// Seen again ten minutes later: the start time must not move, otherwise
	// the grace period would never elapse.
	s.Observe([]hue.LightView{light("a", true)}, base.Add(10*time.Minute))

	got, ok := s.OnSince("a")
	if !ok {
		t.Fatal("light a should still be recorded")
	}
	if !got.Equal(base) {
		t.Errorf("OnSince = %v, want the original %v", got, base)
	}
}

func TestSwitchingOffResetsTheTimer(t *testing.T) {
	s := newStore(t)
	s.Observe([]hue.LightView{light("a", true)}, base)
	s.Observe([]hue.LightView{light("a", false)}, base.Add(5*time.Minute))
	if _, ok := s.OnSince("a"); ok {
		t.Fatal("an off light should be forgotten")
	}

	later := base.Add(10 * time.Minute)
	s.Observe([]hue.LightView{light("a", true)}, later)
	got, ok := s.OnSince("a")
	if !ok {
		t.Fatal("light a should be recorded again")
	}
	if !got.Equal(later) {
		t.Errorf("OnSince = %v, want the new %v", got, later)
	}
}

func TestObserveForgetsUnreachableLights(t *testing.T) {
	s := newStore(t)
	s.Observe([]hue.LightView{light("a", true)}, base)

	// Power cut: the bridge still says "on", but it cannot reach the light.
	dead := light("a", true)
	dead.Unreachable = true
	s.Observe([]hue.LightView{dead}, base.Add(5*time.Minute))
	if _, ok := s.OnSince("a"); ok {
		t.Fatal("an unreachable light should be forgotten")
	}

	// Power back: the grace period starts again from now.
	back := base.Add(30 * time.Minute)
	s.Observe([]hue.LightView{light("a", true)}, back)
	if got, _ := s.OnSince("a"); !got.Equal(back) {
		t.Errorf("OnSince = %v, want %v", got, back)
	}
}

func TestObserveForgetsLightsThatVanish(t *testing.T) {
	s := newStore(t)
	s.Observe([]hue.LightView{light("a", true), light("b", true)}, base)
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want 2", s.Len())
	}
	// Light b has been removed from the bridge entirely.
	s.Observe([]hue.LightView{light("a", true)}, base.Add(time.Minute))
	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1", s.Len())
	}
	if _, ok := s.OnSince("b"); ok {
		t.Error("a light missing from the bridge should be dropped")
	}
}

func TestForget(t *testing.T) {
	s := newStore(t)
	s.Observe([]hue.LightView{light("a", true)}, base)
	s.Forget("a")
	if _, ok := s.OnSince("a"); ok {
		t.Error("Forget should drop the entry")
	}
	// Forgetting something unknown must not panic.
	s.Forget("nope")
}

func TestSaveAndReload(t *testing.T) {
	t.Setenv("HUE_STATE", "")
	t.Setenv("XDG_STATE_HOME", "")
	path := filepath.Join(t.TempDir(), "nested", "sweep-state.json")

	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Observe([]hue.LightView{light("a", true), light("b", true)}, base)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, want 600", perm)
	}

	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Len() != 2 {
		t.Fatalf("Len after reload = %d, want 2", again.Len())
	}
	got, ok := again.OnSince("a")
	if !ok || !got.Equal(base) {
		t.Errorf("OnSince after reload = %v (ok=%v), want %v", got, ok, base)
	}
	if again.Path() != path {
		t.Errorf("Path = %q, want %q", again.Path(), path)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	t.Setenv("HUE_STATE", "")
	t.Setenv("XDG_STATE_HOME", "")
	s, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing state file should not be an error: %v", err)
	}
	if s.Len() != 0 {
		t.Errorf("Len = %d, want 0", s.Len())
	}
}

func TestLoadCorruptFileStartsFresh(t *testing.T) {
	t.Setenv("HUE_STATE", "")
	t.Setenv("XDG_STATE_HOME", "")
	path := filepath.Join(t.TempDir(), "sweep-state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Losing this state is harmless, so a corrupt file must not break a sweep.
	s, err := Load(path)
	if err != nil {
		t.Fatalf("a corrupt state file should not be an error: %v", err)
	}
	if s.Len() != 0 {
		t.Errorf("Len = %d, want 0", s.Len())
	}
	s.Observe([]hue.LightView{light("a", true)}, base)
	if err := s.Save(); err != nil {
		t.Fatalf("saving over a corrupt file should work: %v", err)
	}
}

func TestDefaultPath(t *testing.T) {
	t.Run("HUE_STATE wins", func(t *testing.T) {
		t.Setenv("HUE_STATE", "/tmp/custom/state.json")
		got, err := DefaultPath()
		if err != nil {
			t.Fatal(err)
		}
		if got != "/tmp/custom/state.json" {
			t.Errorf("path = %q", got)
		}
	})

	t.Run("XDG_STATE_HOME is honoured", func(t *testing.T) {
		t.Setenv("HUE_STATE", "")
		t.Setenv("XDG_STATE_HOME", "/tmp/xdgstate")
		got, err := DefaultPath()
		if err != nil {
			t.Fatal(err)
		}
		if want := "/tmp/xdgstate/hue/sweep-state.json"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	})

	t.Run("falls back under the home directory", func(t *testing.T) {
		t.Setenv("HUE_STATE", "")
		t.Setenv("XDG_STATE_HOME", "")
		got, err := DefaultPath()
		if err != nil {
			t.Fatal(err)
		}
		home, _ := os.UserHomeDir()
		if want := filepath.Join(home, ".local", "state", "hue", "sweep-state.json"); got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	})
}
