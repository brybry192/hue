package hue

import (
	"testing"
	"time"
)

var now = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// --- fixtures ---------------------------------------------------------------

func lightDevice(id, name string, serviceIDs ...string) Device {
	d := Device{
		ID:          id,
		Type:        "device",
		Metadata:    Metadata{Name: name},
		ProductData: ProductData{ModelID: "LCA001", ProductName: "Hue color lamp"},
	}
	for _, sid := range serviceIDs {
		d.Services = append(d.Services, ResourceRef{RID: sid, RType: "light"})
	}
	return d
}

func sensorDevice(id, name, motionID string) Device {
	return Device{
		ID:          id,
		Type:        "device",
		Metadata:    Metadata{Name: name},
		ProductData: ProductData{ModelID: "SML001", ProductName: "Hue motion sensor"},
		Services: []ResourceRef{
			{RID: motionID, RType: "motion"},
			{RID: "t-" + id, RType: "temperature"},
		},
	}
}

func dimmerDevice(id, name string) Device {
	d := Device{
		ID:          id,
		Type:        "device",
		Metadata:    Metadata{Name: name},
		ProductData: ProductData{ModelID: "RWL022", ProductName: "Hue dimmer switch"},
	}
	for i := range 4 {
		d.Services = append(d.Services, ResourceRef{RID: string(rune('a'+i)) + id, RType: "button"})
	}
	return d
}

func lightRes(id, name string, on bool, brightness float64) Light {
	return Light{
		ID:       id,
		Owner:    ResourceRef{RID: "dev-" + id, RType: "device"},
		Metadata: Metadata{Name: name},
		On:       OnState{On: on},
		Dimming:  &Dimming{Brightness: brightness},
	}
}

func motionRes(id, owner string, motion bool, changed time.Time) Motion {
	return Motion{
		ID:      id,
		Owner:   ResourceRef{RID: owner, RType: "device"},
		Enabled: true,
		Motion: MotionState{
			Motion:       motion,
			MotionReport: &MotionReport{Changed: changed, Motion: motion},
		},
	}
}

// --- tests ------------------------------------------------------------------

func TestBuildSnapshotRoom(t *testing.T) {
	// A room with two lights, a motion sensor and a dimmer switch.
	devices := []Device{
		lightDevice("dev-l1", "Kitchen Ceiling", "l1"),
		lightDevice("dev-l2", "Kitchen Spot", "l2"),
		sensorDevice("dev-s1", "Kitchen Sensor", "m1"),
		dimmerDevice("dev-b1", "Kitchen Dimmer"),
	}
	lights := []Light{
		lightRes("l1", "Kitchen Ceiling", true, 80),
		lightRes("l2", "Kitchen Spot", false, 0),
	}
	motions := []Motion{motionRes("m1", "dev-s1", false, now.Add(-5*time.Minute))}
	rooms := []Group{{
		ID:       "r1",
		Type:     "room",
		Metadata: Metadata{Name: "Kitchen"},
		Children: []ResourceRef{
			{RID: "dev-l1", RType: "device"},
			{RID: "dev-l2", RType: "device"},
			{RID: "dev-s1", RType: "device"},
			{RID: "dev-b1", RType: "device"},
		},
		Services: []ResourceRef{
			{RID: "gl1", RType: "grouped_light"},
			{RID: "other", RType: "zigbee_connectivity"},
		},
	}}

	snap := BuildSnapshot(rooms, nil, devices, lights, motions)
	if len(snap.Groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(snap.Groups))
	}
	g := snap.Groups[0]

	if g.Name != "Kitchen" || g.Kind != GroupRoom {
		t.Errorf("group = %q/%q", g.Name, g.Kind)
	}
	if len(g.GroupedLights) != 1 || g.GroupedLights[0].ID != "gl1" {
		t.Errorf("grouped lights = %+v, want gl1", g.GroupedLights)
	}
	if len(g.Lights) != 2 {
		t.Fatalf("got %d lights, want 2", len(g.Lights))
	}
	// Sorted by name: Kitchen Ceiling before Kitchen Spot.
	if g.Lights[0].Name != "Kitchen Ceiling" || !g.Lights[0].On {
		t.Errorf("first light = %+v", g.Lights[0])
	}
	if g.Lights[0].Brightness != 80 || !g.Lights[0].HasDimming {
		t.Errorf("brightness not carried through: %+v", g.Lights[0])
	}
	if g.OnCount() != 1 {
		t.Errorf("OnCount = %d, want 1", g.OnCount())
	}
	if len(g.Sensors) != 1 || g.Sensors[0].Name != "Kitchen Sensor" {
		t.Errorf("sensors = %+v", g.Sensors)
	}
	if len(g.Controls) != 1 || g.Controls[0].Kind != KindSwitch {
		t.Errorf("controls = %+v", g.Controls)
	}
	if g.Controls[0].Buttons != 4 {
		t.Errorf("buttons = %d, want 4", g.Controls[0].Buttons)
	}
	if len(snap.Lights) != 2 {
		t.Errorf("snapshot light index has %d entries, want 2", len(snap.Lights))
	}
}

