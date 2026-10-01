package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brybry192/hue/internal/hue"
)

func (a *App) runOn(args []string) error  { return a.runSetState(args, true) }
func (a *App) runOff(args []string) error { return a.runSetState(args, false) }

// runSetState implements both `hue on` and `hue off`.
func (a *App) runSetState(args []string, on bool) error {
	name := "off"
	if on {
		name = "on"
	}
	fs, cfgPath := a.newFlagSet(name, name+" [flags] <room|zone|light>")
	asLight := fs.Bool("light", false, "treat the argument as a light name rather than a room or zone")
	// Like sweep, changing lights is opt-in rather than the default.
	dryRun := fs.Bool("dry-run", true, "report what would change without changing anything")
	noDryRun := fs.Bool("no-dry-run", false, "actually change the lights")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}
	dry := *dryRun && !*noDryRun
	target := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if target == "" {
		fs.Usage()
		return ErrUsage
	}

	cfg, err := a.loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	client, err := a.client(cfg)
	if err != nil {
		return err
	}

	ctx, cancel := a.context()
	defer cancel()

	snap, err := client.Snapshot(ctx)
	if err != nil {
		return err
	}

	if !*asLight {
		if group, ok := snap.Group(target); ok {
			return a.setGroup(ctx, client, group, on, dry)
		}
	}
	return a.setLightsByName(ctx, client, snap, target, on, *asLight, dry)
}

// setGroup prefers the group's grouped-light service, which changes every
// light in one request.
func (a *App) setGroup(ctx context.Context, client *hue.Client, group hue.GroupView, on, dry bool) error {
	if len(group.Lights) == 0 {
		return fmt.Errorf("%s (%s) has no lights", group.Name, group.Kind)
	}
	if dry {
		fmt.Fprintf(a.Out, "would turn %s %s (%s): %d lights\n",
			strings.TrimSpace(stateWord(on)), group.Name, group.Kind, len(group.Lights))
		a.dryRunHint()
		return nil
	}
	if group.GroupedLightID != "" {
		if err := client.SetGroupOn(ctx, group.GroupedLightID, on); err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "%s %s (%s): %d lights\n", stateWord(on), group.Name, group.Kind, len(group.Lights))
		return nil
	}

	// Fall back to individual lights for a group with no grouped-light
	// service, which should not happen but is cheap to support.
	var failed int
	for _, l := range group.Lights {
		if err := client.SetLightOn(ctx, l.ID, on); err != nil {
			fmt.Fprintf(a.Err, "hue: %s: %v\n", l.Name, err)
			failed++
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(writeDelay):
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d lights in %s could not be changed", failed, len(group.Lights), group.Name)
	}
	fmt.Fprintf(a.Out, "%s %s (%s): %d lights\n", stateWord(on), group.Name, group.Kind, len(group.Lights))
	return nil
}

// setLightsByName changes every light whose name matches, which handles both
// an explicit --light and the "not a room" fallback.
func (a *App) setLightsByName(ctx context.Context, client *hue.Client, snap *hue.Snapshot, target string, on, explicit, dry bool) error {
	want := strings.ToLower(target)
	var matches []hue.LightView
	for _, l := range snap.Lights {
		if strings.ToLower(l.Name) == want {
			matches = append(matches, l)
		}
	}
	if len(matches) == 0 {
		if explicit {
			return fmt.Errorf("no light named %q; run 'hue ls' to see the names", target)
		}
		return fmt.Errorf("no room, zone or light named %q; run 'hue ls' to see the names", target)
	}

	if dry {
		for _, l := range matches {
			fmt.Fprintf(a.Out, "would turn %s %s\n", strings.TrimSpace(stateWord(on)), l.Name)
		}
		a.dryRunHint()
		return nil
	}

	var failed int
	for _, l := range matches {
		if err := client.SetLightOn(ctx, l.ID, on); err != nil {
			fmt.Fprintf(a.Err, "hue: %s: %v\n", l.Name, err)
			failed++
			continue
		}
		fmt.Fprintf(a.Out, "%s %s\n", stateWord(on), l.Name)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(writeDelay):
		}
	}
	if failed > 0 {
		return errors.New("some lights could not be changed")
	}
	return nil
}

// dryRunHint tells the user how to actually apply the change.
func (a *App) dryRunHint() {
	fmt.Fprintln(a.Out, "this was a dry run; re-run with --no-dry-run to apply")
}

func stateWord(on bool) string {
	if on {
		return "on "
	}
	return "off"
}
