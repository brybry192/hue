// Package hue is a small client for the Philips Hue CLIP v2 API.
//
// Only the handful of resource types this CLI needs are modelled; the bridge
// returns a lot more per resource than is declared here and unknown fields are
// simply ignored.
package hue

import "time"

// ResourceRef points at another CLIP v2 resource.
type ResourceRef struct {
	RID   string `json:"rid"`
	RType string `json:"rtype"`
}

// Metadata is the user-facing name/archetype block shared by most resources.
type Metadata struct {
	Name      string `json:"name"`
	Archetype string `json:"archetype"`
}

// Group models both /resource/room and /resource/zone, which share a shape.
//
// The difference matters when walking Children: a room's children are devices,
// while a zone's children are light services directly.
type Group struct {
	ID       string        `json:"id"`
	Type     string        `json:"type"`
	Metadata Metadata      `json:"metadata"`
	Children []ResourceRef `json:"children"`
	Services []ResourceRef `json:"services"`
}

// ProductData identifies the hardware behind a device.
type ProductData struct {
	ModelID          string `json:"model_id"`
	ProductName      string `json:"product_name"`
	ProductArchetype string `json:"product_archetype"`
}

// Device is a physical thing. Its capabilities are exposed as Services.
type Device struct {
	ID          string        `json:"id"`
	Type        string        `json:"type"`
	ProductData ProductData   `json:"product_data"`
	Metadata    Metadata      `json:"metadata"`
	Services    []ResourceRef `json:"services"`
}

// OnState is the on/off block used by lights and grouped lights.
type OnState struct {
	On bool `json:"on"`
}

// Dimming carries brightness as a percentage.
type Dimming struct {
	Brightness float64 `json:"brightness"`
}

// Light is a single controllable light service.
type Light struct {
	ID       string      `json:"id"`
	Owner    ResourceRef `json:"owner"`
	Metadata Metadata    `json:"metadata"`
	On       OnState     `json:"on"`
	Dimming  *Dimming    `json:"dimming"`
}

// GroupedLight controls every light in a room or zone at once.
type GroupedLight struct {
	ID    string      `json:"id"`
	Owner ResourceRef `json:"owner"`
	On    OnState     `json:"on"`
}

// MotionReport is the timestamped motion event. Changed is when the motion
// value last flipped, which is what makes "how long has this room been idle"
// answerable at all.
type MotionReport struct {
	Changed time.Time `json:"changed"`
	Motion  bool      `json:"motion"`
}

// MotionState is the motion block of a motion service. MotionValid is the
// deprecated predecessor of MotionReport and is only read as a fallback.
type MotionState struct {
	Motion       bool          `json:"motion"`
	MotionValid  *bool         `json:"motion_valid"`
	MotionReport *MotionReport `json:"motion_report"`
}

// Motion is a motion-sensing service owned by a sensor device.
type Motion struct {
	ID      string      `json:"id"`
	Owner   ResourceRef `json:"owner"`
	Enabled bool        `json:"enabled"`
	Motion  MotionState `json:"motion"`
}

// BridgeInfo is the bridge's own resource, used for a cheap identity check.
type BridgeInfo struct {
	ID       string `json:"id"`
	BridgeID string `json:"bridge_id"`
	TimeZone struct {
		TimeZone string `json:"time_zone"`
	} `json:"time_zone"`
}