func TestBuildSnapshotZoneUsesLightChildren(t *testing.T) {
	// Zones list light services directly rather than devices, and inherit
	// their sensors from the rooms the lights live in.
	devices := []Device{
		lightDevice("dev-l1", "Porch Left", "l1"),
		lightDevice("dev-l2", "Garden", "l2"),
		sensorDevice("dev-s1", "Porch Sensor", "m1"),
	}
	lights := []Light{
		lightRes("l1", "Porch Left", true, 50),
		lightRes("l2", "Garden", true, 60),
	}
	motions := []Motion{motionRes("m1", "dev-s1", false, now.Add(-30*time.Minute))}
	rooms := []Group{{
		ID: "r1", Type: "room", Metadata: Metadata{Name: "Porch"},
		Children: []ResourceRef{
			{RID: "dev-l1", RType: "device"},
			{RID: "dev-l2", RType: "device"},
			{RID: "dev-s1", RType: "device"},
		},
		Services: []ResourceRef{{RID: "gl1", RType: "grouped_light"}},
	}}
	zones := []Group{{
		ID: "z1", Type: "zone", Metadata: Metadata{Name: "Outside"},
		Children: []ResourceRef{
			{RID: "l1", RType: "light"},
			{RID: "l2", RType: "light"},
		},
		Services: []ResourceRef{{RID: "glz", RType: "grouped_light"}},
	}}

	snap := BuildSnapshot(rooms, zones, devices, lights, motions)
	zone, ok := snap.Group("outside") // case-insensitive lookup
	if !ok {
		t.Fatal("zone Outside not found")
	}
	if zone.Kind != GroupZone {
		t.Errorf("kind = %q, want zone", zone.Kind)
	}
	if len(zone.Lights) != 2 {
		t.Fatalf("got %d lights, want 2", len(zone.Lights))
	}
	if !zone.SensorsInherited || len(zone.Sensors) != 1 {
		t.Errorf("zone should inherit the Porch sensor, got %+v (inherited=%v)",
			zone.Sensors, zone.SensorsInherited)
	}
	if len(zone.GroupedLights) != 1 || zone.GroupedLights[0].ID != "glz" {
		t.Errorf("grouped lights = %+v, want glz", zone.GroupedLights)
	}
}

func TestApplyConnectivity(t *testing.T) {
	// One light in both a room and a zone has lost power; one is fine; one
	// has no connectivity record at all.
	devices := []Device{
		lightDevice("dev-l1", "Sconce", "l1"),
		lightDevice("dev-l2", "Desk", "l2"),
		lightDevice("dev-l3", "Strip", "l3"),
	}
	lights := []Light{
		lightRes("l1", "Sconce", true, 50),
		lightRes("l2", "Desk", true, 50),
		lightRes("l3", "Strip", false, 0),
	}
	rooms := []Group{{
		ID: "r1", Type: "room", Metadata: Metadata{Name: "Study"},
		Children: []ResourceRef{
			{RID: "dev-l1", RType: "device"},
			{RID: "dev-l2", RType: "device"},
			{RID: "dev-l3", RType: "device"},
		},
	}}
	zones := []Group{{
		ID: "z1", Type: "zone", Metadata: Metadata{Name: "Desk Area"},
		Children: []ResourceRef{{RID: "l1", RType: "light"}},
	}}

	snap := BuildSnapshot(rooms, zones, devices, lights, nil)
	snap.ApplyConnectivity([]ZigbeeConnectivity{
		{ID: "zc1", Owner: ResourceRef{RID: "dev-l1", RType: "device"}, Status: "connectivity_issue"},
		{ID: "zc2", Owner: ResourceRef{RID: "dev-l2", RType: "device"}, Status: ConnectivityConnected},
	})

	want := map[string]bool{"l1": true, "l2": false, "l3": false}
	for id, unreachable := range want {
		if got := snap.Lights[id].Unreachable; got != unreachable {
			t.Errorf("Lights[%s].Unreachable = %v, want %v", id, got, unreachable)
		}
	}
	for _, g := range snap.Groups {
		for _, l := range g.Lights {
			if l.Unreachable != want[l.ID] {
				t.Errorf("%s: %s.Unreachable = %v, want %v", g.Name, l.Name, l.Unreachable, want[l.ID])
			}
		}
	}

	office, _ := snap.Group("Study")
	if n := office.OnCount(); n != 1 {
		t.Errorf("OnCount = %d, want 1: the unreachable light's stale on must not count", n)
	}
}

