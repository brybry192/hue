// Package sweep decides which lights should be switched off.
//
// The decision is a pure function of a bridge snapshot, the configuration and
// the current time, so every rule below is directly testable without a bridge.
package sweep

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brybry192/hue/internal/config"
	"github.com/brybry192/hue/internal/hue"
)

// Rule names why a light was selected.
type Rule string

const (
	// RuleOutlier fired because only a small minority of a group was on.
	RuleOutlier Rule = "outlier"
	// RuleMotionIdle fired because the group's motion sensors have seen
	// nothing for longer than the configured threshold.
	RuleMotionIdle Rule = "motion-idle"
)

// Target is one light the sweep intends to switch off.
type Target struct {
	LightID string `json:"light_id"`
	Name    string `json:"name"`
	// OnFor is how long the light has been observed on. Valid only when
	// HasOnFor is true.
	OnFor    time.Duration `json:"on_for,omitempty"`
	HasOnFor bool          `json:"-"`
}

// Action is a group's worth of lights to switch off, and why.
type Action struct {
	Group     string   `json:"group"`
	GroupKind string   `json:"group_kind"`
	GroupID   string   `json:"group_id"`
	Rule      Rule     `json:"rule"`
	Reason    string   `json:"reason"`
	OnCount   int      `json:"on_count"`
	Total     int      `json:"total"`
	Lights    []Target `json:"lights"`
}

// Note explains why a group was examined but left alone. Notes are
// informational; they are never failures.
type Note struct {
	Group string `json:"group"`
	Text  string `json:"text"`
}

// Plan is the full set of decisions for one sweep.
type Plan struct {
	Now     time.Time `json:"now"`
	Actions []Action  `json:"actions"`
	Notes   []Note    `json:"notes"`
	// Warnings are problems with the request itself, such as a configured
	// room that does not exist on the bridge.
	Warnings []string `json:"warnings,omitempty"`
}

// LightCount totals the lights the plan would switch off.
func (p Plan) LightCount() int {
	n := 0
	for _, a := range p.Actions {
		n += len(a.Lights)
	}
	return n
}

// Inputs is everything Build needs.
type Inputs struct {
	Groups []hue.GroupView
	Cfg    config.Sweep
	// OnSince reports when a light was first observed on. A nil function
	// means no history is available, which with a non-zero MinOnDuration
	// holds every light back for one run.
	OnSince func(lightID string) (time.Time, bool)
	Now     time.Time
	// IgnoreMinOn skips the MinOnDuration grace period.
	IgnoreMinOn bool
	// RoomsOverride replaces Cfg.Rooms when non-nil.
	RoomsOverride []string
}

// Build produces the plan. It performs no I/O.
func Build(in Inputs) Plan {
	plan := Plan{Now: in.Now}

	wanted := in.Cfg.Rooms
	if in.RoomsOverride != nil {
		wanted = in.RoomsOverride
	}
	groups, warnings := selectGroups(in.Groups, wanted)
	plan.Warnings = warnings

	// A light can belong to both a room and a zone. Switching it off once is
	// enough, and reporting it once keeps the output honest.
	claimed := make(map[string]bool)

	for _, g := range groups {
		action, notes := evaluate(g, in, claimed)
		plan.Notes = append(plan.Notes, notes...)
		if action == nil {
			continue
		}
		for _, t := range action.Lights {
			claimed[t.LightID] = true
		}
		plan.Actions = append(plan.Actions, *action)
	}
	return plan
}

