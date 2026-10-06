package sweep

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/brybry192/hue/internal/config"
	"github.com/brybry192/hue/internal/hue"
)

var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// light builds an ordinary light.
func light(id, name string, on bool) hue.LightView {
	return hue.LightView{ID: id, Name: name, DeviceName: name, Kind: hue.KindLight, On: on}
}

// plug builds a smart-plug light.
func plug(id, name string, on bool) hue.LightView {
	return hue.LightView{ID: id, Name: name, DeviceName: name, Kind: hue.KindPlug, On: on}
}

// lightsOnOf builds total lights of which the first onCount are on.
func lightsOnOf(total, onCount int) []hue.LightView {
	out := make([]hue.LightView, 0, total)
	for i := range total {
		id := fmt.Sprintf("l%d", i+1)
		out = append(out, light(id, fmt.Sprintf("Light %d", i+1), i < onCount))
	}
	return out
}

// lightsPrefixed is lightsOnOf with group-unique IDs, for tests that use more
// than one group at a time.
func lightsPrefixed(prefix string, total, onCount int) []hue.LightView {
	out := make([]hue.LightView, 0, total)
	for i := range total {
		out = append(out, light(
			fmt.Sprintf("%s-l%d", prefix, i+1),
			fmt.Sprintf("%s Light %d", prefix, i+1),
			i < onCount))
	}
	return out
}

// sensorSeen builds a motion sensor that last changed the given time ago.
func sensorSeen(name string, ago time.Duration) hue.SensorView {
	return hue.SensorView{
		ID: "m-" + name, Name: name, Enabled: true, HasReport: true,
		Motion: false, LastChanged: testNow.Add(-ago),
	}
}

// sensorActive builds a sensor currently reporting motion.
func sensorActive(name string) hue.SensorView {
	return hue.SensorView{
		ID: "m-" + name, Name: name, Enabled: true, HasReport: true,
		Motion: true, LastChanged: testNow,
	}
}

func room(name string, lights []hue.LightView, sensors ...hue.SensorView) hue.GroupView {
	return hue.GroupView{
		ID: "g-" + name, Kind: hue.GroupRoom, Name: name,
		GroupedLights: []hue.GroupedLightRef{{ID: "gl-" + name}}, Lights: lights, Sensors: sensors,
	}
}

// baseCfg is the shipped default, which the tests then tweak.
func baseCfg() config.Sweep { return config.Default().Sweep }

func build(t *testing.T, in Inputs) Plan {
	t.Helper()
	if in.Now.IsZero() {
		in.Now = testNow
	}
	return Build(in)
}

// targetNames lists every light the plan would switch off.
func targetNames(p Plan) []string {
	var out []string
	for _, a := range p.Actions {
		for _, l := range a.Lights {
			out = append(out, l.Name)
		}
	}
	return out
}

