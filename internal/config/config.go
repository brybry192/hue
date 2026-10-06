// Package config loads and saves the hue CLI's configuration file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Duration is a time.Duration that round-trips through JSON as a string such
// as "15m", so the config file stays readable.
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	// Accept both "15m" and a raw nanosecond count.
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		parsed, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("invalid duration %s: expected a string like \"15m\"", b)
	}
	*d = Duration(n)
	return nil
}

// Bridge holds how to reach and authenticate to one bridge.
type Bridge struct {
	// Name identifies the bridge in output and on the command line. It may
	// be empty when only one bridge is configured.
	Name   string `json:"name,omitempty"`
	Host   string `json:"host"`
	AppKey string `json:"app_key"`
	// CertSHA256 pins the bridge's TLS certificate, recorded at pairing time.
	CertSHA256 string `json:"cert_sha256,omitempty"`
	// Insecure skips certificate pinning.
	Insecure bool     `json:"insecure,omitempty"`
	Timeout  Duration `json:"timeout,omitempty"`
}

// Label is how the bridge is shown: its name, or its address when unnamed.
func (b Bridge) Label() string {
	if b.Name != "" {
		return b.Name
	}
	return b.Host
}

// Outlier configures the "one light left on in a group" rule.
type Outlier struct {
	// Enabled defaults to true when absent.
	Enabled *bool `json:"enabled,omitempty"`
	// MinGroupSize is the smallest group the rule will consider; a group of
	// two has no meaningful outlier.
	MinGroupSize int `json:"min_group_size"`
	// MaxOnCount is the most lights that may be on and still count as an
	// outlier.
	MaxOnCount int `json:"max_on_count"`
	// MaxOnFraction is the largest share of a group that may be on and still
	// count as an outlier, so big groups are not swept just because two
	// lights are on.
	MaxOnFraction float64 `json:"max_on_fraction"`
}

func (o Outlier) IsEnabled() bool { return o.Enabled == nil || *o.Enabled }

// Motion configures the motion-sensor rule.
type Motion struct {
	// Enabled defaults to true when absent.
	Enabled *bool `json:"enabled,omitempty"`
	// IdleThreshold is how long a room must be free of motion before its
	// lights may be switched off.
	IdleThreshold Duration `json:"idle_threshold"`
}

func (m Motion) IsEnabled() bool { return m.Enabled == nil || *m.Enabled }

// Sweep configures the sweep subcommand.
type Sweep struct {
	// Rooms limits the sweep to these rooms or zones, by name. Empty means
	// every room and zone.
	Rooms []string `json:"rooms"`
	// ExcludeLights never get switched off, matched by light or device name.
	ExcludeLights []string `json:"exclude_lights,omitempty"`
	// IncludePlugs allows smart plugs to be switched off. On by default; turn
	// it off, or list the plug in exclude_lights, if it powers something
	// that must stay on.
	IncludePlugs bool    `json:"include_plugs"`
	Outlier      Outlier `json:"outlier"`
	Motion       Motion  `json:"motion"`
}

// Config is the whole config file.
type Config struct {
	// Bridges lists every bridge. Rooms and zones with the same name on
	// different bridges are treated as one, so lights on one bridge can be
	// decided by motion sensors on another.
	Bridges []Bridge `json:"bridges"`
	// RoomAliases renames rooms and zones before bridges are merged, for
	// rooms named differently on each: {"Yard": "Terrace"}. Matching is
	// case-insensitive.
	RoomAliases map[string]string `json:"room_aliases,omitempty"`
	Sweep       Sweep             `json:"sweep"`

	// LegacyBridge is the single "bridge" key of older config files. Load
	// moves it into Bridges, so it is never set after loading and is never
	// written back.
	LegacyBridge *Bridge `json:"bridge,omitempty"`

	// path records where this config was loaded from. Unexported, so it is
	// never serialised.
	path string
}

// Path reports the file this config was loaded from, if any.
func (c *Config) Path() string { return c.path }

func boolPtr(b bool) *bool { return &b }

// Default returns the configuration used when the file is absent or partial.
func Default() Config {
	return Config{
		Bridges: []Bridge{},
		Sweep: Sweep{
			Rooms:        []string{},
			IncludePlugs: true,
			Outlier: Outlier{
				Enabled:       boolPtr(true),
				MinGroupSize:  3,
				MaxOnCount:    2,
				MaxOnFraction: 0.34,
			},
			Motion: Motion{
				Enabled:       boolPtr(true),
				IdleThreshold: Duration(15 * time.Minute),
			},
		},
	}
}