// selectGroups resolves the configured names to groups, preserving the order
// the groups came in. An empty selection means every group.
func selectGroups(all []hue.GroupView, wanted []string) ([]hue.GroupView, []string) {
	if len(wanted) == 0 {
		return all, nil
	}
	matched := make(map[string]bool, len(wanted))
	var out []hue.GroupView
	for _, g := range all {
		name := strings.ToLower(strings.TrimSpace(g.Name))
		for _, w := range wanted {
			lw := strings.ToLower(strings.TrimSpace(w))
			if lw == "" {
				continue
			}
			if name == lw {
				matched[lw] = true
				out = append(out, g)
				break
			}
		}
	}
	var warnings []string
	for _, w := range wanted {
		lw := strings.ToLower(strings.TrimSpace(w))
		if lw == "" {
			continue
		}
		if !matched[lw] {
			warnings = append(warnings, fmt.Sprintf("configured room %q does not match any room or zone on the bridge", w))
		}
	}
	return out, warnings
}

// evaluate applies the rules to a single group.
func evaluate(g hue.GroupView, in Inputs, claimed map[string]bool) (*Action, []Note) {
	cfg := in.Cfg
	var notes []Note
	note := func(format string, args ...any) {
		notes = append(notes, Note{Group: g.Name, Text: fmt.Sprintf(format, args...)})
	}

	eligible, unreachable := eligibleLights(g, cfg.IncludePlugs)
	if len(unreachable) > 0 {
		note("%s unreachable, ignored", strings.Join(unreachable, ", "))
	}
	total := len(eligible)
	if total == 0 {
		return nil, notes
	}

	var on []hue.LightView
	for _, l := range eligible {
		if l.On {
			on = append(on, l)
		}
	}
	if len(on) == 0 {
		return nil, notes
	}

	// Decide which rule applies. Motion evidence is stronger than a group
	// shape guess, so it is checked first.
	rule, reason, ok := pickRule(g, cfg, on, total, in.Now)
	if !ok {
		note("%d/%d on, no rule applies (%s)", len(on), total, reason)
		return nil, notes
	}

	candidates, excluded := filterExcluded(on, cfg.ExcludeLights)
	if len(excluded) > 0 {
		note("%s excluded by config", strings.Join(excluded, ", "))
	}
	if len(candidates) == 0 {
		note("%d/%d on but every on light is excluded", len(on), total)
		return nil, notes
	}

	targets, held := applyGracePeriod(candidates, in)
	for _, h := range held {
		note("%s", h)
	}

	// Drop lights another group already claimed.
	var final []Target
	for _, t := range targets {
		if claimed[t.LightID] {
			note("%s already handled by another group", t.Name)
			continue
		}
		final = append(final, t)
	}
	if len(final) == 0 {
		return nil, notes
	}

	return &Action{
		Group:     g.Name,
		GroupKind: g.Kind,
		GroupID:   g.ID,
		Rule:      rule,
		Reason:    reason,
		OnCount:   len(on),
		Total:     total,
		Lights:    final,
	}, notes
}

// pickRule returns the rule that justifies switching lights off in this group.
// When no rule applies, the returned string explains why, for -v output.
//
// A group with working motion sensors is decided by motion alone. Motion is
// direct evidence about whether anyone is there, so when it is recent it vetoes
// the outlier rule rather than falling through to it: one light on in a room
// someone is standing in is a choice, not a straggler.
func pickRule(g hue.GroupView, cfg config.Sweep, on []hue.LightView, total int, now time.Time) (Rule, string, bool) {
	var why []string

	if cfg.Motion.IsEnabled() {
		if last, ok := g.LastMotion(now); ok {
			idle := now.Sub(last)
			threshold := cfg.Motion.IdleThreshold.Duration()
			if idle >= threshold {
				return RuleMotionIdle, fmt.Sprintf("no motion for %s (threshold %s)",
					hue.ShortDuration(idle), hue.ShortDuration(threshold)), true
			}
			return "", fmt.Sprintf("motion %s ago, under the %s threshold, so someone is probably there",
				hue.ShortDuration(idle), hue.ShortDuration(threshold)), false
		}
		why = append(why, "no usable motion sensor")
	} else {
		why = append(why, "motion rule disabled")
	}

	if cfg.Outlier.IsEnabled() {
		o := cfg.Outlier
		fraction := float64(len(on)) / float64(total)
		switch {
		case total < o.MinGroupSize:
			why = append(why, fmt.Sprintf("group of %d is below min_group_size %d", total, o.MinGroupSize))
		case len(on) > o.MaxOnCount:
			why = append(why, fmt.Sprintf("%d on exceeds max_on_count %d", len(on), o.MaxOnCount))
		case fraction > o.MaxOnFraction:
			why = append(why, fmt.Sprintf("%.0f%% of the group is on, over max_on_fraction %.0f%%",
				fraction*100, o.MaxOnFraction*100))
		default:
			return RuleOutlier, fmt.Sprintf("%d of %d lights on (%.0f%% of the group)",
				len(on), total, fraction*100), true
		}
	} else {
		why = append(why, "outlier rule disabled")
	}

	return "", strings.Join(why, "; "), false
}

