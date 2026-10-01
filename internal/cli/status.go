package cli

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/brybry192/hue/internal/hue"
	"github.com/brybry192/hue/internal/state"
)

func (a *App) runStatus(args []string) error {
	fs, cfgPath := a.newFlagSet("status", "status [flags]")
	asJSON := fs.Bool("json", false, "emit JSON")
	statePath := fs.String("state", "", "sweep state file path")
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

	info, err := client.Bridge(ctx)
	if err != nil {
		return err
	}
	snap, err := client.Snapshot(ctx)
	if err != nil {
		return err
	}

	var rooms, zones, lights, on, sensors, controls int
	for _, g := range snap.Groups {
		if g.Kind == hue.GroupRoom {
			rooms++
			// Only count a room's contents, so zones do not double-count.
			sensors += len(g.Sensors)
			controls += len(g.Controls)
			lights += len(g.Lights)
			on += g.OnCount()
		} else {
			zones++
		}
	}

	type statusOut struct {
		Bridge     string `json:"bridge"`
		BridgeID   string `json:"bridge_id"`
		TimeZone   string `json:"time_zone,omitempty"`
		Rooms      int    `json:"rooms"`
		Zones      int    `json:"zones"`
		Lights     int    `json:"lights"`
		LightsOn   int    `json:"lights_on"`
		Sensors    int    `json:"sensors"`
		Controls   int    `json:"controls"`
		SweptState string `json:"state_file,omitempty"`
		Tracked    int    `json:"tracked_on,omitempty"`
		ConfigPath string `json:"config_path,omitempty"`
		SweepRooms int    `json:"sweep_rooms"`
	}
	out := statusOut{
		Bridge:     cfg.Bridge.Host,
		BridgeID:   info.BridgeID,
		TimeZone:   info.TimeZone.TimeZone,
		Rooms:      rooms,
		Zones:      zones,
		Lights:     lights,
		LightsOn:   on,
		Sensors:    sensors,
		Controls:   controls,
		ConfigPath: cfg.Path(),
		SweepRooms: len(cfg.Sweep.Rooms),
	}
	if store, err := state.Load(*statePath); err == nil {
		out.SweptState = store.Path()
		out.Tracked = store.Len()
	}

	if *asJSON {
		enc := json.NewEncoder(a.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	tw := tabwriter.NewWriter(a.Out, 0, 8, 2, ' ', 0)
	fmt.Fprintf(tw, "bridge\t%s\n", out.Bridge)
	fmt.Fprintf(tw, "bridge id\t%s\n", out.BridgeID)
	if out.TimeZone != "" {
		fmt.Fprintf(tw, "time zone\t%s\n", out.TimeZone)
	}
	fmt.Fprintf(tw, "rooms\t%d\n", out.Rooms)
	fmt.Fprintf(tw, "zones\t%d\n", out.Zones)
	fmt.Fprintf(tw, "lights\t%d (%d on)\n", out.Lights, out.LightsOn)
	fmt.Fprintf(tw, "motion sensors\t%d\n", out.Sensors)
	fmt.Fprintf(tw, "switches/remotes\t%d\n", out.Controls)
	fmt.Fprintf(tw, "config\t%s\n", out.ConfigPath)
	if out.SweptState != "" {
		fmt.Fprintf(tw, "sweep state\t%s (%d lights tracked on)\n", out.SweptState, out.Tracked)
	}
	if len(cfg.Sweep.Rooms) == 0 {
		fmt.Fprintf(tw, "sweep scope\tevery room and zone\n")
	} else {
		fmt.Fprintf(tw, "sweep scope\t%d configured: %v\n", len(cfg.Sweep.Rooms), sortedNames(cfg.Sweep.Rooms))
	}
	fmt.Fprintf(tw, "motion rule\t%s, idle threshold %s\n",
		enabledWord(cfg.Sweep.Motion.IsEnabled()), cfg.Sweep.Motion.IdleThreshold)
	fmt.Fprintf(tw, "outlier rule\t%s, up to %d on and at most %.0f%% of a group of %d+\n",
		enabledWord(cfg.Sweep.Outlier.IsEnabled()), cfg.Sweep.Outlier.MaxOnCount,
		cfg.Sweep.Outlier.MaxOnFraction*100, cfg.Sweep.Outlier.MinGroupSize)
	fmt.Fprintf(tw, "grace period\t%s\n", cfg.Sweep.MinOnDuration)
	return tw.Flush()
}

func enabledWord(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}
