package hue

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// DeviceKind is how this CLI labels a device for humans.
type DeviceKind string

const (
	KindLight  DeviceKind = "light"
	KindPlug   DeviceKind = "plug"
	KindSensor DeviceKind = "sensor"
	KindSwitch DeviceKind = "switch"
	KindRemote DeviceKind = "remote"
	KindOther  DeviceKind = "other"
)

// GroupKind distinguishes rooms from zones.
const (
	GroupRoom = "room"
	GroupZone = "zone"
)

// LightView is one light service, flattened with the bits the CLI needs.
type LightView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	DeviceID   string     `json:"device_id"`
	DeviceName string     `json:"device_name"`
	Kind       DeviceKind `json:"kind"`
	On         bool       `json:"on"`
	Brightness float64    `json:"brightness,omitempty"`
	HasDimming bool       `json:"-"`
}

// SensorView is one motion service.
type SensorView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	DeviceID    string    `json:"device_id"`
	Enabled     bool      `json:"enabled"`
	Motion      bool      `json:"motion"`
	LastChanged time.Time `json:"last_changed,omitempty"`
	// HasReport is false for firmware old enough to lack motion_report, in
	// which case LastChanged is meaningless.
	HasReport bool `json:"has_report"`
}

// ControlView is a switch, dial or other non-light device.
type ControlView struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Kind    DeviceKind `json:"kind"`
	Product string     `json:"product,omitempty"`
	Buttons int        `json:"buttons,omitempty"`
}

// GroupView is a room or zone with everything in it.
type GroupView struct {
	ID             string        `json:"id"`
	Kind           string        `json:"kind"`
	Name           string        `json:"name"`
	GroupedLightID string        `json:"grouped_light_id,omitempty"`
	Lights         []LightView   `json:"lights"`
	Sensors        []SensorView  `json:"sensors,omitempty"`
	Controls       []ControlView `json:"controls,omitempty"`
	// SensorsInherited is true when a zone borrowed its sensors from the
	// rooms that own its lights.
	SensorsInherited bool `json:"sensors_inherited,omitempty"`
}

// OnCount counts lights that are currently on.
func (g GroupView) OnCount() int {
	n := 0
	for _, l := range g.Lights {
		if l.On {
			n++
		}
	}
	return n
}

// LastMotion reports the most recent moment any usable sensor in the group saw
// motion. ok is false when the group has no sensor that can answer, in which
// case the motion rule must not be applied.
//
// A sensor that is currently detecting motion is treated as "now". Otherwise
// motion_report.changed is when motion last cleared, which is a close enough
// proxy for last motion given thresholds measured in minutes.
func (g GroupView) LastMotion(now time.Time) (time.Time, bool) {
	var last time.Time
	ok := false
	for _, s := range g.Sensors {
		if !s.Enabled || !s.HasReport {
			continue
		}
		if s.Motion {
			return now, true
		}
		ok = true
		if s.LastChanged.After(last) {
			last = s.LastChanged
		}
	}
	return last, ok
}

// Snapshot is a consistent-enough view of the whole bridge.
type Snapshot struct {
	Bridge  BridgeInfo
	Groups  []GroupView
	Lights  map[string]LightView
	Devices map[string]Device
}

// Group finds a room or zone by name, case-insensitively.
func (s *Snapshot) Group(name string) (GroupView, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, g := range s.Groups {
		if strings.ToLower(g.Name) == want {
			return g, true
		}
	}
	return GroupView{}, false
}

// Snapshot fetches every resource the CLI needs and assembles the view.
func (c *Client) Snapshot(ctx context.Context) (*Snapshot, error) {
	rooms, err := c.Rooms(ctx)
	if err != nil {
		return nil, err
	}
	zones, err := c.Zones(ctx)
	if err != nil {
		return nil, err
	}
	devices, err := c.Devices(ctx)
	if err != nil {
		return nil, err
	}
	lights, err := c.Lights(ctx)
	if err != nil {
		return nil, err
	}
	motions, err := c.Motions(ctx)
	if err != nil {
		return nil, err
	}
	return BuildSnapshot(rooms, zones, devices, lights, motions), nil
}

