package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDurationJSON(t *testing.T) {
	t.Run("marshals as a readable string", func(t *testing.T) {
		got, err := json.Marshal(Duration(15 * time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `"15m0s"` {
			t.Errorf("marshalled %s, want \"15m0s\"", got)
		}
	})

	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: `"15m"`, want: 15 * time.Minute},
		{in: `"1h30m"`, want: 90 * time.Minute},
		{in: `"45s"`, want: 45 * time.Second},
		{in: `"  10m  "`, want: 10 * time.Minute},
		{in: `0`, want: 0},
		{in: `60000000000`, want: time.Minute},
		{in: `"nonsense"`, wantErr: true},
		{in: `"15"`, wantErr: true}, // no unit
		{in: `true`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			var d Duration
			err := json.Unmarshal([]byte(tc.in), &d)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parsed %s as %v, want an error", tc.in, d.Duration())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d.Duration() != tc.want {
				t.Errorf("parsed %s as %v, want %v", tc.in, d.Duration(), tc.want)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	c := Default()
	if got := c.Sweep.Motion.IdleThreshold.Duration(); got != 15*time.Minute {
		t.Errorf("idle threshold = %v, want 15m", got)
	}
	if got := c.Sweep.MinOnDuration.Duration(); got != 10*time.Minute {
		t.Errorf("min on duration = %v, want 10m", got)
	}
	if !c.Sweep.Motion.IsEnabled() || !c.Sweep.Outlier.IsEnabled() {
		t.Error("both rules should be enabled by default")
	}
	if c.Sweep.IncludePlugs {
		t.Error("plugs should be excluded by default")
	}
	if c.Sweep.Outlier.MinGroupSize != 3 || c.Sweep.Outlier.MaxOnCount != 2 {
		t.Errorf("unexpected outlier defaults: %+v", c.Sweep.Outlier)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("defaults should validate: %v", err)
	}
}

// clearEnv makes a test independent of the developer's own environment.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HUE_CONFIG", "HUE_BRIDGE_HOST", "HUE_APP_KEY", "XDG_CONFIG_HOME"} {
		t.Setenv(k, "")
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "absent.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("a missing config should not be an error: %v", err)
	}
	if cfg.Bridge.Host != "" {
		t.Errorf("host = %q, want empty", cfg.Bridge.Host)
	}
	if cfg.Sweep.Motion.IdleThreshold.Duration() != 15*time.Minute {
		t.Error("defaults were not applied")
	}
	if cfg.Path() != path {
		t.Errorf("Path() = %q, want %q", cfg.Path(), path)
	}
}

func TestLoadPartialFileKeepsDefaults(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// Only a couple of keys: everything else must fall back to the default.
	body := `{
	  "bridge": {"host": "192.0.2.10", "app_key": "abc123"},
	  "sweep": {"rooms": ["Kitchen", "Outside"], "motion": {"idle_threshold": "25m"}}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bridge.Host != "192.0.2.10" || cfg.Bridge.AppKey != "abc123" {
		t.Errorf("bridge = %+v", cfg.Bridge)
	}
	if got := cfg.Sweep.Motion.IdleThreshold.Duration(); got != 25*time.Minute {
		t.Errorf("idle threshold = %v, want 25m", got)
	}
	if got := cfg.Sweep.MinOnDuration.Duration(); got != 10*time.Minute {
		t.Errorf("min on duration = %v, want the 10m default", got)
	}
	if got := cfg.Sweep.Outlier.MaxOnCount; got != 2 {
		t.Errorf("max on count = %d, want the default 2", got)
	}
	if len(cfg.Sweep.Rooms) != 2 {
		t.Errorf("rooms = %v", cfg.Sweep.Rooms)
	}
}

func TestExplicitlyDisablingARule(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "config.json")
	// "enabled": false must be distinguishable from an absent key, which is
	// why the field is a pointer.
	body := `{"sweep": {"outlier": {"enabled": false}, "motion": {"enabled": false}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sweep.Outlier.IsEnabled() {
		t.Error("outlier rule should be disabled")
	}
	if cfg.Sweep.Motion.IsEnabled() {
		t.Error("motion rule should be disabled")
	}
}

func TestLoadRejectsBadConfig(t *testing.T) {
	clearEnv(t)
	tests := map[string]string{
		"malformed json":        `{"bridge":`,
		"bad duration":          `{"sweep": {"min_on_duration": "soon"}}`,
		"fraction above one":    `{"sweep": {"outlier": {"max_on_fraction": 1.5}}}`,
		"negative group size":   `{"sweep": {"outlier": {"min_group_size": -1}}}`,
		"negative grace period": `{"sweep": {"min_on_duration": "-5m"}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("expected an error for %s", name)
			}
		})
	}
}

func TestEnvOverridesFile(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"bridge": {"host": "10.0.0.1", "app_key": "fromfile"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUE_BRIDGE_HOST", "192.0.2.10")
	t.Setenv("HUE_APP_KEY", "fromenv")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bridge.Host != "192.0.2.10" {
		t.Errorf("host = %q, want the environment value", cfg.Bridge.Host)
	}
	if cfg.Bridge.AppKey != "fromenv" {
		t.Errorf("app key = %q, want the environment value", cfg.Bridge.AppKey)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "nested", "config.json")

	cfg := Default()
	cfg.Bridge.Host = "192.0.2.10"
	cfg.Bridge.AppKey = "secret-key"
	cfg.Bridge.CertSHA256 = "aabbcc"
	cfg.Sweep.Rooms = []string{"Kitchen"}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The file holds the application key, so it must not be world readable.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, want 600", perm)
	}

	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Bridge != cfg.Bridge {
		t.Errorf("bridge round trip: got %+v want %+v", again.Bridge, cfg.Bridge)
	}
	if len(again.Sweep.Rooms) != 1 || again.Sweep.Rooms[0] != "Kitchen" {
		t.Errorf("rooms round trip: %v", again.Sweep.Rooms)
	}
	if again.Sweep.Motion.IdleThreshold != cfg.Sweep.Motion.IdleThreshold {
		t.Errorf("idle threshold round trip: %v", again.Sweep.Motion.IdleThreshold)
	}
}

func TestSaveLeavesNoTempFileBehind(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := Default()
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temporary file %s was left behind", e.Name())
		}
	}
}

func TestDefaultPath(t *testing.T) {
	t.Run("HUE_CONFIG wins", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("HUE_CONFIG", "/tmp/custom/hue.json")
		got, err := DefaultPath()
		if err != nil {
			t.Fatal(err)
		}
		if got != "/tmp/custom/hue.json" {
			t.Errorf("path = %q", got)
		}
	})

	t.Run("XDG_CONFIG_HOME is honoured", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
		got, err := DefaultPath()
		if err != nil {
			t.Fatal(err)
		}
		if want := "/tmp/xdg/hue/config.json"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	})

	t.Run("falls back to the home directory", func(t *testing.T) {
		clearEnv(t)
		got, err := DefaultPath()
		if err != nil {
			t.Fatal(err)
		}
		home, _ := os.UserHomeDir()
		if want := filepath.Join(home, ".config", "hue", "config.json"); got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	})
}
