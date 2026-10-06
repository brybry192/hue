package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/brybry192/hue/internal/hue"
)

func TestListShowsRoomsDevicesAndSensors(t *testing.T) {
	h := newHarness(t, "")
	out, _, err := h.run(t, "ls")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"ROOM GROUP DEVICE TYPE STATE BRIGHT DETAIL",
		"Kitchen room Kitchen 1 light on 70%",
		"Kitchen room Kitchen 2 light off -",
		"Kitchen room Kitchen Dimmer switch - - Hue dimmer switch (4 buttons)",
		"Hallway room Hall Sensor sensor - - motion 30m ago",
		"Outside zone Porch Left light on 70%",
		"5 rooms/zones, 18 lights, 10 on",
	} {
		if !hasRow(out, want) {
			t.Errorf("ls output is missing a row %q\n--- output ---\n%s", want, out)
		}
	}
}

// hasRow reports whether any line of out, with its column padding collapsed to
// single spaces, contains want. It keeps table assertions independent of
// column widths.
func hasRow(out, want string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(strings.Join(strings.Fields(line), " "), want) {
			return true
		}
	}
	return false
}

func TestListOnlyOnFilter(t *testing.T) {
	h := newHarness(t, "")
	out, _, err := h.run(t, "ls", "--on")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Kitchen 1") {
		t.Error("expected the light that is on to be listed")
	}
	if strings.Contains(out, "Kitchen 2") {
		t.Error("lights that are off should be hidden by --on")
	}
}

func TestListSingleRoomAndUnknownRoom(t *testing.T) {
	h := newHarness(t, "")

	out, _, err := h.run(t, "ls", "Kitchen")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Kitchen") || strings.Contains(out, "Hallway") {
		t.Errorf("expected only Kitchen:\n%s", out)
	}

	if _, _, err := h.run(t, "ls", "Dungeon"); err == nil {
		t.Error("expected an error for an unknown room")
	}
}

func TestListJSON(t *testing.T) {
	h := newHarness(t, "")
	out, _, err := h.run(t, "ls", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var groups []hue.GroupView
	if err := json.Unmarshal([]byte(out), &groups); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(groups) != 5 { // 4 rooms + 1 zone
		t.Errorf("got %d groups, want 5", len(groups))
	}
}

func TestSweepIsDryRunByDefault(t *testing.T) {
	h := newHarness(t, "")

	out, _, err := h.run(t, "sweep")
	if err != nil {
		t.Fatal(err)
	}
	if w := h.bridge.Writes(); len(w) != 0 {
		t.Fatalf("a default sweep must not change anything, got writes %v", w)
	}
	if !strings.Contains(out, "dry-run off ") {
		t.Errorf("expected dry-run lines:\n%s", out)
	}
	if !strings.Contains(out, "--no-dry-run") {
		t.Errorf("expected a hint about --no-dry-run:\n%s", out)
	}
}

func TestSweepAppliesWithNoDryRun(t *testing.T) {
	h := newHarness(t, "")

	out, _, err := h.run(t, "sweep", "--no-dry-run")
	if err != nil {
		t.Fatal(err)
	}

	writes := h.bridge.Writes()
	got := make(map[string]bool, len(writes))
	for _, w := range writes {
		got[w] = true
	}

	// Kitchen: the lone light on among five is an outlier.
	if !got["light/l-kitchen1=off"] {
		t.Errorf("expected the Kitchen straggler to be switched off, got %v", writes)
	}
	// Hallway: no motion for 30m, so all three go off.
	for _, id := range []string{"l-hall1", "l-hall2", "l-hall3"} {
		if !got["light/"+id+"=off"] {
			t.Errorf("expected %s to be switched off, got %v", id, writes)
		}
	}
	// Lounge: motion a minute ago, so it must be left alone.
	for _, id := range []string{"l-lounge1", "l-lounge2", "l-lounge3", "l-lounge4"} {
		if got["light/"+id+"=off"] {
			t.Errorf("%s should not have been touched (recent motion), got %v", id, writes)
		}
	}
	if !strings.Contains(out, "Z off group=") || strings.Contains(out, "dry-run") {
		t.Errorf("expected applied lines without the dry-run marker:\n%s", out)
	}
}

func TestSweepRulesAreReportedWithReasons(t *testing.T) {
	h := newHarness(t, "")
	out, _, err := h.run(t, "sweep")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "outlier") {
		t.Errorf("expected the outlier rule to be named:\n%s", out)
	}
	if !strings.Contains(out, "motion-idle") {
		t.Errorf("expected the motion rule to be named:\n%s", out)
	}
	if !strings.Contains(out, "no motion for 30m") {
		t.Errorf("expected the motion reason:\n%s", out)
	}
}

