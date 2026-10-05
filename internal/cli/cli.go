// Package cli implements the hue command line interface.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/brybry192/hue/internal/config"
	"github.com/brybry192/hue/internal/hue"
)

// Version is overridden at build time with -ldflags.
var Version = "dev"

// ErrUsage signals that usage text has already been written and the process
// should exit non-zero without an extra error line.
var ErrUsage = errors.New("usage")

// App holds the CLI's dependencies so tests can substitute them.
type App struct {
	Out io.Writer
	Err io.Writer
	// Now defaults to time.Now.
	Now func() time.Time
}

// Run dispatches a command line. It returns an error for the caller to report.
func Run(args []string, out, err io.Writer) error {
	app := &App{Out: out, Err: err}
	return app.Run(args)
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

type command struct {
	name    string
	summary string
	run     func(*App, []string) error
}

func commands() []command {
	return []command{
		{"ls", "list rooms and zones with the devices in them", (*App).runList},
		{"sweep", "switch off lights that were left on", (*App).runSweep},
		{"on", "switch a room, zone or light on", (*App).runOn},
		{"off", "switch a room, zone or light off", (*App).runOff},
		{"status", "show bridge and light totals", (*App).runStatus},
		{"auth", "pair with a bridge and store its application key", (*App).runAuth},
		{"config", "show, locate or initialise the config file", (*App).runConfig},
		{"probe", "inventory the bridge's resource types, or dump one", (*App).runProbe},
		{"version", "print the version", (*App).runVersion},
	}
}

// Run executes one command.
func (a *App) Run(args []string) error {
	if len(args) == 0 {
		a.usage()
		return ErrUsage
	}
	name := args[0]
	switch name {
	case "-h", "--help", "help":
		a.usage()
		return nil
	case "-v", "--version":
		return a.runVersion(nil)
	case "list":
		name = "ls"
	}
	for _, c := range commands() {
		if c.name == name {
			return c.run(a, args[1:])
		}
	}
	fmt.Fprintf(a.Err, "hue: unknown command %q\n\n", args[0])
	a.usage()
	return ErrUsage
}

func (a *App) usage() {
	w := a.Err
	fmt.Fprint(w, `hue manages Philips Hue lights from the command line.

Usage:
  hue <command> [flags]

Commands:
`)
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-8s %s\n", c.name, c.summary)
	}
	fmt.Fprint(w, `
Run 'hue <command> -h' for the flags of a command.

Getting started:
  hue auth            pair with the bridge (press its round button first)
  hue ls              see the rooms, lights, switches and sensors
  hue sweep --dry-run see what a sweep would switch off

Environment:
  HUE_CONFIG          config file path
  HUE_STATE           sweep state file path
  HUE_BRIDGE_HOST     bridge address, overriding the config
  HUE_APP_KEY         bridge application key, overriding the config
`)
}

// newFlagSet builds a flag set that writes to the app's error stream and
// registers the --config flag shared by every bridge-touching command.
func (a *App) newFlagSet(name, usage string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.Err)
	cfgPath := fs.String("config", "", "config file path (default "+defaultConfigPathForHelp()+")")
	fs.Usage = func() {
		fmt.Fprintf(a.Err, "Usage: hue %s\n\n%s\n\nFlags:\n", usage, commandDoc(name))
		fs.PrintDefaults()
	}
	return fs, cfgPath
}

func defaultConfigPathForHelp() string {
	if p, err := config.DefaultPath(); err == nil {
		return p
	}
	return "~/.config/hue/config.json"
}

func commandDoc(name string) string {
	for _, c := range commands() {
		if c.name == name {
			return strings.ToUpper(name[:1]) + name[1:] + " " + c.summary + "."
		}
	}
	return ""
}

// parseArgs parses flags that may be interspersed with positional arguments.
//
// Go's flag package stops at the first non-flag argument, which would make
// `hue off Kitchen --no-dry-run` silently ignore the flag. Parsing repeatedly
// and collecting the positionals as they appear keeps both orders working.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		rest = fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
}

// loadConfig reads the config, reporting a helpful error when it is unusable.
func (a *App) loadConfig(path string) (config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}

// client builds a bridge client, insisting on a host and key first.
func (a *App) client(cfg config.Config) (*hue.Client, error) {
	if cfg.Bridge.Host == "" {
		return nil, errors.New("no bridge configured; run 'hue auth' (or set HUE_BRIDGE_HOST)")
	}
	if cfg.Bridge.AppKey == "" {
		return nil, errors.New("no application key configured; run 'hue auth' (or set HUE_APP_KEY)")
	}
	return hue.New(hue.Options{
		Host:       cfg.Bridge.Host,
		AppKey:     cfg.Bridge.AppKey,
		CertSHA256: cfg.Bridge.CertSHA256,
		Insecure:   cfg.Bridge.Insecure,
		Timeout:    cfg.Bridge.Timeout.Duration(),
	}), nil
}

// context returns a context cancelled on interrupt, so a sweep can be stopped
// cleanly mid-flight.
func (a *App) context() (context.Context, context.CancelFunc) {
	return signalContext()
}

func (a *App) runVersion(_ []string) error {
	fmt.Fprintf(a.Out, "hue %s\n", Version)
	return nil
}

// splitList parses a comma-separated flag value into trimmed, non-empty parts.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sortedNames is a small helper for deterministic output.
func sortedNames(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// isTTY reports whether w looks like a terminal, used only to decide whether
// to print progress chatter.
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