func TestZoneInheritsOnlyWhenEveryLightIsSensed(t *testing.T) {
	// Lower Level spans a kitchen with a sensor and a lounge without one. The
	// kitchen sensor says nothing about the lounge, so the zone must not
	// inherit it; Pantry, wholly inside the kitchen, may.
	devices := []Device{
		lightDevice("dev-k1", "Pendant", "k1"),
		lightDevice("dev-l1", "Sofa Lamp", "l1"),
		sensorDevice("dev-s1", "Kitchen Sensor", "m1"),
	}
	lights := []Light{lightRes("k1", "Pendant", true, 50), lightRes("l1", "Sofa Lamp", true, 50)}
	motions := []Motion{motionRes("m1", "dev-s1", false, now.Add(-time.Hour))}
	rooms := []Group{
		{ID: "r1", Type: "room", Metadata: Metadata{Name: "Kitchen"},
			Children: []ResourceRef{{RID: "dev-k1", RType: "device"}, {RID: "dev-s1", RType: "device"}}},
		{ID: "r2", Type: "room", Metadata: Metadata{Name: "Lounge"},
			Children: []ResourceRef{{RID: "dev-l1", RType: "device"}}},
	}
	zones := []Group{
		{ID: "z1", Type: "zone", Metadata: Metadata{Name: "Lower Level"},
			Children: []ResourceRef{{RID: "k1", RType: "light"}, {RID: "l1", RType: "light"}}},
		{ID: "z2", Type: "zone", Metadata: Metadata{Name: "Pantry"},
			Children: []ResourceRef{{RID: "k1", RType: "light"}}},
	}

	snap := BuildSnapshot(rooms, zones, devices, lights, motions)
	if z, _ := snap.Group("Lower Level"); z.SensorsInherited || len(z.Sensors) != 0 {
		t.Errorf("Lower Level inherited %+v; the lounge has no sensor, so it should inherit none", z.Sensors)
	}
	if z, _ := snap.Group("Pantry"); !z.SensorsInherited || len(z.Sensors) != 1 {
		t.Errorf("Pantry sensors = %+v, want the Kitchen Sensor", z.Sensors)
	}
}

func TestBuildSnapshotSkipsDanglingReferences(t *testing.T) {
	// A room that points at a device the bridge did not return must not panic.
	rooms := []Group{{
		ID: "r1", Type: "room", Metadata: Metadata{Name: "Ghost"},
		Children: []ResourceRef{
			{RID: "missing-device", RType: "device"},
			{RID: "l1", RType: "light"}, // wrong rtype for a room
		},
	}}
	snap := BuildSnapshot(rooms, nil, nil, nil, nil)
	if len(snap.Groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(snap.Groups))
	}
	if len(snap.Groups[0].Lights) != 0 {
		t.Errorf("expected no lights, got %+v", snap.Groups[0].Lights)
	}
}

func TestBuildSnapshotMultiLightDevice(t *testing.T) {
	// Some fixtures expose several light services from one device.
	devices := []Device{lightDevice("dev-m", "Centris", "l1", "l2", "l3")}
	lights := []Light{
		{ID: "l1", Owner: ResourceRef{RID: "dev-m"}, Metadata: Metadata{Name: "Centris 1"}, On: OnState{On: true}},
		{ID: "l2", Owner: ResourceRef{RID: "dev-m"}, Metadata: Metadata{Name: "Centris 2"}, On: OnState{On: false}},
		{ID: "l3", Owner: ResourceRef{RID: "dev-m"}, Metadata: Metadata{Name: "Centris 3"}, On: OnState{On: false}},
	}
	rooms := []Group{{
		ID: "r1", Type: "room", Metadata: Metadata{Name: "Hall"},
		Children: []ResourceRef{{RID: "dev-m", RType: "device"}},
	}}

	snap := BuildSnapshot(rooms, nil, devices, lights, nil)
	g := snap.Groups[0]
	if len(g.Lights) != 3 {
		t.Fatalf("got %d lights, want 3", len(g.Lights))
	}
	for _, l := range g.Lights {
		if l.DeviceName != "Centris" {
			t.Errorf("light %q has device name %q", l.Name, l.DeviceName)
		}
	}
}