func TestSweepOutputIsLogLines(t *testing.T) {
	h := newHarness(t, "")

	out, _, err := h.run(t, "sweep", "--rooms", "Kitchen")
	if err != nil {
		t.Fatal(err)
	}
	want := `2026-10-01T12:00:00Z dry-run off group="Kitchen" kind=room rule=outlier light="Kitchen 1" reason="1 of 5 lights on (20% of the group)"
2026-10-01T12:00:00Z dry-run swept lights=1 groups=1 hint="nothing changed; add --no-dry-run to apply"
`
	if out != want {
		t.Errorf("sweep output:\n%s\nwant:\n%s", out, want)
	}

	// A real run drops the dry-run marker, and a run with nothing to do is
	// a single line.
	out, _, err = h.run(t, "sweep", "--no-dry-run", "--rooms", "Kitchen")
	if err != nil {
		t.Fatal(err)
	}
	want = `2026-10-01T12:00:00Z off group="Kitchen" kind=room rule=outlier light="Kitchen 1" reason="1 of 5 lights on (20% of the group)"
2026-10-01T12:00:00Z swept lights=1 groups=1
`
	if out != want {
		t.Errorf("applied output:\n%s\nwant:\n%s", out, want)
	}

	out, _, err = h.run(t, "sweep", "--rooms", "Lounge")
	if err != nil {
		t.Fatal(err)
	}
	if want := "2026-10-01T12:00:00Z dry-run swept lights=0 groups=0 hint=\"add -v to see why each group was left alone\"\n"; out != want {
		t.Errorf("quiet output = %q, want %q", out, want)
	}
}

func TestSweepIgnoresUnreachableLights(t *testing.T) {
	// Kitchen 1 is the Kitchen straggler, but its power has been cut. The
	// bridge still reports it as on.
	h := newHarness(t, "")
	h.bridge.cutPower("kitchen1")

	out, _, err := h.run(t, "sweep", "--no-dry-run", "-v", "--rooms", "Kitchen")
	if err != nil {
		t.Fatal(err)
	}
	if w := h.bridge.Writes(); len(w) != 0 {
		t.Fatalf("an unreachable light should not be switched, got %v", w)
	}
	if !strings.Contains(out, "Kitchen 1 unreachable, ignored") {
		t.Errorf("-v should say why Kitchen 1 was ignored:\n%s", out)
	}

	out, _, err = h.run(t, "ls", "Kitchen")
	if err != nil {
		t.Fatal(err)
	}
	if row := "Kitchen room Kitchen 1 light ? - unreachable (no power?)"; !hasRow(out, row) {
		t.Errorf("ls should show Kitchen 1 as unreachable:\n%s", out)
	}
}

func TestSweepRoomScope(t *testing.T) {
	h := newHarness(t, `{"rooms": ["Kitchen"]}`)
	if _, _, err := h.run(t, "sweep", "--no-dry-run"); err != nil {
		t.Fatal(err)
	}
	for _, w := range h.bridge.Writes() {
		if !strings.Contains(w, "kitchen") {
			t.Errorf("only Kitchen was configured, but %q was changed", w)
		}
	}
}

func TestSweepRoomsFlagOverridesConfig(t *testing.T) {
	h := newHarness(t, `{"rooms": ["Kitchen"]}`)
	if _, _, err := h.run(t, "sweep", "--no-dry-run", "--rooms", "Hallway"); err != nil {
		t.Fatal(err)
	}
	writes := h.bridge.Writes()
	if len(writes) == 0 {
		t.Fatal("expected Hallway to be swept")
	}
	for _, w := range writes {
		if !strings.Contains(w, "hall") {
			t.Errorf("--rooms should limit the sweep to Hallway, but %q was changed", w)
		}
	}
}

func TestSweepUnknownConfiguredRoomWarns(t *testing.T) {
	h := newHarness(t, `{"rooms": ["Kitchen", "Dungeon"]}`)
	_, errOut, err := h.run(t, "sweep")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "Dungeon") {
		t.Errorf("expected a warning about Dungeon on stderr, got %q", errOut)
	}
}

func TestSweepIdleFlagOverridesThreshold(t *testing.T) {
	// A 45 minute threshold is longer than the Hallway's 30 minute idle, so
	// the motion rule should no longer fire there.
	h := newHarness(t, "")
	if _, _, err := h.run(t, "sweep", "--no-dry-run", "--idle", "45m"); err != nil {
		t.Fatal(err)
	}
	for _, w := range h.bridge.Writes() {
		if strings.Contains(w, "hall") {
			t.Errorf("a 45m threshold should spare the Hallway, but %q was changed", w)
		}
	}
}