// BuildSnapshot assembles a Snapshot from raw resources. It is separated from
// the fetching so the assembly logic is directly testable.
func BuildSnapshot(rooms, zones []Group, devices []Device, lights []Light, motions []Motion) *Snapshot {
	devByID := make(map[string]Device, len(devices))
	for _, d := range devices {
		devByID[d.ID] = d
	}
	lightByID := make(map[string]Light, len(lights))
	for _, l := range lights {
		lightByID[l.ID] = l
	}
	motionByID := make(map[string]Motion, len(motions))
	for _, m := range motions {
		motionByID[m.ID] = m
	}

	snap := &Snapshot{
		Lights:  make(map[string]LightView, len(lights)),
		Devices: devByID,
	}

	// roomOfDevice lets a zone inherit the motion sensors of the rooms its
	// lights physically live in.
	roomOfDevice := make(map[string]string, len(devices))
	for _, r := range rooms {
		for _, child := range r.Children {
			if child.RType == "device" {
				roomOfDevice[child.RID] = r.ID
			}
		}
	}

	groups := make([]GroupView, 0, len(rooms)+len(zones))
	roomSensors := make(map[string][]SensorView, len(rooms))

	for _, r := range rooms {
		g := buildRoom(r, devByID, lightByID, motionByID)
		roomSensors[r.ID] = g.Sensors
		groups = append(groups, g)
	}
	for _, z := range zones {
		groups = append(groups, buildZone(z, devByID, lightByID, roomOfDevice, roomSensors))
	}

	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Name != groups[j].Name {
			return groups[i].Name < groups[j].Name
		}
		return groups[i].Kind < groups[j].Kind
	})
	snap.Groups = groups

	for _, g := range groups {
		for _, l := range g.Lights {
			snap.Lights[l.ID] = l
		}
	}
	return snap
}

func buildRoom(r Group, devByID map[string]Device, lightByID map[string]Light, motionByID map[string]Motion) GroupView {
	g := GroupView{
		ID:             r.ID,
		Kind:           GroupRoom,
		Name:           r.Metadata.Name,
		GroupedLightID: groupedLightID(r),
	}
	for _, child := range r.Children {
		if child.RType != "device" {
			continue
		}
		d, ok := devByID[child.RID]
		if !ok {
			continue
		}
		counts := serviceCounts(d)
		kind := Classify(d, counts)

		switch kind {
		case KindLight, KindPlug:
			for _, svc := range d.Services {
				if svc.RType != "light" {
					continue
				}
				if l, ok := lightByID[svc.RID]; ok {
					g.Lights = append(g.Lights, lightView(l, d, kind))
				}
			}
		case KindSensor:
			added := false
			for _, svc := range d.Services {
				if svc.RType != "motion" {
					continue
				}
				if m, ok := motionByID[svc.RID]; ok {
					g.Sensors = append(g.Sensors, sensorView(m, d))
					added = true
				}
			}
			if !added {
				// A sensor device with no motion service (e.g. a bare
				// temperature sensor) is still worth listing.
				g.Controls = append(g.Controls, controlView(d, kind, counts))
			}
		default:
			g.Controls = append(g.Controls, controlView(d, kind, counts))
		}
	}
	sortGroupContents(&g)
	return g
}

func buildZone(z Group, devByID map[string]Device, lightByID map[string]Light,
	roomOfDevice map[string]string, roomSensors map[string][]SensorView) GroupView {

	g := GroupView{
		ID:             z.ID,
		Kind:           GroupZone,
		Name:           z.Metadata.Name,
		GroupedLightID: groupedLightID(z),
	}
	seenRoom := make(map[string]bool)
	for _, child := range z.Children {
		if child.RType != "light" {
			continue
		}
		l, ok := lightByID[child.RID]
		if !ok {
			continue
		}
		d := devByID[l.Owner.RID]
		kind := Classify(d, serviceCounts(d))
		if kind != KindPlug {
			kind = KindLight
		}
		g.Lights = append(g.Lights, lightView(l, d, kind))

		// Zones contain light services, not devices, so they carry no
		// sensors of their own. Borrow them from the owning rooms.
		if roomID, ok := roomOfDevice[l.Owner.RID]; ok && !seenRoom[roomID] {
			seenRoom[roomID] = true
			if sensors := roomSensors[roomID]; len(sensors) > 0 {
				g.Sensors = append(g.Sensors, sensors...)
				g.SensorsInherited = true
			}
		}
	}
	sortGroupContents(&g)
	return g
}