func TestGroupsAreSortedByName(t *testing.T) {
	rooms := []Group{
		{ID: "r1", Metadata: Metadata{Name: "Zebra"}},
		{ID: "r2", Metadata: Metadata{Name: "Attic"}},
	}
	zones := []Group{{ID: "z1", Metadata: Metadata{Name: "Middle"}}}
	snap := BuildSnapshot(rooms, zones, nil, nil, nil)

	want := []string{"Attic", "Middle", "Zebra"}
	for i, w := range want {
		if snap.Groups[i].Name != w {
			t.Errorf("group %d = %q, want %q", i, snap.Groups[i].Name, w)
		}
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		dev  Device
		want DeviceKind
	}{
		{
			name: "a colour lamp is a light",
			dev:  lightDevice("d1", "Lamp", "l1"),
			want: KindLight,
		},
		{
			name: "a smart plug is a plug",
			dev: Device{
				ProductData: ProductData{ProductName: "Hue smart plug", ProductArchetype: "plug"},
				Services:    []ResourceRef{{RType: "light"}},
			},
			want: KindPlug,
		},
		{
			name: "a plug archetype wins over the product name",
			dev: Device{
				ProductData: ProductData{ProductName: "Generic outlet", ProductArchetype: "plug"},
				Services:    []ResourceRef{{RType: "light"}},
			},
			want: KindPlug,
		},
		{
			name: "a motion sensor is a sensor",
			dev:  sensorDevice("d2", "Sensor", "m1"),
			want: KindSensor,
		},
		{
			name: "a dimmer switch is a switch",
			dev:  dimmerDevice("d3", "Dimmer"),
			want: KindSwitch,
		},
		{
			name: "a smart button is a switch",
			dev: Device{
				ProductData: ProductData{ModelID: "ROM001", ProductName: "Hue smart button"},
				Services:    []ResourceRef{{RType: "button"}},
			},
			want: KindSwitch,
		},
		{
			name: "a tap dial is a remote",
			dev: Device{
				ProductData: ProductData{ModelID: "RDM002", ProductName: "Hue tap dial switch"},
				Services: []ResourceRef{
					{RType: "button"}, {RType: "button"}, {RType: "button"}, {RType: "button"},
					{RType: "relative_rotary"},
				},
			},
			want: KindRemote,
		},
		{
			name: "an original Hue Tap is a remote",
			dev: Device{
				ProductData: ProductData{ModelID: "ZGPSWITCH", ProductName: "Hue tap switch"},
				Services:    []ResourceRef{{RType: "button"}},
			},
			want: KindRemote,
		},
		{
			name: "a bare temperature sensor is a sensor",
			dev: Device{
				ProductData: ProductData{ProductName: "Thermo"},
				Services:    []ResourceRef{{RType: "temperature"}},
			},
			want: KindSensor,
		},
		{
			name: "an unrecognised device is other",
			dev: Device{
				ProductData: ProductData{ProductName: "Mystery"},
				Services:    []ResourceRef{{RType: "zigbee_connectivity"}},
			},
			want: KindOther,
		},
		{
			name: "an unknown button device defaults to a switch",
			dev: Device{
				ProductData: ProductData{ProductName: "Third party clicker"},
				Services:    []ResourceRef{{RType: "button"}},
			},
			want: KindSwitch,
		},
		{
			name: "a four button unknown device is a remote",
			dev: Device{
				ProductData: ProductData{ProductName: "Third party pad"},
				Services: []ResourceRef{
					{RType: "button"}, {RType: "button"}, {RType: "button"}, {RType: "button"},
				},
			},
			want: KindRemote,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.dev, serviceCounts(tc.dev)); got != tc.want {
				t.Errorf("Classify = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLastMotion(t *testing.T) {
	tests := []struct {
		name    string
		sensors []SensorView
		wantOK  bool
		wantAgo time.Duration
	}{
		{
			name:   "no sensors",
			wantOK: false,
		},
		{
			name: "one idle sensor",
			sensors: []SensorView{
				{Enabled: true, HasReport: true, LastChanged: now.Add(-20 * time.Minute)},
			},
			wantOK:  true,
			wantAgo: 20 * time.Minute,
		},
		{
			name: "active motion counts as now",
			sensors: []SensorView{
				{Enabled: true, HasReport: true, Motion: true, LastChanged: now.Add(-time.Hour)},
			},
			wantOK:  true,
			wantAgo: 0,
		},
		{
			name: "the most recent sensor wins",
			sensors: []SensorView{
				{Enabled: true, HasReport: true, LastChanged: now.Add(-40 * time.Minute)},
				{Enabled: true, HasReport: true, LastChanged: now.Add(-3 * time.Minute)},
			},
			wantOK:  true,
			wantAgo: 3 * time.Minute,
		},
		{
			name: "a disabled sensor is unusable",
			sensors: []SensorView{
				{Enabled: false, HasReport: true, LastChanged: now},
			},
			wantOK: false,
		},
		{
			name: "a sensor with no report is unusable",
			sensors: []SensorView{
				{Enabled: true, HasReport: false},
			},
			wantOK: false,
		},
		{
			name: "a usable sensor alongside an unusable one still answers",
			sensors: []SensorView{
				{Enabled: true, HasReport: false},
				{Enabled: true, HasReport: true, LastChanged: now.Add(-10 * time.Minute)},
			},
			wantOK:  true,
			wantAgo: 10 * time.Minute,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := GroupView{Sensors: tc.sensors}
			got, ok := g.LastMotion(now)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if ago := now.Sub(got); ago != tc.wantAgo {
				t.Errorf("last motion %v ago, want %v", ago, tc.wantAgo)
			}
		})
	}
}

func TestSensorViewPrefersMotionReport(t *testing.T) {
	// motion_report is authoritative when the firmware provides it.
	m := Motion{
		ID:      "m1",
		Owner:   ResourceRef{RID: "d1"},
		Enabled: true,
		Motion: MotionState{
			Motion:       false,
			MotionReport: &MotionReport{Changed: now.Add(-time.Minute), Motion: true},
		},
	}
	v := sensorView(m, Device{Metadata: Metadata{Name: "Sensor"}})
	if !v.HasReport {
		t.Fatal("HasReport should be true")
	}
	if !v.Motion {
		t.Error("Motion should come from the report")
	}

	t.Run("falls back when the report is absent", func(t *testing.T) {
		old := Motion{ID: "m2", Enabled: true, Motion: MotionState{Motion: true}}
		v := sensorView(old, Device{Metadata: Metadata{Name: "Old"}})
		if v.HasReport {
			t.Error("HasReport should be false without motion_report")
		}
		if !v.Motion {
			t.Error("the legacy motion flag should still be read")
		}
	})
}

func TestSensorDescribe(t *testing.T) {
	tests := []struct {
		name   string
		sensor SensorView
		want   string
	}{
		{"disabled", SensorView{Enabled: false}, "disabled"},
		{"no report", SensorView{Enabled: true}, "no motion report"},
		{"active", SensorView{Enabled: true, HasReport: true, Motion: true}, "motion now"},
		{
			"idle",
			SensorView{Enabled: true, HasReport: true, LastChanged: now.Add(-12 * time.Minute)},
			"motion 12m ago",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sensor.Describe(now); got != tc.want {
				t.Errorf("Describe = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestShortDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{-5 * time.Second, "0s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m"},
		{59 * time.Minute, "59m"},
		{time.Hour, "1h"},
		{3*time.Hour + 10*time.Minute, "3h10m"},
		{25 * time.Hour, "1d1h"},
		{48 * time.Hour, "2d"},
	}
	for _, tc := range tests {
		if got := ShortDuration(tc.in); got != tc.want {
			t.Errorf("ShortDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSnapshotGroupLookup(t *testing.T) {
	rooms := []Group{{ID: "r1", Metadata: Metadata{Name: "Atrium"}}}
	snap := BuildSnapshot(rooms, nil, nil, nil, nil)

	for _, name := range []string{"Atrium", "atrium", "  ATRIUM  "} {
		if _, ok := snap.Group(name); !ok {
			t.Errorf("lookup of %q failed", name)
		}
	}
	if _, ok := snap.Group("Nowhere"); ok {
		t.Error("lookup of a missing group should fail")
	}
}

func TestNormalizeHost(t *testing.T) {
	tests := map[string]string{
		"192.0.2.10":          "192.0.2.10",
		" 192.0.2.10 ":        "192.0.2.10",
		"https://192.0.2.10":  "192.0.2.10",
		"https://192.0.2.10/": "192.0.2.10",
		"192.0.2.10:443":      "192.0.2.10",
		"192.0.2.10:8443":     "192.0.2.10:8443",
		"hue.local":           "hue.local",
		"":                    "",
	}
	for in, want := range tests {
		if got := NormalizeHost(in); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}