func TestBuildRules(t *testing.T) {
	tests := []struct {
		name      string
		group     hue.GroupView
		tweak     func(*config.Sweep)
		wantRule  Rule
		wantCount int
	}{
		{
			name:      "one of five on is an outlier",
			group:     room("Kitchen", lightsOnOf(5, 1)),
			wantRule:  RuleOutlier,
			wantCount: 1,
		},
		{
			name:      "two of twelve on is an outlier",
			group:     room("Outside", lightsOnOf(12, 2)),
			wantRule:  RuleOutlier,
			wantCount: 2,
		},
		{
			name:      "three of five on is most of the group, left alone",
			group:     room("Kitchen", lightsOnOf(5, 3)),
			wantCount: 0,
		},
		{
			name:      "every light on is left alone",
			group:     room("Kitchen", lightsOnOf(4, 4)),
			wantCount: 0,
		},
		{
			name:      "a group of two has no meaningful outlier",
			group:     room("Closet", lightsOnOf(2, 1)),
			wantCount: 0,
		},
		{
			name:      "three on exceeds max_on_count even in a big group",
			group:     room("Outside", lightsOnOf(20, 3)),
			wantCount: 0,
		},
		{
			name:      "nothing on means nothing to do",
			group:     room("Kitchen", lightsOnOf(5, 0)),
			wantCount: 0,
		},
		{
			name:      "idle motion sensor clears the whole room",
			group:     room("Hallway", lightsOnOf(3, 3), sensorSeen("Hall Sensor", 20*time.Minute)),
			wantRule:  RuleMotionIdle,
			wantCount: 3,
		},
		{
			name:      "recent motion vetoes the outlier rule",
			group:     room("Hallway", lightsOnOf(5, 1), sensorSeen("Hall Sensor", 2*time.Minute)),
			wantCount: 0,
		},
		{
			name:      "motion right now vetoes the outlier rule",
			group:     room("Hallway", lightsOnOf(5, 1), sensorActive("Hall Sensor")),
			wantCount: 0,
		},
		{
			name: "the most recent of several sensors wins",
			group: room("Landing", lightsOnOf(3, 3),
				sensorSeen("A", 40*time.Minute), sensorSeen("B", 3*time.Minute)),
			wantCount: 0,
		},
		{
			name: "all sensors idle clears the room",
			group: room("Landing", lightsOnOf(3, 3),
				sensorSeen("A", 40*time.Minute), sensorSeen("B", 16*time.Minute)),
			wantRule:  RuleMotionIdle,
			wantCount: 3,
		},
		{
			name: "a sensor with no motion report falls back to the outlier rule",
			group: room("Hallway", lightsOnOf(5, 1),
				hue.SensorView{ID: "m1", Name: "Old Sensor", Enabled: true, HasReport: false}),
			wantRule:  RuleOutlier,
			wantCount: 1,
		},
		{
			name: "a disabled sensor is ignored",
			group: room("Hallway", lightsOnOf(5, 1),
				hue.SensorView{ID: "m1", Name: "Off Sensor", Enabled: false, HasReport: true,
					LastChanged: testNow}),
			wantRule:  RuleOutlier,
			wantCount: 1,
		},
		{
			name:  "disabling the motion rule lets the outlier rule see the room",
			group: room("Hallway", lightsOnOf(5, 1), sensorSeen("Hall Sensor", time.Minute)),
			tweak: func(s *config.Sweep) {
				no := false
				s.Motion.Enabled = &no
			},
			wantRule:  RuleOutlier,
			wantCount: 1,
		},
		{
			name:  "disabling the outlier rule leaves a sensorless room alone",
			group: room("Kitchen", lightsOnOf(5, 1)),
			tweak: func(s *config.Sweep) {
				no := false
				s.Outlier.Enabled = &no
			},
			wantCount: 0,
		},
		{
			name:      "a stricter fraction can rule out a small group",
			group:     room("Kitchen", lightsOnOf(4, 1)),
			tweak:     func(s *config.Sweep) { s.Outlier.MaxOnFraction = 0.2 },
			wantCount: 0,
		},
		{
			name:      "a longer idle threshold keeps the room on",
			group:     room("Hallway", lightsOnOf(3, 3), sensorSeen("Hall Sensor", 20*time.Minute)),
			tweak:     func(s *config.Sweep) { s.Motion.IdleThreshold = config.Duration(30 * time.Minute) },
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseCfg()
			if tc.tweak != nil {
				tc.tweak(&cfg)
			}
			plan := build(t, Inputs{Groups: []hue.GroupView{tc.group}, Cfg: cfg})

			if got := plan.LightCount(); got != tc.wantCount {
				t.Fatalf("switched off %d lights, want %d (actions: %+v)", got, tc.wantCount, plan.Actions)
			}
			if tc.wantCount == 0 {
				if len(plan.Actions) != 0 {
					t.Fatalf("expected no actions, got %+v", plan.Actions)
				}
				return
			}
			if len(plan.Actions) != 1 {
				t.Fatalf("expected 1 action, got %d", len(plan.Actions))
			}
			if plan.Actions[0].Rule != tc.wantRule {
				t.Errorf("rule = %q, want %q", plan.Actions[0].Rule, tc.wantRule)
			}
			if plan.Actions[0].Reason == "" {
				t.Error("action has no human-readable reason")
			}
		})
	}
}