func sortGroupContents(g *GroupView) {
	sort.SliceStable(g.Lights, func(i, j int) bool { return g.Lights[i].Name < g.Lights[j].Name })
	sort.SliceStable(g.Sensors, func(i, j int) bool { return g.Sensors[i].Name < g.Sensors[j].Name })
	sort.SliceStable(g.Controls, func(i, j int) bool { return g.Controls[i].Name < g.Controls[j].Name })
}

func groupedLightID(g Group) string {
	for _, svc := range g.Services {
		if svc.RType == "grouped_light" {
			return svc.RID
		}
	}
	return ""
}

func lightView(l Light, d Device, kind DeviceKind) LightView {
	name := l.Metadata.Name
	if name == "" {
		name = d.Metadata.Name
	}
	v := LightView{
		ID:         l.ID,
		Name:       name,
		DeviceID:   l.Owner.RID,
		DeviceName: d.Metadata.Name,
		Kind:       kind,
		On:         l.On.On,
	}
	if l.Dimming != nil {
		v.Brightness = l.Dimming.Brightness
		v.HasDimming = true
	}
	return v
}

func sensorView(m Motion, d Device) SensorView {
	v := SensorView{
		ID:       m.ID,
		Name:     d.Metadata.Name,
		DeviceID: m.Owner.RID,
		Enabled:  m.Enabled,
		Motion:   m.Motion.Motion,
	}
	if m.Motion.MotionReport != nil && !m.Motion.MotionReport.Changed.IsZero() {
		v.LastChanged = m.Motion.MotionReport.Changed
		v.HasReport = true
		// motion_report is authoritative when present.
		v.Motion = m.Motion.MotionReport.Motion
	}
	return v
}

func controlView(d Device, kind DeviceKind, counts map[string]int) ControlView {
	return ControlView{
		ID:      d.ID,
		Name:    d.Metadata.Name,
		Kind:    kind,
		Product: d.ProductData.ProductName,
		Buttons: counts["button"],
	}
}

func serviceCounts(d Device) map[string]int {
	counts := make(map[string]int, len(d.Services))
	for _, s := range d.Services {
		counts[s.RType]++
	}
	return counts
}

// Classify labels a device from its services and product data.
//
// The switch/remote split is a best-effort heuristic over Hue's product names
// and model IDs; both are only used for display.
func Classify(d Device, counts map[string]int) DeviceKind {
	switch {
	case counts["light"] > 0:
		if isPlug(d) {
			return KindPlug
		}
		return KindLight
	case counts["motion"] > 0:
		return KindSensor
	case counts["button"] > 0 || counts["relative_rotary"] > 0:
		return buttonKind(d, counts)
	case counts["temperature"] > 0 || counts["light_level"] > 0:
		return KindSensor
	}
	return KindOther
}

func isPlug(d Device) bool {
	if strings.EqualFold(d.ProductData.ProductArchetype, "plug") {
		return true
	}
	return strings.Contains(strings.ToLower(d.ProductData.ProductName), "plug")
}

func buttonKind(d Device, counts map[string]int) DeviceKind {
	id := strings.ToLower(d.ProductData.ProductName + " " + d.ProductData.ModelID)
	switch {
	case counts["relative_rotary"] > 0, // tap dial switch
		strings.Contains(id, "dial"),
		strings.Contains(id, "tap"),
		strings.Contains(id, "zgpswitch"):
		return KindRemote
	case strings.Contains(id, "dimmer"),
		strings.Contains(id, "rwl"),
		strings.Contains(id, "button"),
		strings.Contains(id, "wall switch"),
		strings.Contains(id, "rom00"):
		return KindSwitch
	}
	if counts["button"] >= 4 {
		return KindRemote
	}
	return KindSwitch
}

// Describe renders a sensor's motion state for display.
func (s SensorView) Describe(now time.Time) string {
	switch {
	case !s.Enabled:
		return "disabled"
	case !s.HasReport:
		return "no motion report"
	case s.Motion:
		return "motion now"
	default:
		return fmt.Sprintf("motion %s ago", ShortDuration(now.Sub(s.LastChanged)))
	}
}

// ShortDuration renders a duration compactly: 45s, 12m, 3h10m, 2d4h.
func ShortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) % 24
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, h)
	}
}
