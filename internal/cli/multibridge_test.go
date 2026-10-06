package cli

import (
	"fmt"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brybry192/hue/internal/hue"
)

// newSensorBridge builds the second bridge for the two-bridge tests:
//
//   - Kitchen:  a lightstrip that is on, and a sensor idle for 30 minutes
//   - Yard: a lantern that is off, and a sensor that saw motion just now
//
// Aliased Yard -> Porch, the recent motion should protect the Porch lights
// and the Outside zone on the main bridge.
func newSensorBridge(t *testing.T, now time.Time) *fakeBridge {
	t.Helper()
	f := &fakeBridge{t: t}

	room := func(id, name string, devs ...string) hue.Group {
		g := hue.Group{ID: id, Type: "room", Metadata: hue.Metadata{Name: name},
			Services: []hue.ResourceRef{{RID: "gl-" + id, RType: "grouped_light"}}}
		for _, d := range devs {
			g.Children = append(g.Children, hue.ResourceRef{RID: d, RType: "device"})
		}
		return g
	}
	light := func(devID, name string, on bool) {
		f.devices = append(f.devices, hue.Device{
			ID: devID, Type: "device", Metadata: hue.Metadata{Name: name},
			ProductData: hue.ProductData{ModelID: "LST002", ProductName: "Hue lightstrip"},
			Services:    []hue.ResourceRef{{RID: "l-" + devID, RType: "light"}},
		})
		f.lights = append(f.lights, hue.Light{
			ID: "l-" + devID, Owner: hue.ResourceRef{RID: devID, RType: "device"},
			Metadata: hue.Metadata{Name: name}, On: hue.OnState{On: on},
		})
	}
	sensor := func(devID, name string, last time.Time) {
		f.devices = append(f.devices, hue.Device{
			ID: devID, Type: "device", Metadata: hue.Metadata{Name: name},
			ProductData: hue.ProductData{ModelID: "SML001", ProductName: "Hue motion sensor"},
			Services:    []hue.ResourceRef{{RID: "m-" + devID, RType: "motion"}},
		})
		f.motions = append(f.motions, hue.Motion{
			ID: "m-" + devID, Owner: hue.ResourceRef{RID: devID, RType: "device"}, Enabled: true,
			Motion: hue.MotionState{MotionReport: &hue.MotionReport{Changed: last}},
		})
	}

	light("v2strip", "Strip", true)
	sensor("v2kitchensensor", "Kitchen Sensor", now.Add(-30*time.Minute))
	light("v2lantern", "Lantern", false)
	sensor("v2yardsensor", "Lawn Sensor", now.Add(-time.Minute))

	f.rooms = []hue.Group{
		room("v2kitchen", "Kitchen", "v2strip", "v2kitchensensor"),
		room("v2yard", "Yard", "v2lantern", "v2yardsensor"),
	}

	f.server = httptest.NewTLSServer(f.handler())
	t.Cleanup(f.server.Close)
	return f
}

