package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brybry192/hue/internal/hue"
)

// fakeBridge serves just enough of the CLIP v2 API to drive the CLI, and
// records every state change it is asked to make.
type fakeBridge struct {
	t      *testing.T
	server *httptest.Server

	mu      sync.Mutex
	rooms   []hue.Group
	zones   []hue.Group
	devices []hue.Device
	lights  []hue.Light
	motions []hue.Motion
	conns   []hue.ZigbeeConnectivity

	// writes records PUTs as "light/<id>=off" or "grouped_light/<id>=on".
	writes []string
	// failWrites makes every PUT return an error, to exercise the sad path.
	failWrites bool
}

func (f *fakeBridge) URL() string { return strings.TrimPrefix(f.server.URL, "https://") }

func (f *fakeBridge) Writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

func (f *fakeBridge) serveList(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{}, "data": data})
}

func (f *fakeBridge) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/clip/v2/resource/room", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.serveList(w, f.rooms)
	})
	mux.HandleFunc("/clip/v2/resource/zone", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.serveList(w, f.zones)
	})
	mux.HandleFunc("/clip/v2/resource/device", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.serveList(w, f.devices)
	})
	mux.HandleFunc("/clip/v2/resource/motion", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.serveList(w, f.motions)
	})
	mux.HandleFunc("/clip/v2/resource/zigbee_connectivity", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.serveList(w, f.conns)
	})
	mux.HandleFunc("/clip/v2/resource/bridge", func(w http.ResponseWriter, r *http.Request) {
		f.serveList(w, []hue.BridgeInfo{{ID: "b1", BridgeID: "001788FFFE000001"}})
	})

	// Lights are both listed and individually updated.
	mux.HandleFunc("/clip/v2/resource/light", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.serveList(w, f.lights)
	})
	mux.HandleFunc("/clip/v2/resource/light/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/clip/v2/resource/light/")
		f.handleWrite(w, r, "light", id)
	})
	mux.HandleFunc("/clip/v2/resource/grouped_light/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/clip/v2/resource/grouped_light/")
		f.handleWrite(w, r, "grouped_light", id)
	})

	// The whole-bridge inventory used by `hue probe`.
	mux.HandleFunc("/clip/v2/resource", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var all []hue.RawResource
		for _, d := range f.devices {
			all = append(all, hue.RawResource{ID: d.ID, Type: "device", Metadata: d.Metadata})
		}
		for _, l := range f.lights {
			all = append(all, hue.RawResource{ID: l.ID, Type: "light", Metadata: l.Metadata})
		}
		for _, m := range f.motions {
			all = append(all, hue.RawResource{ID: m.ID, Type: "motion"})
		}
		f.serveList(w, all)
	})

	return mux
}

