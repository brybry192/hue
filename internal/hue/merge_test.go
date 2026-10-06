package hue

import (
	"testing"
	"time"
)

// twoBridges is a home split across two bridges:
//
//	main: Kitchen (k1), Terrace (p1), zone Outside (p1)
//	annex:  Kitchen (k2 + sensor), Yard (b1 + sensor)
func twoBridges(t *testing.T) (main, annex *Snapshot) {
	t.Helper()
	room := func(id, name string, devs ...string) Group {
		g := Group{ID: id, Type: "room", Metadata: Metadata{Name: name},
			Services: []ResourceRef{{RID: "gl-" + id, RType: "grouped_light"}}}
		for _, d := range devs {
			g.Children = append(g.Children, ResourceRef{RID: d, RType: "device"})
		}
		return g
	}

	main = BuildSnapshot(
		[]Group{room("main-kitchen", "Kitchen", "dev-k1"), room("main-patio", "Terrace", "dev-p1")},
		[]Group{{
			ID: "main-outside", Type: "zone", Metadata: Metadata{Name: "Outside"},
			Children: []ResourceRef{{RID: "p1", RType: "light"}},
			Services: []ResourceRef{{RID: "gl-outside", RType: "grouped_light"}},
		}},
		[]Device{lightDevice("dev-k1", "Pendant", "k1"), lightDevice("dev-p1", "Porch", "p1")},
		[]Light{lightRes("k1", "Pendant", true, 50), lightRes("p1", "Porch", true, 50)},
		nil,
	)
	main.TagBridge("main")

	annex = BuildSnapshot(
		[]Group{
			room("annex-kitchen", "kitchen", "dev-k2", "dev-s1"),
			room("annex-yard", "Yard", "dev-b1", "dev-s2"),
		},
		nil,
		[]Device{
			lightDevice("dev-k2", "Strip", "k2"),
			lightDevice("dev-b1", "Lantern", "b1"),
			sensorDevice("dev-s1", "Kitchen Sensor", "m1"),
			sensorDevice("dev-s2", "Terrace Sensor", "m2"),
		},
		[]Light{lightRes("k2", "Strip", false, 0), lightRes("b1", "Lantern", false, 0)},
		[]Motion{
			motionRes("m1", "dev-s1", false, now.Add(-time.Minute)),
			motionRes("m2", "dev-s2", false, now.Add(-time.Hour)),
		},
	)
	annex.TagBridge("annex")
	return main, annex
}

func TestMergeJoinsRoomsByName(t *testing.T) {
	main, annex := twoBridges(t)
	snap := Merge([]BridgeSnapshot{{"main", main}, {"annex", annex}}, nil)

	kitchen, ok := snap.Group("Kitchen")
	if !ok {
		t.Fatal("Kitchen not found")
	}
	if got := lightNames(kitchen); got != "Pendant@main,Strip@annex" {
		t.Errorf("Kitchen lights = %s, want Pendant@main,Strip@annex (matched case-insensitively)", got)
	}
	if len(kitchen.Sensors) != 1 || kitchen.Sensors[0].Bridge != "annex" {
		t.Errorf("Kitchen sensors = %+v, want the annex Kitchen Sensor", kitchen.Sensors)
	}
	if len(kitchen.GroupedLights) != 2 {
		t.Errorf("grouped lights = %+v, want one per bridge", kitchen.GroupedLights)
	}
	if kitchen.Name != "Kitchen" {
		t.Errorf("name = %q, want the first bridge's spelling", kitchen.Name)
	}

	// Without an alias, Yard stays its own room.
	if _, ok := snap.Group("Yard"); !ok {
		t.Error("Yard should be kept when no alias maps it")
	}
	if got := snap.Lights["k2"].Bridge; got != "annex" {
		t.Errorf("Lights[k2].Bridge = %q, want annex", got)
	}

	// Merging must not write through to the source snapshots.
	if src, _ := main.Group("Kitchen"); len(src.Lights) != 1 {
		t.Errorf("source Kitchen was modified: %d lights", len(src.Lights))
	}
}

func TestMergeAliasesAndZoneSensors(t *testing.T) {
	main, annex := twoBridges(t)
	snap := Merge([]BridgeSnapshot{{"main", main}, {"annex", annex}},
		map[string]string{" yard ": "Terrace"})

	if _, ok := snap.Group("Yard"); ok {
		t.Error("Yard should have been merged into Terrace")
	}
	patio, ok := snap.Group("Terrace")
	if !ok {
		t.Fatal("Terrace not found")
	}
	if got := lightNames(patio); got != "Lantern@annex,Porch@main" {
		t.Errorf("Terrace lights = %s", got)
	}

	// The Outside zone holds only a main light, but that light is now in a
	// room with the annex sensor, so the zone inherits it.
	outside, _ := snap.Group("Outside")
	if !outside.SensorsInherited || len(outside.Sensors) != 1 || outside.Sensors[0].Name != "Terrace Sensor" {
		t.Errorf("Outside sensors = %+v, want the annex Terrace Sensor", outside.Sensors)
	}
	if _, ok := outside.LastMotion(now); !ok {
		t.Error("Outside should be decided by motion once it has a sensor")
	}
}

func TestMergeSingleBridgeIsUnchanged(t *testing.T) {
	main, _ := twoBridges(t)
	snap := Merge([]BridgeSnapshot{{"main", main}}, nil)
	if len(snap.Groups) != len(main.Groups) || len(snap.Lights) != len(main.Lights) {
		t.Errorf("got %d groups / %d lights, want %d / %d",
			len(snap.Groups), len(snap.Lights), len(main.Groups), len(main.Lights))
	}
}

func lightNames(g GroupView) string {
	var s string
	for i, l := range g.Lights {
		if i > 0 {
			s += ","
		}
		s += l.Name + "@" + l.Bridge
	}
	return s
}
