// Package engine turns normalised device edges into game actions.
//
// The engine is fully deterministic: wall time is never read. Replays supply
// monotonically increasing millisecond timestamps, so the same configuration
// and event sequence always produce the same action sequence.
package engine

import (
	"sort"

	"gameinput/config"
	"gameinput/device"
)

// EventType enumerates action-layer outputs.
type EventType int

const (
	// Activate begins an action (hold engaged, or a switch turned on).
	Activate EventType = iota
	// Deactivate ends an action (hold released, or a switch turned off).
	Deactivate
	// Pulse is a one-shot action (tap or long press).
	Pulse
	// ContextSwitched reports a successful context change.
	ContextSwitched
)

// Event is one action-layer output.
type Event struct {
	Type        EventType
	Action      string
	BindingID   string
	FromContext string
	ToContext   string
	TimeMs      int64
}

type engagement struct {
	binding   config.Binding
	startMs   int64
	longFired bool
}

// Engine maps keyboard edges to actions for one configuration.
type Engine struct {
	cfg *config.Compiled

	context   string
	held      map[device.Key]struct{}
	lingering map[device.Key]struct{} // physically down across a teardown; next release is stale
	engaged   map[string]*engagement
	owner     map[device.Key]string // key -> chord group currently claiming it
	toggled   map[string]bool       // binding id -> switch state (per context)
	active    map[string]bool       // binding id currently emitting Activate
	switches  []config.Binding      // context switches deferred from this edge
}

// New builds an engine starting in the configured start context.
func New(cfg *config.Compiled) *Engine {
	return &Engine{
		cfg:       cfg,
		context:   cfg.Start,
		held:      make(map[device.Key]struct{}),
		lingering: make(map[device.Key]struct{}),
		engaged:   make(map[string]*engagement),
		owner:     make(map[device.Key]string),
		toggled:   make(map[string]bool),
		active:    make(map[string]bool),
	}
}

// Context reports the active context.
func (e *Engine) Context() string { return e.context }

// Active reports whether an action is currently active (hold/switch).
func (e *Engine) Active(action string) bool {
	for id := range e.active {
		if e.active[id] {
			for _, b := range e.cfg.Bindings(e.context) {
				if b.ID == id && b.Action == action {
					return true
				}
			}
		}
	}
	return false
}

// Process handles one raw device event (duplicate edges are filtered by the
// device layer, but the engine also ignores no-op repeats defensively).
func (e *Engine) Process(ev device.Event, nowMs int64) []Event {
	edges := e.applyPhysical(ev.Key, ev.Phase)
	return e.reconcile(edges, nowMs)
}

// ApplyEdge handles one already-normalised edge.
func (e *Engine) ApplyEdge(edge device.Edge, nowMs int64) []Event {
	edges := e.applyPhysical(edge.Key, edge.Phase)
	return e.reconcile(edges, nowMs)
}

// ApplyRelease injects a forced release for a key (used after focus loss).
func (e *Engine) ApplyRelease(key device.Key, nowMs int64) []Event {
	edges := e.applyPhysical(key, device.Release)
	return e.reconcile(edges, nowMs)
}

// Update expires long-press timers. Call it with the replay's current time.
func (e *Engine) Update(nowMs int64) []Event {
	var out []Event
	ids := make([]string, 0, len(e.engaged))
	for id := range e.engaged {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		g := e.engaged[id]
		if g == nil || g.longFired {
			continue
		}
		b := g.binding
		if b.Mode == config.ModeLongPress && nowMs-g.startMs >= b.LongPressMs {
			g.longFired = true
			out = append(out, Event{
				Type: Pulse, Action: b.Action, BindingID: b.ID, TimeMs: nowMs,
			})
		}
	}
	return out
}

func (e *Engine) applyPhysical(key device.Key, phase device.Phase) []device.Edge {
	switch phase {
	case device.Press:
		// A press always re-syncs a lingering key into the new round.
		delete(e.lingering, key)
		if _, isHeld := e.held[key]; isHeld {
			return nil
		}
		e.held[key] = struct{}{}
		return []device.Edge{{Key: key, Phase: device.Press}}
	case device.Release:
		if _, stale := e.lingering[key]; stale {
			// Continuation rule: a release left over from before a teardown
			// (focus loss, device removal, context switch) must not cancel
			// anything in the new round.
			delete(e.lingering, key)
			return nil
		}
		if _, isHeld := e.held[key]; !isHeld {
			return nil
		}
		delete(e.held, key)
		return []device.Edge{{Key: key, Phase: device.Release}}
	}
	return nil
}

// reconcile recomputes chord ownership after an edge and diffs engagements.
func (e *Engine) reconcile(edges []device.Edge, nowMs int64) []Event {
	if len(edges) == 0 {
		return nil
	}

	bindings := e.cfg.Bindings(e.context)
	want := e.resolveOwners(bindings)

	var out []Event
	releasedKey := edges[0].Phase == device.Release
	pressedKey := edges[0].Key

	// Disappear first, in reverse priority order for deterministic release.
	gone := make([]string, 0)
	for id := range e.engaged {
		if !want[id] {
			gone = append(gone, id)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(gone)))
	for _, id := range gone {
		g := e.engaged[id]
		out = append(out, e.endEngagement(g, releasedKey, nowMs)...)
		delete(e.engaged, id)
	}

	// Appearances in priority order.
	for _, b := range bindings {
		if !want[b.ID] || e.engaged[b.ID] != nil {
			continue
		}
		// A chord only engages on the press edge that completes it. A key left
		// held after a higher-priority chord breaks must not retroactively
		// start a new engagement.
		if releasedKey || !containsKey(b.Chord, pressedKey) {
			continue
		}
		g := &engagement{binding: b, startMs: nowMs}
		e.engaged[b.ID] = g
		out = append(out, e.beginEngagement(g, nowMs)...)
	}

	// A context switch requested during this edge releases everything held in
	// the old context and activates the new one.
	if len(e.switches) > 0 {
		sw := e.switches[0]
		e.switches = nil
		out = append(out, e.switchContext(sw, nowMs)...)
	}

	// Long presses that mature exactly on this time step.
	out = append(out, e.Update(nowMs)...)
	return out
}