func TestSweepVerboseExplainsSkips(t *testing.T) {
	h := newHarness(t, "")
	out, _, err := h.run(t, "sweep", "-v")
	if err != nil {
		t.Fatal(err)
	}
	// The Lounge is skipped because of recent motion; -v should say so.
	if !strings.Contains(out, "Lounge") {
		t.Errorf("expected the Lounge to be explained:\n%s", out)
	}
	if !strings.Contains(out, "probably there") {
		t.Errorf("expected the presence explanation:\n%s", out)
	}
}

func TestSweepJSON(t *testing.T) {
	h := newHarness(t, "")
	out, _, err := h.run(t, "sweep", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		DryRun bool `json:"dry_run"`
		Plan   struct {
			Actions []struct {
				Group  string `json:"group"`
				Rule   string `json:"rule"`
				Reason string `json:"reason"`
				Lights []struct {
					Name string `json:"name"`
				} `json:"lights"`
			} `json:"actions"`
		} `json:"plan"`
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if !got.DryRun {
		t.Error("dry_run should be true by default")
	}
	// Hallway (motion), Kitchen (outlier) and the Outside zone (outlier).
	// The Porch room holds the same lights as the zone, so it is deduped.
	if len(got.Plan.Actions) != 3 {
		t.Fatalf("got %d actions, want 3: %+v", len(got.Plan.Actions), got.Plan.Actions)
	}
	for _, a := range got.Plan.Actions {
		if a.Reason == "" || a.Rule == "" {
			t.Errorf("action %+v is missing its rule or reason", a)
		}
	}
	if len(got.Results) != 5 { // 1 Kitchen + 3 Hallway + 1 Outside
		t.Errorf("got %d results, want 5", len(got.Results))
	}
}

func TestSweepReportsWriteFailures(t *testing.T) {
	h := newHarness(t, "")
	h.bridge.failWrites = true

	out, _, err := h.run(t, "sweep", "--no-dry-run")
	if err == nil {
		t.Fatal("expected an error when the bridge rejects the writes")
	}
	if !strings.Contains(out, " failed group=") {
		t.Errorf("expected the failure to be visible:\n%s", out)
	}
	if !strings.Contains(err.Error(), "could not be switched off") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOffGroupIsDryRunByDefault(t *testing.T) {
	h := newHarness(t, "")

	out, _, err := h.run(t, "off", "Kitchen")
	if err != nil {
		t.Fatal(err)
	}
	if w := h.bridge.Writes(); len(w) != 0 {
		t.Fatalf("off should default to a dry run, got %v", w)
	}
	if !strings.Contains(out, "would turn off") {
		t.Errorf("expected dry-run wording:\n%s", out)
	}
}

func TestOffGroupUsesGroupedLight(t *testing.T) {
	h := newHarness(t, "")
	if _, _, err := h.run(t, "off", "Kitchen", "--no-dry-run"); err != nil {
		t.Fatal(err)
	}
	writes := h.bridge.Writes()
	if len(writes) != 1 || writes[0] != "grouped_light/gl-kitchen=off" {
		t.Errorf("expected one grouped-light write, got %v", writes)
	}
}

func TestOnGroup(t *testing.T) {
	h := newHarness(t, "")
	if _, _, err := h.run(t, "on", "Hallway", "--no-dry-run"); err != nil {
		t.Fatal(err)
	}
	writes := h.bridge.Writes()
	if len(writes) != 1 || writes[0] != "grouped_light/gl-hall=on" {
		t.Errorf("expected one grouped-light write, got %v", writes)
	}
}

func TestOffZone(t *testing.T) {
	h := newHarness(t, "")
	if _, _, err := h.run(t, "off", "Outside", "--no-dry-run"); err != nil {
		t.Fatal(err)
	}
	writes := h.bridge.Writes()
	if len(writes) != 1 || writes[0] != "grouped_light/gl-outside=off" {
		t.Errorf("expected the zone's grouped light, got %v", writes)
	}
}

func TestOffSingleLightByName(t *testing.T) {
	h := newHarness(t, "")
	if _, _, err := h.run(t, "off", "Kitchen 1", "--no-dry-run"); err != nil {
		t.Fatal(err)
	}
	writes := h.bridge.Writes()
	if len(writes) != 1 || writes[0] != "light/l-kitchen1=off" {
		t.Errorf("expected a single light write, got %v", writes)
	}
}

func TestOffUnknownTarget(t *testing.T) {
	h := newHarness(t, "")
	_, _, err := h.run(t, "off", "Nowhere", "--no-dry-run")
	if err == nil {
		t.Fatal("expected an error for an unknown target")
	}
	if !strings.Contains(err.Error(), "hue ls") {
		t.Errorf("the error should point at 'hue ls', got %v", err)
	}
}

func TestOffWithNoTargetIsAUsageError(t *testing.T) {
	h := newHarness(t, "")
	if _, _, err := h.run(t, "off"); !errors.Is(err, ErrUsage) {
		t.Errorf("expected a usage error, got %v", err)
	}
}

func TestStatus(t *testing.T) {
	h := newHarness(t, `{"rooms": ["Kitchen"]}`)
	out, _, err := h.run(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"bridge", "001788FFFE000001", "rooms", "lights", "motion rule", "outlier rule",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q\n%s", want, out)
		}
	}
	if !strings.Contains(out, "1 configured") {
		t.Errorf("expected the configured sweep scope:\n%s", out)
	}
}

func TestProbeInventoriesResourceTypes(t *testing.T) {
	h := newHarness(t, "")
	out, _, err := h.run(t, "probe")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"TYPE", "device", "light", "motion", "resources across"} {
		if !strings.Contains(out, want) {
			t.Errorf("probe output is missing %q\n%s", want, out)
		}
	}
}