func (f *fakeBridge) handleWrite(w http.ResponseWriter, r *http.Request, rtype, id string) {
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		On *hue.OnState `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.On == nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failWrites {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]string{{"description": "device unreachable"}},
			"data":   []any{},
		})
		return
	}

	state := "off"
	if body.On.On {
		state = "on"
	}
	f.writes = append(f.writes, fmt.Sprintf("%s/%s=%s", rtype, id, state))

	// Reflect the change so a second snapshot sees it.
	for i := range f.lights {
		if f.lights[i].ID == id {
			f.lights[i].On.On = body.On.On
		}
	}
	f.serveList(w, []any{})
}

// cutPower marks a device unreachable, the way the bridge reports a light
// whose wall switch is off. Its last on/off state is left as it was.
func (f *fakeBridge) cutPower(devID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns = append(f.conns, hue.ZigbeeConnectivity{
		ID: "zc-" + devID, Owner: hue.ResourceRef{RID: devID, RType: "device"},
		Status: "connectivity_issue",
	})
}

// newFakeBridge builds a bridge with a deliberately varied topology:
//
//   - Kitchen: 5 lights, 1 on, no sensor        -> outlier rule
//   - Hallway: 3 lights, all on, idle sensor    -> motion rule
//   - Lounge:  4 lights, all on, recent motion  -> left alone
//   - Outside: a zone over the porch lights
func newFakeBridge(t *testing.T, now time.Time) *fakeBridge {
	t.Helper()
	f := &fakeBridge{t: t}

	addLight := func(devID, name string, on bool) {
		f.devices = append(f.devices, hue.Device{
			ID: devID, Type: "device", Metadata: hue.Metadata{Name: name},
			ProductData: hue.ProductData{ModelID: "LCA001", ProductName: "Hue color lamp"},
			Services:    []hue.ResourceRef{{RID: "l-" + devID, RType: "light"}},
		})
		f.lights = append(f.lights, hue.Light{
			ID: "l-" + devID, Owner: hue.ResourceRef{RID: devID, RType: "device"},
			Metadata: hue.Metadata{Name: name}, On: hue.OnState{On: on},
			Dimming: &hue.Dimming{Brightness: 70},
		})
	}
	addSensor := func(devID, name string, lastMotion time.Time, motion bool) {
		f.devices = append(f.devices, hue.Device{
			ID: devID, Type: "device", Metadata: hue.Metadata{Name: name},
			ProductData: hue.ProductData{ModelID: "SML001", ProductName: "Hue motion sensor"},
			Services:    []hue.ResourceRef{{RID: "m-" + devID, RType: "motion"}},
		})
		f.motions = append(f.motions, hue.Motion{
			ID: "m-" + devID, Owner: hue.ResourceRef{RID: devID, RType: "device"}, Enabled: true,
			Motion: hue.MotionState{
				Motion:       motion,
				MotionReport: &hue.MotionReport{Changed: lastMotion, Motion: motion},
			},
		})
	}
	addDimmer := func(devID, name string) {
		d := hue.Device{
			ID: devID, Type: "device", Metadata: hue.Metadata{Name: name},
			ProductData: hue.ProductData{ModelID: "RWL022", ProductName: "Hue dimmer switch"},
		}
		for i := range 4 {
			d.Services = append(d.Services, hue.ResourceRef{
				RID: fmt.Sprintf("b%d-%s", i, devID), RType: "button",
			})
		}
		f.devices = append(f.devices, d)
	}

	// Kitchen: one straggler among five.
	for i := range 5 {
		addLight(fmt.Sprintf("kitchen%d", i+1), fmt.Sprintf("Kitchen %d", i+1), i == 0)
	}
	addDimmer("kitchendim", "Kitchen Dimmer")

	// Hallway: everything on, but nobody has moved for half an hour.
	for i := range 3 {
		addLight(fmt.Sprintf("hall%d", i+1), fmt.Sprintf("Hall %d", i+1), true)
	}
	addSensor("hallsensor", "Hall Sensor", now.Add(-30*time.Minute), false)

	// Lounge: everything on and someone is clearly in there.
	for i := range 4 {
		addLight(fmt.Sprintf("lounge%d", i+1), fmt.Sprintf("Lounge %d", i+1), true)
	}
	addSensor("loungesensor", "Lounge Sensor", now.Add(-1*time.Minute), false)

	// Porch lights, also gathered into an Outside zone.
	addLight("porch1", "Porch Left", true)
	addLight("porch2", "Porch Right", false)
	addLight("porch3", "Porch Step", false)

	devRefs := func(ids ...string) []hue.ResourceRef {
		var out []hue.ResourceRef
		for _, id := range ids {
			out = append(out, hue.ResourceRef{RID: id, RType: "device"})
		}
		return out
	}

	f.rooms = []hue.Group{
		{
			ID: "r-kitchen", Type: "room", Metadata: hue.Metadata{Name: "Kitchen"},
			Children: devRefs("kitchen1", "kitchen2", "kitchen3", "kitchen4", "kitchen5", "kitchendim"),
			Services: []hue.ResourceRef{{RID: "gl-kitchen", RType: "grouped_light"}},
		},
		{
			ID: "r-hall", Type: "room", Metadata: hue.Metadata{Name: "Hallway"},
			Children: devRefs("hall1", "hall2", "hall3", "hallsensor"),
			Services: []hue.ResourceRef{{RID: "gl-hall", RType: "grouped_light"}},
		},
		{
			ID: "r-lounge", Type: "room", Metadata: hue.Metadata{Name: "Lounge"},
			Children: devRefs("lounge1", "lounge2", "lounge3", "lounge4", "loungesensor"),
			Services: []hue.ResourceRef{{RID: "gl-lounge", RType: "grouped_light"}},
		},
		{
			ID: "r-porch", Type: "room", Metadata: hue.Metadata{Name: "Porch"},
			Children: devRefs("porch1", "porch2", "porch3"),
			Services: []hue.ResourceRef{{RID: "gl-porch", RType: "grouped_light"}},
		},
	}
	f.zones = []hue.Group{{
		ID: "z-outside", Type: "zone", Metadata: hue.Metadata{Name: "Outside"},
		Children: []hue.ResourceRef{
			{RID: "l-porch1", RType: "light"},
			{RID: "l-porch2", RType: "light"},
			{RID: "l-porch3", RType: "light"},
		},
		Services: []hue.ResourceRef{{RID: "gl-outside", RType: "grouped_light"}},
	}}

	f.server = httptest.NewTLSServer(f.handler())
	t.Cleanup(f.server.Close)
	return f
}

// harness wires the CLI up to a fake bridge with its own config and state.
type harness struct {
	bridge     *fakeBridge
	configPath string
	now        time.Time
}

func newHarness(t *testing.T, sweepCfg string) *harness {
	t.Helper()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	bridge := newFakeBridge(t, now)

	dir := t.TempDir()
	h := &harness{
		bridge:     bridge,
		configPath: filepath.Join(dir, "config.json"),
		now:        now,
	}

	if sweepCfg == "" {
		sweepCfg = `{}`
	}
	// insecure is required because httptest mints its own certificate.
	cfg := fmt.Sprintf(`{
	  "bridge": {"host": %q, "app_key": "test-key", "insecure": true},
	  "sweep": %s
	}`, bridge.URL(), sweepCfg)
	if err := os.WriteFile(h.configPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	// Keep the developer's real config and state out of the test.
	t.Setenv("HUE_CONFIG", h.configPath)
	t.Setenv("HUE_BRIDGE_HOST", "")
	t.Setenv("HUE_APP_KEY", "")
	return h
}

// run executes a command, returning stdout, stderr and the error.
func (h *harness) run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut strings.Builder
	app := &App{Out: &out, Err: &errOut, Now: func() time.Time { return h.now }}

	full := append([]string(nil), args...)
	full = append(full, "--config", h.configPath)
	err := app.Run(full)
	return out.String(), errOut.String(), err
}

// readFileIfExists is a small helper so tests can assert a file was not made.
func readFileIfExists(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// writeFile replaces a file's contents during a test.
func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}