// DefaultPath is the config file location, honouring XDG_CONFIG_HOME.
func DefaultPath() (string, error) {
	if p := os.Getenv("HUE_CONFIG"); p != "" {
		return p, nil
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "hue", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".config", "hue", "config.json"), nil
}

// Load reads the config at path, falling back to DefaultPath when empty.
//
// A missing file is not an error: the defaults are returned so that commands
// can give a useful "run hue auth" message instead of a stat error.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return cfg, err
		}
		path = p
	}
	cfg.path = path

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := applyEnv(&cfg); err != nil {
			return cfg, err
		}
		return cfg, nil
	case err != nil:
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}

	// Unmarshalling over the defaults leaves absent keys at their default.
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.path = path
	if err := cfg.migrateLegacyBridge(); err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	if err := applyEnv(&cfg); err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// migrateLegacyBridge moves an old single "bridge" entry into Bridges.
func (c *Config) migrateLegacyBridge() error {
	legacy := c.LegacyBridge
	c.LegacyBridge = nil
	if legacy == nil || (legacy.Host == "" && legacy.AppKey == "") {
		return nil
	}
	if len(c.Bridges) > 0 {
		return errors.New(`both "bridge" and "bridges" are set; move the bridge into the "bridges" list`)
	}
	c.Bridges = []Bridge{*legacy}
	return nil
}

// applyEnv lets environment variables override the file, which is handy for
// one-off runs and for keeping the key out of the file entirely. With several
// bridges configured there is no telling which one they mean, so they are
// refused.
func applyEnv(cfg *Config) error {
	host := strings.TrimSpace(os.Getenv("HUE_BRIDGE_HOST"))
	key := strings.TrimSpace(os.Getenv("HUE_APP_KEY"))
	if host == "" && key == "" {
		return nil
	}
	switch len(cfg.Bridges) {
	case 0:
		cfg.Bridges = []Bridge{{}}
	case 1:
	default:
		return fmt.Errorf("HUE_BRIDGE_HOST and HUE_APP_KEY cannot be used with %d bridges configured", len(cfg.Bridges))
	}
	if host != "" {
		cfg.Bridges[0].Host = host
	}
	if key != "" {
		cfg.Bridges[0].AppKey = key
	}
	return nil
}

// FindBridge returns the index of the bridge with the given name or host,
// ignoring case, or -1.
func (c *Config) FindBridge(nameOrHost string) int {
	want := strings.ToLower(strings.TrimSpace(nameOrHost))
	if want == "" {
		return -1
	}
	for i, b := range c.Bridges {
		if strings.ToLower(b.Name) == want || strings.ToLower(b.Host) == want {
			return i
		}
	}
	return -1
}

// Validate checks values that would otherwise fail confusingly later.
func (c *Config) Validate() error {
	// Several bridges need names, since output and --bridge refer to them.
	if len(c.Bridges) > 1 {
		seen := make(map[string]bool, len(c.Bridges))
		for i, b := range c.Bridges {
			name := strings.ToLower(strings.TrimSpace(b.Name))
			if name == "" {
				return fmt.Errorf("bridges[%d] (%s) needs a name when several bridges are configured", i, b.Host)
			}
			if seen[name] {
				return fmt.Errorf("bridge name %q is used twice", b.Name)
			}
			seen[name] = true
		}
	}
	s := c.Sweep
	if s.Outlier.MinGroupSize < 0 {
		return errors.New("sweep.outlier.min_group_size must not be negative")
	}
	if s.Outlier.MaxOnCount < 0 {
		return errors.New("sweep.outlier.max_on_count must not be negative")
	}
	if s.Outlier.MaxOnFraction < 0 || s.Outlier.MaxOnFraction > 1 {
		return errors.New("sweep.outlier.max_on_fraction must be between 0 and 1")
	}
	if s.Motion.IdleThreshold < 0 {
		return errors.New("sweep.motion.idle_threshold must not be negative")
	}
	return nil
}

// Save writes the config to path (or its loaded path) with owner-only
// permissions, since it holds the application key.
func (c *Config) Save(path string) error {
	if path == "" {
		path = c.path
	}
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return err
		}
		path = p
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')

	// Write via a temporary file so an interrupted save cannot truncate a
	// working config.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace config: %w", err)
	}
	c.path = path
	return nil
}