func TestProbeDumpsOneType(t *testing.T) {
	h := newHarness(t, "")
	out, _, err := h.run(t, "probe", "light")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("probe <type> should emit JSON: %v\n%s", err, out)
	}
	if _, ok := body["data"]; !ok {
		t.Errorf("expected a data array:\n%s", out)
	}
}

func TestConfigShowRedactsTheKey(t *testing.T) {
	h := newHarness(t, "")
	out, _, err := h.run(t, "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "test-key") {
		t.Errorf("the application key must not be printed in full:\n%s", out)
	}
	if !strings.Contains(out, "....") {
		t.Errorf("expected a redacted key:\n%s", out)
	}
}

func TestVersionAndHelp(t *testing.T) {
	h := newHarness(t, "")

	out, _, err := h.run(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hue") {
		t.Errorf("version output = %q", out)
	}

	var sb, eb strings.Builder
	app := &App{Out: &sb, Err: &eb}
	if err := app.Run([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sweep", "ls", "auth", "HUE_CONFIG"} {
		if !strings.Contains(eb.String(), want) {
			t.Errorf("help is missing %q", want)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	var sb, eb strings.Builder
	app := &App{Out: &sb, Err: &eb}
	err := app.Run([]string{"frobnicate"})
	if !errors.Is(err, ErrUsage) {
		t.Errorf("expected a usage error, got %v", err)
	}
	if !strings.Contains(eb.String(), "unknown command") {
		t.Errorf("expected an unknown-command message, got %q", eb.String())
	}
}

func TestNoArgsShowsUsage(t *testing.T) {
	var sb, eb strings.Builder
	app := &App{Out: &sb, Err: &eb}
	if err := app.Run(nil); !errors.Is(err, ErrUsage) {
		t.Errorf("expected a usage error, got %v", err)
	}
}

func TestMissingBridgeConfigIsExplained(t *testing.T) {
	h := newHarness(t, "")
	// An empty config has no host or key.
	if err := writeFile(h.configPath, `{}`); err != nil {
		t.Fatal(err)
	}
	_, _, err := h.run(t, "ls")
	if err == nil {
		t.Fatal("expected an error without a configured bridge")
	}
	if !strings.Contains(err.Error(), "hue auth") {
		t.Errorf("the error should point at 'hue auth', got %v", err)
	}
}

func TestListAliasesToLs(t *testing.T) {
	h := newHarness(t, "")
	out, _, err := h.run(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Kitchen") {
		t.Errorf("'list' should behave like 'ls':\n%s", out)
	}
}

func TestWriteTablePadsByCharacter(t *testing.T) {
	// A curly apostrophe is three bytes but one column wide.
	var b strings.Builder
	writeTable(&b, []string{"ROOM", "STATE"}, [][]string{
		{"Café", "on"},
		{"Kitchen", "off"},
	})
	// column is the character offset of the second column in a line.
	column := func(line string) int {
		return len([]rune(line[:strings.LastIndex(line, " ")+1]))
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	want := column(lines[0])
	for _, line := range lines[2:] {
		if got := column(line); got != want {
			t.Errorf("second column of %q starts at %d, want %d", line, got, want)
		}
	}
}