func TestExclusions(t *testing.T) {
	cfg := baseCfg()
	cfg.ExcludeLights = []string{"Night Light", "porch *"}

	group := room("Hall", []hue.LightView{
		light("l1", "Night Light", true),
		light("l2", "Hall Ceiling", false),
		light("l3", "Hall Lamp", false),
		light("l4", "Hall Spot", false),
	})
	plan := build(t, Inputs{Groups: []hue.GroupView{group}, Cfg: cfg})
	if n := plan.LightCount(); n != 0 {
		t.Fatalf("switched off %d lights, want 0 (the only one on is excluded)", n)
	}

	t.Run("wildcards match case-insensitively", func(t *testing.T) {
		g := room("Outside", []hue.LightView{
			light("l1", "Porch Left", true),
			light("l2", "Garden 1", false),
			light("l3", "Garden 2", false),
			light("l4", "Garden 3", false),
		})
		plan := build(t, Inputs{Groups: []hue.GroupView{g}, Cfg: cfg})
		if n := plan.LightCount(); n != 0 {
			t.Fatalf("switched off %d lights, want 0 (Porch Left matches 'porch *')", n)
		}
	})

	t.Run("an exclusion by device name also counts", func(t *testing.T) {
		c := baseCfg()
		c.ExcludeLights = []string{"Fish Tank Plug"}
		g := room("Study", []hue.LightView{
			{ID: "l1", Name: "Tank Light", DeviceName: "Fish Tank Plug", Kind: hue.KindLight, On: true},
			light("l2", "Desk", false),
			light("l3", "Shelf", false),
		})
		plan := build(t, Inputs{Groups: []hue.GroupView{g}, Cfg: c})
		if n := plan.LightCount(); n != 0 {
			t.Fatalf("switched off %d lights, want 0", n)
		}
	})
}

func TestPlugHandling(t *testing.T) {
	group := room("Study", []hue.LightView{
		plug("p1", "Lamp Plug", true),
		light("l2", "Desk", false),
		light("l3", "Shelf", false),
		light("l4", "Spot", false),
	})

	t.Run("include_plugs false leaves them alone", func(t *testing.T) {
		cfg := baseCfg()
		cfg.IncludePlugs = false
		plan := build(t, Inputs{Groups: []hue.GroupView{group}, Cfg: cfg})
		if n := plan.LightCount(); n != 0 {
			t.Fatalf("switched off %d lights, want 0", n)
		}
	})

	t.Run("plugs are swept by default", func(t *testing.T) {
		plan := build(t, Inputs{Groups: []hue.GroupView{group}, Cfg: baseCfg()})
		if n := plan.LightCount(); n != 1 {
			t.Fatalf("switched off %d lights, want 1", n)
		}
		if got := targetNames(plan)[0]; got != "Lamp Plug" {
			t.Errorf("switched off %q, want Lamp Plug", got)
		}
	})

	t.Run("an excluded plug does not count toward the group size", func(t *testing.T) {
		// Two real lights plus a plug: with the plug excluded the group is
		// below min_group_size, so nothing happens.
		g := room("Nook", []hue.LightView{
			plug("p1", "Heater", false),
			light("l1", "Nook Lamp", true),
			light("l2", "Nook Spot", false),
		})
		cfg := baseCfg()
		cfg.IncludePlugs = false
		plan := build(t, Inputs{Groups: []hue.GroupView{g}, Cfg: cfg})
		if n := plan.LightCount(); n != 0 {
			t.Fatalf("switched off %d lights, want 0", n)
		}
	})
}