// eligibleLights returns the lights in a group the sweep is allowed to touch,
// and the names of lights left out because the bridge cannot reach them.
//
// Unreachable lights are left out of the group entirely, not just spared: their
// on/off state is stale, so counting them would distort the outlier fraction,
// and a light with its power cut cannot be switched anyway.
func eligibleLights(g hue.GroupView, includePlugs bool) (out []hue.LightView, unreachable []string) {
	out = make([]hue.LightView, 0, len(g.Lights))
	for _, l := range g.Lights {
		if l.Unreachable {
			if l.Kind == hue.KindLight || includePlugs {
				unreachable = append(unreachable, l.Name)
			}
			continue
		}
		switch l.Kind {
		case hue.KindLight:
			out = append(out, l)
		case hue.KindPlug:
			if includePlugs {
				out = append(out, l)
			}
		}
	}
	return out, unreachable
}

// filterExcluded splits lights by the configured exclusion patterns, which are
// matched case-insensitively against the light name and its device name and
// may use shell-style wildcards.
func filterExcluded(lights []hue.LightView, patterns []string) (keep []hue.LightView, excluded []string) {
	for _, l := range lights {
		if matchesAny(l, patterns) {
			excluded = append(excluded, l.Name)
			continue
		}
		keep = append(keep, l)
	}
	return keep, excluded
}

func matchesAny(l hue.LightView, patterns []string) bool {
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		for _, candidate := range []string{strings.ToLower(l.Name), strings.ToLower(l.DeviceName)} {
			if candidate == "" {
				continue
			}
			if candidate == p {
				return true
			}
			if ok, err := filepath.Match(p, candidate); err == nil && ok {
				return true
			}
		}
	}
	return false
}

// applyGracePeriod keeps back lights that have not been on long enough, which
// is how the sweep avoids switching off a light someone just turned on.
func applyGracePeriod(candidates []hue.LightView, in Inputs) (targets []Target, held []string) {
	grace := in.Cfg.MinOnDuration.Duration()
	for _, l := range candidates {
		t := Target{LightID: l.ID, Name: l.Name}

		var onSince time.Time
		known := false
		if in.OnSince != nil {
			onSince, known = in.OnSince(l.ID)
		}
		if known {
			t.OnFor = in.Now.Sub(onSince)
			if t.OnFor < 0 {
				t.OnFor = 0
			}
			t.HasOnFor = true
		}

		if in.IgnoreMinOn || grace <= 0 {
			targets = append(targets, t)
			continue
		}
		switch {
		case !known:
			held = append(held, fmt.Sprintf("%s held: first time seen on, needs %s on record",
				l.Name, hue.ShortDuration(grace)))
		case t.OnFor < grace:
			held = append(held, fmt.Sprintf("%s held: on for %s, under the %s grace period",
				l.Name, hue.ShortDuration(t.OnFor), hue.ShortDuration(grace)))
		default:
			targets = append(targets, t)
		}
	}
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
	return targets, held
}
