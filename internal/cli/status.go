package cli

import (
	"encoding/json"
	"fmt"
	"strings"
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
	down := make(map[string]error, len(failed))
	for _, f := range failed {
		down[f.Bridge] = f.Err
	}

	type bridgeOut struct {
		Name     string `json:"name,omitempty"`
		Host     string `json:"host"`
		BridgeID string `json:"bridge_id,omitempty"`
		TimeZone string `json:"time_zone,omitempty"`
		Error    string `json:"error,omitempty"`
	}
	bridges := make([]bridgeOut, 0, len(h.bridges))
	for _, b := range h.bridges {
		bo := bridgeOut{Name: b.cfg.Name, Host: b.cfg.Host}
		if err, ok := down[b.cfg.Label()]; ok {
			bo.Error = err.Error()
		} else if info, err := b.client.Bridge(ctx); err != nil {
			bo.Error = err.Error()
		} else {
			bo.BridgeID = info.BridgeID
			bo.TimeZone = info.TimeZone.TimeZone
		}
		bridges = append(bridges, bo)
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
		Bridges    []bridgeOut `json:"bridges"`
		Rooms      int         `json:"rooms"`
		Zones      int         `json:"zones"`
		Lights     int         `json:"lights"`
		LightsOn   int         `json:"lights_on"`
		Sensors    int         `json:"sensors"`
		Controls   int         `json:"controls"`
		SweptState string      `json:"state_file,omitempty"`
		Tracked    int         `json:"tracked_on,omitempty"`
		ConfigPath string      `json:"config_path,omitempty"`
		SweepRooms int         `json:"sweep_rooms"`
	}
	out := statusOut{
		Bridges:    bridges,
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
	for _, b := range out.Bridges {
		label := "bridge"
		if b.Name != "" {
			label += " " + b.Name
		}
		switch {
		case b.Error != "":
			fmt.Fprintf(tw, "%s\t%s, unreachable: %s\n", label, b.Host, b.Error)
		case b.TimeZone != "":
			fmt.Fprintf(tw, "%s\t%s, id %s, %s\n", label, b.Host, b.BridgeID, b.TimeZone)
		default:
			fmt.Fprintf(tw, "%s\t%s, id %s\n", label, b.Host, b.BridgeID)
		}
	}
	if len(cfg.RoomAliases) > 0 {
		var pairs []string
		for from, to := range cfg.RoomAliases {
			pairs = append(pairs, from+" -> "+to)
		}
		fmt.Fprintf(tw, "room aliases\t%s\n", strings.Join(sortedNames(pairs), ", "))
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