func TestUnreachableLights(t *testing.T) {
	unreachable := func(l hue.LightView) hue.LightView {
		l.Unreachable = true
		return l
	}

	t.Run("a stale on light without power is not swept", func(t *testing.T) {
		// The bridge still reports Sconce as on, but it cannot reach it.
		g := room("Study", []hue.LightView{
			unreachable(light("l1", "Sconce", true)),
			light("l2", "Desk", false),
			light("l3", "Shelf", false),
			light("l4", "Spot", false),
		})
		plan := build(t, Inputs{Groups: []hue.GroupView{g}, Cfg: baseCfg()})
		if n := plan.LightCount(); n != 0 {
			t.Fatalf("switched off %v, want nothing", targetNames(plan))
		}
		if len(plan.Notes) != 1 || !strings.Contains(plan.Notes[0].Text, "Sconce unreachable") {
			t.Errorf("notes = %+v, want one saying Sconce is unreachable", plan.Notes)
		}
	})

	t.Run("unreachable lights do not count toward the group", func(t *testing.T) {
		// 1 of 4 reachable lights on would be an outlier. Counting the two
		// dead lights as part of the group would wrongly make it 1 of 6.
		lights := lightsOnOf(4, 1)
		lights = append(lights,
			unreachable(light("d1", "Dead 1", true)),
			unreachable(light("d2", "Dead 2", false)))
		plan := build(t, Inputs{Groups: []hue.GroupView{room("West", lights)}, Cfg: baseCfg()})
		if got := targetNames(plan); len(got) != 1 || got[0] != "Light 1" {
			t.Fatalf("switched off %v, want [Light 1]", got)
		}
		if a := plan.Actions[0]; a.OnCount != 1 || a.Total != 4 {
			t.Errorf("counted %d of %d on, want 1 of 4", a.OnCount, a.Total)
		}
	})

	t.Run("unreachable plugs are not mentioned unless plugs are swept", func(t *testing.T) {
		g := room("Study", append(lightsOnOf(3, 0), unreachable(plug("p1", "Fridge", true))))
		cfg := baseCfg()
		cfg.IncludePlugs = false
		plan := build(t, Inputs{Groups: []hue.GroupView{g}, Cfg: cfg})
		if len(plan.Notes) != 0 {
			t.Errorf("notes = %+v, want none", plan.Notes)
		}
	})
}

func TestOverlappingGroupsAreSweptInOneRun(t *testing.T) {
	// Pendant and Sconce 2 are in both zones. Middle has 4 on, too many to look
	// forgotten, while East has just those 2. Once East's are planned off,
	// Middle is down to Arc and Cone, and West then has 1 left. One run must
	// reach that, not three.
	zone := func(name string, lights ...hue.LightView) hue.GroupView {
		for i := len(lights); i < 10; i++ {
			lights = append(lights, light(fmt.Sprintf("%s-off%d", name, i), fmt.Sprintf("%s off %d", name, i), false))
		}
		return hue.GroupView{ID: "z-" + name, Kind: hue.GroupZone, Name: name, Lights: lights}
	}
	pendant, sconce := light("c", "Pendant", true), light("t", "Sconce 2", true)
	arc, cone, dome := light("g", "Arc", true), light("r", "Cone", true), light("s", "Dome", true)
	groups := []hue.GroupView{
		zone("West", arc, cone, dome),
		zone("Middle", pendant, sconce, arc, cone),
		zone("East", pendant, sconce),
	}

	plan := build(t, Inputs{Groups: groups, Cfg: baseCfg()})

	got := targetNames(plan)
	sort.Strings(got)
	if want := []string{"Arc", "Cone", "Dome", "Pendant", "Sconce 2"}; !slices.Equal(got, want) {
		t.Fatalf("switched off %v, want %v", got, want)
	}
	rounds := map[string]int{}
	for _, a := range plan.Actions {
		rounds[a.Group] = a.Round
	}
	if want := map[string]int{"East": 1, "Middle": 2, "West": 3}; !maps.Equal(rounds, want) {
		t.Errorf("rounds = %v, want %v", rounds, want)
	}
	for _, a := range plan.Actions {
		if a.Round > 1 && !strings.Contains(a.Reason, "once other groups were swept") {
			t.Errorf("%s reason %q should say it followed earlier rounds", a.Group, a.Reason)
		}
	}
	if len(plan.Notes) != 0 {
		t.Errorf("notes = %+v; with everything swept there should be nothing left to explain", plan.Notes)
	}
}

