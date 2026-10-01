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

// Bridge holds how to reach and authenticate to the bridge.
type Bridge struct {
	Host   string `json:"host"`
	AppKey string `json:"app_key"`
	// CertSHA256 pins the bridge's TLS certificate, recorded at pairing time.
	CertSHA256 string `json:"cert_sha256,omitempty"`
	// Insecure skips certificate pinning.
	Insecure bool     `json:"insecure,omitempty"`
	Timeout  Duration `json:"timeout,omitempty"`
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
	// MinOnDuration is how long a light must have been observed on before the
	// sweep will switch it off. It stops the sweep fighting someone who just
	// turned a light on, at the cost of needing two runs to act.
	MinOnDuration Duration `json:"min_on_duration"`
	// IncludePlugs allows smart plugs to be switched off. Off by default,
	// since a plug may be powering something that is not a lamp.
	IncludePlugs bool    `json:"include_plugs"`
	Outlier      Outlier `json:"outlier"`
	Motion       Motion  `json:"motion"`
}

// Config is the whole config file.
type Config struct {
	Bridge Bridge `json:"bridge"`
	Sweep  Sweep  `json:"sweep"`

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
		Bridge: Bridge{
			Timeout: Duration(10 * time.Second),
		},
		Sweep: Sweep{
			Rooms:         []string{},
			MinOnDuration: Duration(10 * time.Minute),
			IncludePlugs:  false,
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
		applyEnv(&cfg)
		return cfg, nil
	case err != nil:
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}

	// Unmarshalling over the defaults leaves absent keys at their default.
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.path = path
	applyEnv(&cfg)
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// applyEnv lets environment variables override the file, which is handy for
// one-off runs and for keeping the key out of the file entirely.
func applyEnv(cfg *Config) {
	if v := strings.TrimSpace(os.Getenv("HUE_BRIDGE_HOST")); v != "" {
		cfg.Bridge.Host = v
	}
	if v := strings.TrimSpace(os.Getenv("HUE_APP_KEY")); v != "" {
		cfg.Bridge.AppKey = v
	}
}

// Validate checks values that would otherwise fail confusingly later.
func (c *Config) Validate() error {
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
	if s.MinOnDuration < 0 {
		return errors.New("sweep.min_on_duration must not be negative")
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
