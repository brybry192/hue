package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
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
	a.warnFailed(failed)

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
	a.printGroups(groups, *onlyOn, h.multi())
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

// listColumns are the headers of the device table, in order. BRIDGE is only
// shown when there is more than one bridge.
var listColumns = []string{"ROOM", "GROUP", "DEVICE", "TYPE", "BRIDGE", "STATE", "BRIGHT", "DETAIL"}

const bridgeColumn = 4

func (a *App) printGroups(groups []hue.GroupView, onlyOn, showBridge bool) {
	now := a.now()

	rows := make([][]string, 0, 32)
	totalLights, totalOn := 0, 0

	for _, g := range groups {
		totalLights += len(g.Lights)
		totalOn += g.OnCount()

		var block [][]string
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
					bright = brightness(l.Brightness)
				}
			}
			block = append(block, []string{
				g.Name, g.Kind, l.Name, string(l.Kind), l.Bridge, state, bright, detail,
			})
		}
		if !onlyOn {
			for _, s := range g.Sensors {
				detail := s.Describe(now)
				if g.SensorsInherited {
					detail += " (from room)"
				}
				block = append(block, []string{
					g.Name, g.Kind, s.Name, string(hue.KindSensor), s.Bridge, "-", "-", detail,
				})
			}
			for _, c := range g.Controls {
				detail := c.Product
				if c.Buttons > 0 {
					detail = strings.TrimSpace(fmt.Sprintf("%s (%d buttons)", detail, c.Buttons))
				}
				block = append(block, []string{
					g.Name, g.Kind, c.Name, string(c.Kind), c.Bridge, "-", "-", detail,
				})
			}
		}
		if len(block) == 0 {
			continue
		}
		// Blank line between rooms so each block reads on its own.
		if len(rows) > 0 {
			rows = append(rows, nil)
		}
		rows = append(rows, block...)
	}

	if len(rows) == 0 {
		fmt.Fprintln(a.Out, "nothing to show")
		return
	}

	headers := listColumns
	if !showBridge {
		headers = dropColumn(headers, bridgeColumn)
		for i, row := range rows {
			if row != nil {
				rows[i] = dropColumn(row, bridgeColumn)
			}
		}
	}
	writeTable(a.Out, headers, rows)
	fmt.Fprintf(a.Out, "\n%d rooms/zones, %d lights, %d on\n", len(groups), totalLights, totalOn)
}

// dropColumn returns row without column i.
func dropColumn(row []string, i int) []string {
	out := make([]string, 0, len(row)-1)
	out = append(out, row[:i]...)
	return append(out, row[i+1:]...)
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

// brightness formats a light's brightness setting to one decimal place, as
// the bridge reports it: 30.6%, 100%. A light on at the bottom of the scale
// can report 0, which would read as off, so it is shown as "min" (the Hue
// app shows 1%).
func brightness(pct float64) string {
	if pct <= 0 {
		return "min"
	}
	return strconv.FormatFloat(math.Round(pct*10)/10, 'f', -1, 64) + "%"
}
