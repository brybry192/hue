package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/brybry192/hue/internal/config"
	"github.com/brybry192/hue/internal/hue"
)

// pairPoll is how often CreateAppKey is retried while waiting for the link
// button, and how long the wait lasts by default.
const (
	pairPoll        = 2 * time.Second
	defaultPairWait = 60 * time.Second
)

func (a *App) runAuth(args []string) error {
	fs, cfgPath := a.newFlagSet("auth", "auth [flags]")
	addr := fs.String("bridge", "", "bridge address; discovered automatically when omitted")
	name := fs.String("name", "", "name for this bridge; required to add a second bridge")
	wait := fs.Duration("wait", defaultPairWait, "how long to wait for the link button to be pressed")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}

	cfg, err := a.loadConfig(*cfgPath)
	if err != nil {
		return err
	}

	// Work out which configured bridge, if any, this pairing replaces.
	host := hue.NormalizeHost(*addr)
	idx := -1
	switch {
	case *name != "":
		idx = cfg.FindBridge(*name)
		if idx < 0 && host != "" {
			idx = cfg.FindBridge(host)
		}
	case host != "":
		idx = cfg.FindBridge(host)
	case len(cfg.Bridges) == 1:
		idx = 0
	case len(cfg.Bridges) > 1:
		return fmt.Errorf("%d bridges are configured; choose one with --name", len(cfg.Bridges))
	}

	entry := config.Bridge{Name: strings.TrimSpace(*name)}
	if idx >= 0 {
		entry = cfg.Bridges[idx]
		if *name != "" {
			entry.Name = strings.TrimSpace(*name)
		}
		if host == "" {
			host = entry.Host
		}
	} else if len(cfg.Bridges) > 0 {
		// Adding another bridge: every bridge needs a name to tell them apart.
		if entry.Name == "" {
			return errors.New("a bridge is already configured; name this one with --name to add it alongside")
		}
		for _, b := range cfg.Bridges {
			if b.Name == "" {
				return fmt.Errorf("the bridge at %s has no name; add a \"name\" to it in %s first", b.Host, cfg.Path())
			}
		}
	}

	ctx, cancel := a.context()
	defer cancel()

	if host == "" {
		host, err = a.discoverBridge(ctx)
		if err != nil {
			return err
		}
	}
	fmt.Fprintf(a.Out, "using bridge at %s\n", host)

	// Record the bridge's certificate so later requests can be pinned to it.
	fingerprint, err := hue.Fingerprint(ctx, host, entry.Timeout.Duration())
	if err != nil {
		return err
	}

	client := hue.New(hue.Options{
		Host:       host,
		CertSHA256: fingerprint,
		Timeout:    entry.Timeout.Duration(),
	})

	key, err := a.pair(ctx, client, *wait)
	if err != nil {
		return err
	}

	entry.Host = host
	entry.AppKey = key
	entry.CertSHA256 = fingerprint
	if idx >= 0 {
		cfg.Bridges[idx] = entry
	} else {
		cfg.Bridges = append(cfg.Bridges, entry)
	}
	if err := cfg.Save(*cfgPath); err != nil {
		return err
	}

	fmt.Fprintf(a.Out, "paired; application key saved to %s\n", cfg.Path())
	fmt.Fprintln(a.Out, "run 'hue ls' to see your rooms")
	return nil
}

// pair polls the bridge until the link button is pressed or the wait expires.
func (a *App) pair(ctx context.Context, client *hue.Client, wait time.Duration) (string, error) {
	// Try once before nagging, in case the button was pressed already.
	key, err := client.CreateAppKey(ctx, deviceType())
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, hue.ErrLinkButton) {
		return "", err
	}

	fmt.Fprintf(a.Out, "press the round button on the bridge (waiting up to %s)...\n", hue.ShortDuration(wait))
	deadline := time.Now().Add(wait)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pairPoll):
		}

		key, err := client.CreateAppKey(ctx, deviceType())
		if err == nil {
			return key, nil
		}
		if !errors.Is(err, hue.ErrLinkButton) {
			return "", err
		}
		if time.Now().After(deadline) {
			return "", errors.New("timed out waiting for the bridge link button to be pressed")
		}
	}
}

// deviceType identifies this client to the bridge, shown in the Hue app's
// list of connected apps.
func deviceType() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "cli"
	}
	// The bridge limits each half to 20 and 19 characters respectively.
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	if len(host) > 19 {
		host = host[:19]
	}
	return "hue-cli#" + host
}

// discoverBridge asks the discovery service and picks a bridge, requiring an
// explicit choice when several answer.
func (a *App) discoverBridge(ctx context.Context) (string, error) {
	fmt.Fprintln(a.Out, "discovering bridges...")
	found, err := hue.Discover(ctx, 0)
	if err != nil {
		return "", fmt.Errorf("%w\npass the address instead: hue auth --bridge 192.168.1.5", err)
	}
	switch len(found) {
	case 0:
		return "", errors.New("no bridges found; pass the address instead: hue auth --bridge 192.168.1.5")
	case 1:
		return hue.NormalizeHost(found[0].InternalAddress), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d bridges found; choose one with --bridge:\n", len(found))
	for _, f := range found {
		fmt.Fprintf(&b, "  %s (id %s)\n", f.InternalAddress, f.ID)
	}
	return "", errors.New(strings.TrimRight(b.String(), "\n"))
}
