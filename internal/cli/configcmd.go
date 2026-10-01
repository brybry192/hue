package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/brybry192/hue/internal/config"
)

func (a *App) runConfig(args []string) error {
	fs, cfgPath := a.newFlagSet("config", "config [flags] <show|path|init>")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}

	sub := fs.Arg(0)
	if sub == "" {
		sub = "show"
	}

	switch sub {
	case "path":
		path := *cfgPath
		if path == "" {
			p, err := config.DefaultPath()
			if err != nil {
				return err
			}
			path = p
		}
		fmt.Fprintln(a.Out, path)
		return nil

	case "show":
		cfg, err := a.loadConfig(*cfgPath)
		if err != nil {
			return err
		}
		// Redact the key: this output gets pasted into issues and chats.
		if cfg.Bridge.AppKey != "" {
			cfg.Bridge.AppKey = redact(cfg.Bridge.AppKey)
		}
		enc := json.NewEncoder(a.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(cfg)

	case "init":
		return a.initConfig(*cfgPath)

	default:
		fs.Usage()
		return ErrUsage
	}
}

// initConfig writes a fully populated default config, preserving any bridge
// credentials that are already there.
func (a *App) initConfig(path string) error {
	if path == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return err
		}
		path = p
	}

	cfg := config.Default()
	if existing, err := config.Load(path); err == nil {
		cfg.Bridge = existing.Bridge
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; edit it directly or delete it first", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// Seed the room list as empty with a comment-free but discoverable shape:
	// an empty list means "every room", which is the safest default.
	if err := cfg.Save(path); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "wrote %s\n", path)
	fmt.Fprintln(a.Out, "set sweep.rooms to limit which rooms and zones get swept")
	return nil
}

func redact(s string) string {
	if len(s) <= 6 {
		return "****"
	}
	return s[:3] + "...." + s[len(s)-3:]
}
