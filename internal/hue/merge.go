package hue

import "strings"

// BridgeSnapshot is one bridge's snapshot, labelled with the bridge's name.
type BridgeSnapshot struct {
	Bridge string
	Snap   *Snapshot
}

// TagBridge records which bridge every light, sensor, control and
// grouped-light service in the snapshot belongs to, so that commands can be
// routed back to it after snapshots are merged.
func (s *Snapshot) TagBridge(name string) {
	for gi := range s.Groups {
		g := &s.Groups[gi]
		for i := range g.Lights {
			g.Lights[i].Bridge = name
		}
		for i := range g.Sensors {
			g.Sensors[i].Bridge = name
		}
		for i := range g.Controls {
			g.Controls[i].Bridge = name
		}
		for i := range g.GroupedLights {
			g.GroupedLights[i].Bridge = name
		}
	}
	for id, l := range s.Lights {
		l.Bridge = name
		s.Lights[id] = l
	}
}

// Merge combines several bridges into one view of the home.
//
// Rooms with the same name, ignoring case, become one room holding every
// bridge's lights, sensors and controls; zones likewise. aliases renames
// groups before matching, for rooms that are named differently on each
// bridge: {"Yard": "Terrace"} merges a Yard room into Terrace.
//
// Zones then re-inherit sensors from the merged rooms, which is what lets a
// motion sensor on one bridge decide for a zone of lights on another.
//
// IDs are UUIDs, unique across bridges, so lights need no renaming.
func Merge(parts []BridgeSnapshot, aliases map[string]string) *Snapshot {
	alias := make(map[string]string, len(aliases))
	for from, to := range aliases {
		alias[foldName(from)] = strings.TrimSpace(to)
	}

	out := &Snapshot{
		Lights:  make(map[string]LightView),
		Devices: make(map[string]Device),
	}
	index := make(map[string]int)

	for pi, p := range parts {
		if p.Snap == nil {
			continue
		}
		if pi == 0 {
			out.Bridge = p.Snap.Bridge
		}
		for id, l := range p.Snap.Lights {
			out.Lights[id] = l
		}
		for id, d := range p.Snap.Devices {
			out.Devices[id] = d
		}
		for _, g := range p.Snap.Groups {
			if to, ok := alias[foldName(g.Name)]; ok && to != "" {
				g.Name = to
			}
			key := g.Kind + "\x00" + foldName(g.Name)
			i, seen := index[key]
			if !seen {
				index[key] = len(out.Groups)
				out.Groups = append(out.Groups, cloneGroup(g))
				continue
			}
			m := &out.Groups[i]
			m.Lights = append(m.Lights, g.Lights...)
			m.Sensors = append(m.Sensors, g.Sensors...)
			m.Controls = append(m.Controls, g.Controls...)
			m.GroupedLights = append(m.GroupedLights, g.GroupedLights...)
		}
	}

	for i := range out.Groups {
		sortGroupContents(&out.Groups[i])
	}
	inheritZoneSensors(out.Groups)
	sortGroups(out.Groups)
	return out
}

// cloneGroup copies a group's slices so appending to the merged group cannot
// write into the source snapshot's backing arrays.
func cloneGroup(g GroupView) GroupView {
	g.Lights = append([]LightView(nil), g.Lights...)
	g.Sensors = append([]SensorView(nil), g.Sensors...)
	g.Controls = append([]ControlView(nil), g.Controls...)
	g.GroupedLights = append([]GroupedLightRef(nil), g.GroupedLights...)
	return g
}

func foldName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