func TestRecentMotionProtectsLightsInEveryGroup(t *testing.T) {
	// Someone was in the kitchen a minute ago. The Lower Level zone has no
	// sensor of its own (the lounge has none), and 2 of its 10 lights are on:
	// one in the kitchen, one in the lounge. The zone may switch off the
	// lounge light, but not the kitchen one.
	pendant := light("k1", "Pendant", true)
	sofa := light("l1", "Sofa Lamp", true)
	kitchen := room("Kitchen", []hue.LightView{pendant, light("k2", "Strip", false)}, sensorSeen("Kitchen Sensor", time.Minute))
	lounge := room("Lounge", append([]hue.LightView{sofa}, lightsPrefixed("Lounge", 7, 0)...))
	zone := hue.GroupView{ID: "z1", Kind: hue.GroupZone, Name: "Lower Level",
		Lights: append([]hue.LightView{pendant, light("k2", "Strip", false), sofa}, lightsPrefixed("Lounge", 7, 0)...)}

	plan := build(t, Inputs{Groups: []hue.GroupView{kitchen, lounge, zone}, Cfg: baseCfg()})

	if got := targetNames(plan); !slices.Equal(got, []string{"Sofa Lamp"}) {
		t.Fatalf("switched off %v, want only [Sofa Lamp]", got)
	}
	found := false
	for _, n := range plan.Notes {
		if strings.Contains(n.Text, "Pendant kept on: recent motion in Kitchen") {
			found = true
		}
	}
	if !found {
		t.Errorf("notes = %+v, want one saying Pendant was kept on", plan.Notes)
	}

	t.Run("an idle sensor protects nothing", func(t *testing.T) {
		idle := kitchen
		idle.Sensors = []hue.SensorView{sensorSeen("Kitchen Sensor", time.Hour)}
		plan := build(t, Inputs{Groups: []hue.GroupView{idle, lounge, zone}, Cfg: baseCfg()})
		got := targetNames(plan)
		sort.Strings(got)
		if !slices.Equal(got, []string{"Pendant", "Sofa Lamp"}) {
			t.Errorf("switched off %v, want [Pendant Sofa Lamp]", got)
		}
	})
}

func TestRoomSelection(t *testing.T) {
	groups := []hue.GroupView{
		room("Kitchen", lightsPrefixed("Kitchen", 5, 1)),
		room("Garage", lightsPrefixed("Garage", 5, 1)),
	}

	t.Run("an empty list sweeps everything", func(t *testing.T) {
		plan := build(t, Inputs{Groups: groups, Cfg: baseCfg()})
		if n := plan.LightCount(); n != 2 {
			t.Fatalf("switched off %d lights, want 2", n)
		}
	})

	t.Run("a configured list limits the sweep", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Rooms = []string{"kitchen"} // case-insensitive
		plan := build(t, Inputs{Groups: groups, Cfg: cfg})
		if n := plan.LightCount(); n != 1 {
			t.Fatalf("switched off %d lights, want 1", n)
		}
		if plan.Actions[0].Group != "Kitchen" {
			t.Errorf("swept %q, want Kitchen", plan.Actions[0].Group)
		}
	})

	t.Run("an override beats the config", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Rooms = []string{"Kitchen"}
		plan := build(t, Inputs{Groups: groups, Cfg: cfg, RoomsOverride: []string{"Garage"}})
		if len(plan.Actions) != 1 || plan.Actions[0].Group != "Garage" {
			t.Fatalf("expected only Garage, got %+v", plan.Actions)
		}
	})

	t.Run("an unknown room is reported", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Rooms = []string{"Kitchen", "Dungeon"}
		plan := build(t, Inputs{Groups: groups, Cfg: cfg})
		if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "Dungeon") {
			t.Fatalf("expected a warning about Dungeon, got %+v", plan.Warnings)
		}
	})

	t.Run("whitespace and empty entries are tolerated", func(t *testing.T) {
		cfg := baseCfg()
		cfg.Rooms = []string{"  Kitchen  ", ""}
		plan := build(t, Inputs{Groups: groups, Cfg: cfg})
		if len(plan.Actions) != 1 || plan.Actions[0].Group != "Kitchen" {
			t.Fatalf("expected Kitchen only, got %+v", plan.Actions)
		}
		if len(plan.Warnings) != 0 {
			t.Errorf("unexpected warnings: %+v", plan.Warnings)
		}
	})
}

