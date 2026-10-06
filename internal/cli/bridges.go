package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/brybry192/hue/internal/config"
	"github.com/brybry192/hue/internal/hue"
)

// bridge is one configured bridge and its client.
type bridge struct {
	cfg    config.Bridge
	client *hue.Client
}

// label is how the bridge appears in output. It is empty when only one bridge
// is configured, so single-bridge output does not change.
func (b bridge) label(multi bool) string {
	if !multi {
		return ""
	}
	return b.cfg.Label()
}

// home is every configured bridge, read and controlled as one.
type home struct {
	bridges []bridge
	aliases map[string]string
}

// home builds clients for every configured bridge, insisting on a host and
// key for each first.
func (a *App) home(cfg config.Config) (*home, error) {
	if len(cfg.Bridges) == 0 {
		return nil, errors.New("no bridge configured; run 'hue auth' (or set HUE_BRIDGE_HOST)")
	}
	h := &home{aliases: cfg.RoomAliases}
	for _, b := range cfg.Bridges {
		where := ""
		if len(cfg.Bridges) > 1 {
			where = fmt.Sprintf(" for bridge %s", b.Label())
		}
		if b.Host == "" {
			return nil, fmt.Errorf("no bridge address configured%s; run 'hue auth' (or set HUE_BRIDGE_HOST)", where)
		}
		if b.AppKey == "" {
			return nil, fmt.Errorf("no application key configured%s; run 'hue auth' (or set HUE_APP_KEY)", where)
		}
		h.bridges = append(h.bridges, bridge{cfg: b, client: hue.New(hue.Options{
			Host:       b.Host,
			AppKey:     b.AppKey,
			CertSHA256: b.CertSHA256,
			Insecure:   b.Insecure,
			Timeout:    b.Timeout.Duration(),
		})})
	}
	return h, nil
}

func (h *home) multi() bool { return len(h.bridges) > 1 }

// client returns the client for a bridge label as stamped on lights and
// grouped-light services. With one bridge the label is empty and ignored.
func (h *home) client(label string) (*hue.Client, error) {
	if !h.multi() {
		return h.bridges[0].client, nil
	}
	for _, b := range h.bridges {
		if b.cfg.Label() == label {
			return b.client, nil
		}
	}
	return nil, fmt.Errorf("no bridge named %q is configured", label)
}

// pick chooses one bridge by name or address for commands that inspect a
// single bridge. With one bridge configured the choice may be omitted.
func (h *home) pick(nameOrHost string) (bridge, error) {
	if nameOrHost == "" {
		if !h.multi() {
			return h.bridges[0], nil
		}
		return bridge{}, fmt.Errorf("%d bridges are configured; choose one with --bridge (%s)",
			len(h.bridges), strings.Join(h.labels(), ", "))
	}
	want := strings.ToLower(strings.TrimSpace(nameOrHost))
	for _, b := range h.bridges {
		if strings.ToLower(b.cfg.Name) == want || strings.ToLower(b.cfg.Host) == want {
			return b, nil
		}
	}
	return bridge{}, fmt.Errorf("no bridge %q is configured (have %s)", nameOrHost, strings.Join(h.labels(), ", "))
}

func (h *home) labels() []string {
	out := make([]string, len(h.bridges))
	for i, b := range h.bridges {
		out[i] = b.cfg.Label()
	}
	return out
}

// bridgeError is a bridge that could not be read.
type bridgeError struct {
	Bridge string
	Err    error
}

func (e bridgeError) Error() string { return fmt.Sprintf("bridge %s: %v", e.Bridge, e.Err) }

// snapshot reads every bridge in parallel and merges them into one view.
//
// A bridge that cannot be read is reported in failed rather than failing the
// whole call, so `hue ls` still shows the rest of the home. Callers decide
// whether a partial view is safe for what they are about to do. Only when no
// bridge can be read is an error returned.
func (h *home) snapshot(ctx context.Context) (snap *hue.Snapshot, failed []bridgeError, err error) {
	parts := make([]hue.BridgeSnapshot, len(h.bridges))
	errs := make([]error, len(h.bridges))

	var wg sync.WaitGroup
	for i, b := range h.bridges {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := b.client.Snapshot(ctx)
			if err != nil {
				errs[i] = err
				return
			}
			label := b.label(h.multi())
			s.TagBridge(label)
			parts[i] = hue.BridgeSnapshot{Bridge: label, Snap: s}
		}()
	}
	wg.Wait()

	var ok []hue.BridgeSnapshot
	for i, e := range errs {
		if e != nil {
			failed = append(failed, bridgeError{Bridge: h.bridges[i].cfg.Label(), Err: e})
			continue
		}
		ok = append(ok, parts[i])
	}
	if len(ok) == 0 {
		if !h.multi() {
			return nil, nil, errs[0]
		}
		return nil, failed, fmt.Errorf("no bridge could be read: %w", joinBridgeErrors(failed))
	}
	return hue.Merge(ok, h.aliases), failed, nil
}

// warnFailed reports bridges that could not be read, for commands that can
// carry on with the rest.
func (a *App) warnFailed(failed []bridgeError) {
	for _, f := range failed {
		fmt.Fprintf(a.Err, "hue: warning: %v; its rooms and lights are missing\n", f)
	}
}

func joinBridgeErrors(failed []bridgeError) error {
	msgs := make([]string, len(failed))
	for i, f := range failed {
		msgs[i] = f.Error()
	}
	return errors.New(strings.Join(msgs, "; "))
}