// resolveOwners greedily assigns keys to the highest-priority satisfied chord.
// Bindings sharing an identical chord share ownership (so tap + long-press on
// the same keys coexist); overlapping different chords cannot share keys.
func (e *Engine) resolveOwners(bindings []config.Binding) map[string]bool {
	want := make(map[string]bool)
	owners := make(map[device.Key]string)
	for _, b := range bindings {
		group := chordGroup(b.Chord)
		ok := true
		for _, key := range b.Chord {
			if _, held := e.held[key]; !held {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		for _, key := range b.Chord {
			if owner, taken := owners[key]; taken && owner != group {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		for _, key := range b.Chord {
			if _, taken := owners[key]; !taken {
				owners[key] = group
			}
		}
		want[b.ID] = true
	}
	e.owner = owners
	return want
}

func (e *Engine) beginEngagement(g *engagement, nowMs int64) []Event {
	b := g.binding
	switch b.Mode {
	case config.ModeHold:
		e.active[b.ID] = true
		return []Event{{Type: Activate, Action: b.Action, BindingID: b.ID, TimeMs: nowMs}}
	case config.ModeSwitch:
		on := !e.toggled[b.ID]
		e.toggled[b.ID] = on
		e.active[b.ID] = on
		typ := Activate
		if !on {
			typ = Deactivate
		}
		return []Event{{Type: typ, Action: b.Action, BindingID: b.ID, TimeMs: nowMs}}
	case config.ModeContextSwitch:
		e.switches = append(e.switches, b)
		return nil
	case config.ModeTap, config.ModeLongPress:
		return nil // decided on release / timer
	}
	return nil
}

func (e *Engine) endEngagement(g *engagement, keyReleased bool, nowMs int64) []Event {
	b := g.binding
	switch b.Mode {
	case config.ModeHold:
		if e.active[b.ID] {
			delete(e.active, b.ID)
			return []Event{{Type: Deactivate, Action: b.Action, BindingID: b.ID, TimeMs: nowMs}}
		}
	case config.ModeTap:
		// A chord preempted by a higher-priority chord (no physical release) is
		// not a tap. A long-enough hold is also not a tap.
		if keyReleased && nowMs-g.startMs < b.LongPressMs {
			return []Event{{Type: Pulse, Action: b.Action, BindingID: b.ID, TimeMs: nowMs}}
		}
	case config.ModeLongPress:
		return nil // either already pulsed or the threshold was never reached
	}
	return nil
}

func (e *Engine) switchContext(sw config.Binding, nowMs int64) []Event {
	from := e.context
	out := e.teardown(nowMs)
	e.context = sw.TargetContext
	out = append(out, Event{
		Type: ContextSwitched, BindingID: sw.ID,
		FromContext: from, ToContext: sw.TargetContext, TimeMs: nowMs,
	})
	return out
}

// teardown is the unified cleanup shared by focus loss, device removal and
// context switches. Active continuous actions are deactivated and latched
// switches are reset to off (each emitting exactly one Deactivate); pending
// taps and long presses are cancelled silently. Keys still physically held
// become lingering so their eventual releases are treated as stale input
// from the previous round rather than cancelling new engagements.
func (e *Engine) teardown(nowMs int64) []Event {
	ids := make([]string, 0, len(e.active))
	for id, on := range e.active {
		if on {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var out []Event
	for _, id := range ids {
		for _, b := range e.cfg.Bindings(e.context) {
			if b.ID == id {
				out = append(out, Event{
					Type: Deactivate, Action: b.Action, BindingID: b.ID, TimeMs: nowMs,
				})
			}
		}
	}
	for key := range e.held {
		e.lingering[key] = struct{}{}
	}
	e.held = make(map[device.Key]struct{})
	e.engaged = make(map[string]*engagement)
	e.owner = make(map[device.Key]string)
	e.toggled = make(map[string]bool)
	e.active = make(map[string]bool)
	return out
}

// Blur handles input-focus loss with the unified teardown: held keys are
// released and latched switches are turned off, so no action (including
// toggles such as a flashlight) stays active after the window loses focus.
func (e *Engine) Blur(nowMs int64) []Event {
	return e.teardown(nowMs)
}

// DeviceRemoved handles the input device being unplugged or reset. It runs
// the same unified teardown as Blur: continuous actions deactivate, latched
// switches reset, and stale releases from the removed device are ignored.
func (e *Engine) DeviceRemoved(nowMs int64) []Event {
	return e.teardown(nowMs)
}

func chordGroup(chord []device.Key) string {
	out := ""
	for i, key := range chord {
		if i > 0 {
			out += "+"
		}
		out += string(key)
	}
	return out
}

func containsKey(chord []device.Key, key device.Key) bool {
	for _, k := range chord {
		if k == key {
			return true
		}
	}
	return false
}