func TestLightInRoomAndZoneIsSweptOnce(t *testing.T) {
	// The same light service belongs to a room and to a zone.
	shared := light("l1", "Porch Left", true)
	roomGroup := room("Porch", []hue.LightView{
		shared, light("l2", "Porch Right", false), light("l3", "Porch Step", false),
	})
	zoneGroup := hue.GroupView{
		ID: "z1", Kind: hue.GroupZone, Name: "Outside", GroupedLights: []hue.GroupedLightRef{{ID: "glz"}},
		Lights: []hue.LightView{
			shared, light("l9", "Garden A", false), light("l8", "Garden B", false),
		},
	}

	plan := build(t, Inputs{Groups: []hue.GroupView{roomGroup, zoneGroup}, Cfg: baseCfg()})
	names := targetNames(plan)
	if len(names) != 1 {
		t.Fatalf("switched off %v, want exactly one entry", names)
	}
	if names[0] != "Porch Left" {
		t.Errorf("switched off %q, want Porch Left", names[0])
	}
}

func TestZoneWithInheritedSensorsUsesMotionRule(t *testing.T) {
	zone := hue.GroupView{
		ID: "z1", Kind: hue.GroupZone, Name: "Outside", GroupedLights: []hue.GroupedLightRef{{ID: "glz"}},
		Lights:           lightsOnOf(6, 6),
		Sensors:          []hue.SensorView{sensorSeen("Driveway", 30*time.Minute)},
		SensorsInherited: true,
	}
	plan := build(t, Inputs{Groups: []hue.GroupView{zone}, Cfg: baseCfg()})
	if n := plan.LightCount(); n != 6 {
		t.Fatalf("switched off %d lights, want 6", n)
	}
	if plan.Actions[0].Rule != RuleMotionIdle {
		t.Errorf("rule = %q, want %q", plan.Actions[0].Rule, RuleMotionIdle)
	}
}

func TestPlanMetadata(t *testing.T) {
	plan := build(t, Inputs{
		Groups: []hue.GroupView{room("Kitchen", lightsOnOf(5, 1))},
		Cfg:    baseCfg(),
	})
	if len(plan.Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(plan.Actions))
	}
	a := plan.Actions[0]
	if a.OnCount != 1 || a.Total != 5 {
		t.Errorf("OnCount/Total = %d/%d, want 1/5", a.OnCount, a.Total)
	}
	if a.GroupKind != hue.GroupRoom || a.GroupID != "g-Kitchen" {
		t.Errorf("group identity = %q/%q", a.GroupKind, a.GroupID)
	}
	if !plan.Now.Equal(testNow) {
		t.Errorf("plan time = %v, want %v", plan.Now, testNow)
	}
}

func TestEmptyInputs(t *testing.T) {
	plan := build(t, Inputs{Groups: nil, Cfg: baseCfg()})
	if plan.LightCount() != 0 || len(plan.Actions) != 0 {
		t.Fatalf("expected an empty plan, got %+v", plan)
	}
}
