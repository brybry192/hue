package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"github.com/brybry192/hue/internal/config"
	"github.com/brybry192/hue/internal/hue"
	"github.com/brybry192/hue/internal/state"
	"github.com/brybry192/hue/internal/sweep"
)

// writeDelay spaces out the PUTs sent to the bridge, which is a small embedded
// device and rate limits bursts.
const writeDelay = 120 * time.Millisecond

// result records what happened to one light.
type result struct {
	Group   string `json:"group"`
	Kind    string `json:"group_kind"`
	Rule    string `json:"rule"`
	Reason  string `json:"reason"`
	LightID string `json:"light_id"`
	Name    string `json:"name"`
	OnFor   string `json:"on_for,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (a *App) runSweep(args []string) error {
	fs, cfgPath := a.newFlagSet("sweep", "sweep [flags]")
	// Switching lights off is destructive enough to be opt-in: a sweep only
	// acts when explicitly told to.
	dryRun := fs.Bool("dry-run", true, "report what would be switched off without changing anything")
	noDryRun := fs.Bool("no-dry-run", false, "actually switch the lights off")
	roomList := fs.String("rooms", "", "comma-separated rooms or zones to sweep, overriding the config")
	idle := fs.Duration("idle", 0, "motion idle threshold, overriding the config")
	minOn := fs.Duration("min-on", 0, "how long a light must be observed on first, overriding the config")
	force := fs.Bool("force", false, "ignore the min-on grace period")
	asJSON := fs.Bool("json", false, "emit JSON")
	verbose := fs.Bool("v", false, "explain groups that were left alone")
	statePath := fs.String("state", "", "sweep state file path (default "+defaultStatePathForHelp()+")")
	noState := fs.Bool("no-state", false, "do not read or write the state file")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}

	// --no-dry-run (or an explicit --dry-run=false) is what authorises a
	// real change; anything else stays a preview.
	dry := *dryRun && !*noDryRun

	cfg, err := a.loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	applySweepOverrides(&cfg.Sweep, fs, *idle, *minOn)

	h, err := a.home(cfg)
	if err != nil {
		return err
	}

	ctx, cancel := a.context()
	defer cancel()

	snap, failed, err := h.snapshot(ctx)
	if err != nil {
		return err
	}
	// Without every bridge the sweep would judge rooms on half the
	// evidence: a room's sensor may live on the missing bridge, and the
	// outlier rule would then switch off lights someone is using. Skip the
	// run; the next scheduled one will try again.
	if len(failed) > 0 {
		return fmt.Errorf("skipping sweep, not every bridge could be read: %w", joinBridgeErrors(failed))
	}

	// Load history before planning so the grace period is judged against
	// previous runs, not this one.
	store, err := a.loadState(*statePath, *noState)
	if err != nil {
		return err
	}

	now := a.now()
	plan := sweep.Build(sweep.Inputs{
		Groups:        snap.Groups,
		Cfg:           cfg.Sweep,
		OnSince:       store.OnSince,
		Now:           now,
		IgnoreMinOn:   *force,
		RoomsOverride: splitList(*roomList),
	})

	// Record what we saw, whether or not we act on it.
	lights := make([]hue.LightView, 0, len(snap.Lights))
	for _, l := range snap.Lights {
		lights = append(lights, l)
	}
	store.Observe(lights, now)

	results, actErr := a.applyPlan(ctx, h, snap, plan, dry, store)

	// A dry run still records observations, otherwise the grace period could
	// never elapse for someone who is only ever dry-running.
	if !*noState {
		if err := store.Save(); err != nil {
			fmt.Fprintf(a.Err, "hue: warning: could not save sweep state: %v\n", err)
		}
	}

	if *asJSON {
		out := struct {
			DryRun  bool       `json:"dry_run"`
			Plan    sweep.Plan `json:"plan"`
			Results []result   `json:"results"`
		}{DryRun: dry, Plan: plan, Results: results}
		enc := json.NewEncoder(a.Out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return err
		}
		return actErr
	}

	a.printLog(plan, results, dry, *verbose)
	return actErr
}

// applySweepOverrides folds command-line flags into the sweep config, only for
// flags the user actually passed.
func applySweepOverrides(s *config.Sweep, fs *flag.FlagSet, idle, minOn time.Duration) {
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "idle":
			s.Motion.IdleThreshold = config.Duration(idle)
		case "min-on":
			s.MinOnDuration = config.Duration(minOn)
		}
	})
}

func defaultStatePathForHelp() string {
	if p, err := state.DefaultPath(); err == nil {
		return p
	}
	return "~/.local/state/hue/sweep-state.json"
}

// loadState opens the state store, or an in-memory throwaway when disabled.
func (a *App) loadState(path string, disabled bool) (*state.Store, error) {
	if disabled {
		// An empty path that is never saved behaves as "no history".
		return state.Load(devNullState())
	}
	store, err := state.Load(path)
	if err != nil {
		return nil, err
	}
	return store, nil
}

// devNullState returns a path that cannot exist, giving a store with no
// history. Save is never called on it.
func devNullState() string {
	return "/nonexistent/hue-sweep-state.json"
}

// applyPlan switches off the planned lights unless this is a dry run.
func (a *App) applyPlan(ctx context.Context, h *home, snap *hue.Snapshot, plan sweep.Plan, dryRun bool, store *state.Store) ([]result, error) {
	var results []result
	var failures int

	for _, action := range plan.Actions {
		for _, target := range action.Lights {
			r := result{
				Group:   action.Group,
				Kind:    action.GroupKind,
				Rule:    string(action.Rule),
				Reason:  action.Reason,
				LightID: target.LightID,
				Name:    target.Name,
			}
			if target.HasOnFor {
				r.OnFor = hue.ShortDuration(target.OnFor)
			}
			if dryRun {
				results = append(results, r)
				continue
			}
			if err := ctx.Err(); err != nil {
				return results, err
			}
			client, err := h.client(snap.Lights[target.LightID].Bridge)
			if err == nil {
				err = client.SetLightOn(ctx, target.LightID, false)
			}
			if err != nil {
				r.Error = err.Error()
				failures++
			} else {
				// The light is off now, so its on-timer should restart from
				// the next time it comes on.
				store.Forget(target.LightID)
			}
			results = append(results, r)

			select {
			case <-ctx.Done():
				return results, ctx.Err()
			case <-time.After(writeDelay):
			}
		}
	}
	if failures > 0 {
		return results, fmt.Errorf("%d of %d lights could not be switched off", failures, len(results))
	}
	return results, nil
}

// printLog writes the sweep as logfmt-style lines, each starting with the
// sweep time so the output reads the same in a terminal and in a log file.
// One line per light, then a summary; a run with nothing to do is one line.
//
//	2026-10-05T13:12:03Z dry-run off group="Study" kind=room rule=outlier light="Sconce" on_for=12m reason="1 of 4 lights on (25% of the group)"
//	2026-10-05T13:12:03Z dry-run swept lights=1 groups=1 hint="nothing changed; add --no-dry-run to apply"
func (a *App) printLog(plan sweep.Plan, results []result, dryRun, verbose bool) {
	prefix := plan.Now.Format(time.RFC3339)
	if dryRun {
		prefix += " dry-run"
	}

	for _, w := range plan.Warnings {
		fmt.Fprintf(a.Err, "%s warning msg=%q\n", prefix, w)
	}
	for _, r := range results {
		event := "off"
		if r.Error != "" {
			event = "failed"
		}
		line := fmt.Sprintf("%s %s group=%q kind=%s rule=%s light=%q", prefix, event, r.Group, r.Kind, r.Rule, r.Name)
		if r.OnFor != "" {
			line += " on_for=" + r.OnFor
		}
		line += fmt.Sprintf(" reason=%q", r.Reason)
		if r.Error != "" {
			line += fmt.Sprintf(" error=%q", r.Error)
		}
		fmt.Fprintln(a.Out, line)
	}
	if verbose {
		for _, n := range plan.Notes {
			fmt.Fprintf(a.Out, "%s note group=%q msg=%q\n", prefix, n.Group, n.Text)
		}
	}

	summary := fmt.Sprintf("%s swept lights=%d groups=%d", prefix, plan.LightCount(), len(plan.Actions))
	switch {
	case dryRun && plan.LightCount() > 0:
		summary += ` hint="nothing changed; add --no-dry-run to apply"`
	case plan.LightCount() == 0 && !verbose && len(plan.Notes) > 0:
		summary += ` hint="add -v to see why each group was left alone"`
	}
	fmt.Fprintln(a.Out, summary)
}
