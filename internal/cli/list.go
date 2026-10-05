package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/brybry192/hue/internal/hue"
)

func (a *App) runList(args []string) error {
	fs, cfgPath := a.newFlagSet("ls", "ls [flags] [room|zone ...]")
	onlyOn := fs.Bool("on", false, "only show lights that are on")
	asJSON := fs.Bool("json", false, "emit JSON")
	rooms := fs.Bool("rooms", false, "only show rooms, not zones")
	positional, err := parseArgs(fs, args)
	if err != nil {
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
	if names := positional; len(names) > 0 {
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

// listColumns are the headers of the device table, in order.
var listColumns = []string{"ROOM", "GROUP", "DEVICE", "TYPE", "STATE", "BRIGHT", "DETAIL"}

func (a *App) printGroups(groups []hue.GroupView, onlyOn bool) {
	now := a.now()

	rows := make([][]string, 0, 32)
	totalLights, totalOn := 0, 0

	for gi, g := range groups {
		totalLights += len(g.Lights)
		totalOn += g.OnCount()

		// Blank line between rooms so each block reads on its own.
		if gi > 0 {
			rows = append(rows, nil)
		}

		for _, l := range g.Lights {
			if onlyOn && (!l.On || l.Unreachable) {
				continue
			}
			state, bright, detail := "off", "-", ""
			switch {
			case l.Unreachable:
				// The bridge's on/off value is stale, so do not repeat it.
				state, detail = "?", "unreachable (no power?)"
			case l.On:
				state = "on"
				if l.HasDimming {
					bright = fmt.Sprintf("%.0f%%", l.Brightness)
				}
			}
			rows = append(rows, []string{
				g.Name, g.Kind, l.Name, string(l.Kind), state, bright, detail,
			})
		}
		if onlyOn {
			continue
		}

		for _, s := range g.Sensors {
			detail := s.Describe(now)
			if g.SensorsInherited {
				detail += " (from room)"
			}
			rows = append(rows, []string{
				g.Name, g.Kind, s.Name, string(hue.KindSensor), "-", "-", detail,
			})
		}
		for _, c := range g.Controls {
			detail := c.Product
			if c.Buttons > 0 {
				detail = strings.TrimSpace(fmt.Sprintf("%s (%d buttons)", detail, c.Buttons))
			}
			rows = append(rows, []string{
				g.Name, g.Kind, c.Name, string(c.Kind), "-", "-", detail,
			})
		}
	}

	if len(rows) == 0 {
		fmt.Fprintln(a.Out, "nothing to show")
		return
	}

	writeTable(a.Out, listColumns, rows)
	fmt.Fprintf(a.Out, "\n%d rooms/zones, %d lights, %d on\n", len(groups), totalLights, totalOn)
}

// writeTable prints a header, a dashed rule and the rows, each column padded to
// its widest value. Trailing empty columns are left off so rows do not end in
// a run of spaces.
func writeTable(w interface {
	Write([]byte) (int, error)
}, headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && utf8.RuneCountInString(cell) > widths[i] {
				widths[i] = utf8.RuneCountInString(cell)
			}
		}
	}

	rule := make([]string, len(headers))
	for i, n := range widths {
		rule[i] = strings.Repeat("-", n)
	}

	for _, line := range append([][]string{headers, rule}, rows...) {
		fmt.Fprintln(w, strings.TrimRight(padCells(line, widths), " "))
	}
}

// padCells joins one row, padding every cell but the last to its column width.
func padCells(cells []string, widths []int) string {
	var b strings.Builder
	for i, cell := range cells {
		if i > 0 {
			b.WriteString("  ")
		}
		if i == len(cells)-1 {
			b.WriteString(cell)
			continue
		}
		b.WriteString(cell)
		if pad := widths[i] - utf8.RuneCountInString(cell); pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
	}
	return b.String()
}
