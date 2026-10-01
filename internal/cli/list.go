package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/brybry192/hue/internal/hue"
)

func (a *App) runList(args []string) error {
	fs, cfgPath := a.newFlagSet("ls", "ls [flags] [room|zone ...]")
	onlyOn := fs.Bool("on", false, "only show lights that are on")
	asJSON := fs.Bool("json", false, "emit JSON")
	rooms := fs.Bool("rooms", false, "only show rooms, not zones")
	if err := fs.Parse(args); err != nil {
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

	groups := snap.Groups
	if *rooms {
		groups = filterGroups(groups, func(g hue.GroupView) bool { return g.Kind == hue.GroupRoom })
	}
	if names := fs.Args(); len(names) > 0 {
		wanted := make(map[string]bool, len(names))
		for _, n := range names {
			wanted[strings.ToLower(strings.TrimSpace(n))] = true
		}
		groups = filterGroups(groups, func(g hue.GroupView) bool {
			return wanted[strings.ToLower(g.Name)]
		})
		if len(groups) == 0 {
			return fmt.Errorf("no room or zone matched %s", strings.Join(names, ", "))
		}
	}

	if *asJSON {
		enc := json.NewEncoder(a.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(groups)
	}
	a.printGroups(groups, *onlyOn)
	return nil
}

func filterGroups(in []hue.GroupView, keep func(hue.GroupView) bool) []hue.GroupView {
	var out []hue.GroupView
	for _, g := range in {
		if keep(g) {
			out = append(out, g)
		}
	}
	return out
}

func (a *App) printGroups(groups []hue.GroupView, onlyOn bool) {
	now := a.now()
	tw := tabwriter.NewWriter(a.Out, 0, 8, 2, ' ', 0)

	totalLights, totalOn := 0, 0
	for i, g := range groups {
		if i > 0 {
			fmt.Fprintln(tw)
		}
		on := g.OnCount()
		totalLights += len(g.Lights)
		totalOn += on

		header := fmt.Sprintf("%s (%s)", g.Name, g.Kind)
		fmt.Fprintf(tw, "%s\t%d lights, %d on\n", header, len(g.Lights), on)

		for _, l := range g.Lights {
			if onlyOn && !l.On {
				continue
			}
			state := "off"
			bright := "-"
			if l.On {
				state = "on"
				if l.HasDimming {
					bright = fmt.Sprintf("%.0f%%", l.Brightness)
				}
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", state, bright, l.Name, l.Kind)
		}
		if onlyOn {
			continue
		}
		for _, s := range g.Sensors {
			detail := s.Describe(now)
			if g.SensorsInherited {
				detail += " (from room)"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", "-", "-", s.Name, hue.KindSensor, detail)
		}
		for _, c := range g.Controls {
			detail := c.Product
			if c.Buttons > 0 {
				detail = strings.TrimSpace(fmt.Sprintf("%s (%d buttons)", detail, c.Buttons))
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", "-", "-", c.Name, c.Kind, detail)
		}
	}

	if len(groups) == 0 {
		fmt.Fprintln(tw, "no rooms or zones found")
	} else {
		fmt.Fprintf(tw, "\n%d rooms/zones, %d lights, %d on\n", len(groups), totalLights, totalOn)
	}
	tw.Flush()
}