// newTwoBridgeHarness is newHarness with the sensor bridge added as "annex".
func newTwoBridgeHarness(t *testing.T) (*harness, *fakeBridge) {
	t.Helper()
	h := newHarness(t, "")
	annex := newSensorBridge(t, h.now)

	cfg := fmt.Sprintf(`{
	  "bridges": [
	    {"name": "main", "host": %q, "app_key": "k1", "insecure": true},
	    {"name": "annex", "host": %q, "app_key": "k2", "insecure": true}
	  ],
	  "room_aliases": {"Yard": "Porch"},
	  "sweep": {"min_on_duration": "0s"}
	}`, h.bridge.URL(), annex.URL())
	if err := os.WriteFile(h.configPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return h, annex
}

func TestTwoBridgesSweepUsesSensorsAcrossBridges(t *testing.T) {
	h, annex := newTwoBridgeHarness(t)

	out, _, err := h.run(t, "sweep", "--no-dry-run", "--rooms", "Kitchen,Porch,Outside")
	if err != nil {
		t.Fatal(err)
	}

	// Kitchen: the annex sensor has been idle for 30 minutes, so the motion
	// rule switches off the lights on both bridges.
	if got, want := h.bridge.Writes(), []string{"light/l-kitchen1=off"}; !slices.Equal(got, want) {
		t.Errorf("main writes = %v, want %v", got, want)
	}
	if got, want := annex.Writes(), []string{"light/l-v2strip=off"}; !slices.Equal(got, want) {
		t.Errorf("annex writes = %v, want %v", got, want)
	}
	if !strings.Contains(out, `group="Kitchen" kind=room rule=motion-idle`) || !strings.Contains(out, `reason="no motion for 30m`) {
		t.Errorf("Kitchen should be decided by the annex sensor:\n%s", out)
	}
	// Porch Left alone would be an outlier, but Yard is aliased to
	// Porch and its sensor saw motion a minute ago. The Outside zone
	// inherits that sensor too.
	if strings.Contains(out, "Porch Left") {
		t.Errorf("Porch Left should be protected by the annex Yard sensor:\n%s", out)
	}
}

func TestTwoBridgesList(t *testing.T) {
	h, _ := newTwoBridgeHarness(t)
	out, _, err := h.run(t, "ls", "Kitchen", "Porch", "Outside")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []string{
		"ROOM GROUP DEVICE TYPE BRIDGE STATE BRIGHT DETAIL",
		"Kitchen room Kitchen 1 light main on 70%",
		"Kitchen room Strip light annex on -",
		"Kitchen room Kitchen Sensor sensor annex - - motion 30m ago",
		"Porch room Lantern light annex off -",
		"Outside zone Lawn Sensor sensor annex - - motion 1m ago (from room)",
	} {
		if !hasRow(out, row) {
			t.Errorf("ls is missing row %q\n%s", row, out)
		}
	}
	if strings.Contains(out, "Yard") {
		t.Errorf("Yard should be listed under its alias Porch:\n%s", out)
	}
}

func TestTwoBridgesOffSwitchesBothHalvesOfARoom(t *testing.T) {
	h, annex := newTwoBridgeHarness(t)
	if _, _, err := h.run(t, "off", "Kitchen", "--no-dry-run"); err != nil {
		t.Fatal(err)
	}
	if got, want := h.bridge.Writes(), []string{"grouped_light/gl-kitchen=off"}; !slices.Equal(got, want) {
		t.Errorf("main writes = %v, want %v", got, want)
	}
	if got, want := annex.Writes(), []string{"grouped_light/gl-v2kitchen=off"}; !slices.Equal(got, want) {
		t.Errorf("annex writes = %v, want %v", got, want)
	}

	if _, _, err := h.run(t, "off", "Strip", "--no-dry-run"); err != nil {
		t.Fatal(err)
	}
	if w := annex.Writes(); w[len(w)-1] != "light/l-v2strip=off" {
		t.Errorf("a light on annex should be switched through annex, got %v", w)
	}
}

func TestSweepIsSkippedWhenABridgeIsDown(t *testing.T) {
	h, annex := newTwoBridgeHarness(t)
	annex.server.Close()

	_, _, err := h.run(t, "sweep", "--no-dry-run")
	if err == nil || !strings.Contains(err.Error(), "skipping sweep") || !strings.Contains(err.Error(), "bridge annex") {
		t.Fatalf("err = %v, want the sweep skipped because annex is down", err)
	}
	if w := h.bridge.Writes(); len(w) != 0 {
		t.Errorf("nothing should change with a bridge missing, got %v", w)
	}
	if _, err := os.Stat(h.statePath); err == nil {
		t.Error("a skipped sweep should not record state")
	}

	// Listing carries on with the bridge that answered.
	out, errOut, err := h.run(t, "ls", "Kitchen")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "warning: bridge annex") || !hasRow(out, "Kitchen room Kitchen 1 light main on 70%") {
		t.Errorf("ls should warn and show the main lights:\nstdout:\n%s\nstderr:\n%s", out, errOut)
	}
}

func TestProbeNeedsABridgeChoiceWithSeveral(t *testing.T) {
	h, _ := newTwoBridgeHarness(t)
	if _, _, err := h.run(t, "probe"); err == nil || !strings.Contains(err.Error(), "--bridge") {
		t.Fatalf("err = %v, want a request to choose with --bridge", err)
	}
	out, _, err := h.run(t, "probe", "--bridge", "annex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "motion") {
		t.Errorf("probe of annex should list its motion sensors:\n%s", out)
	}
}

func TestStatusListsEveryBridge(t *testing.T) {
	h, _ := newTwoBridgeHarness(t)
	out, _, err := h.run(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bridge main", "bridge annex", "room aliases", "Yard -> Porch"} {
		if !strings.Contains(out, want) {
			t.Errorf("status is missing %q:\n%s", want, out)
		}
	}
}
