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

	context string
	// held and pending are tracked per (input source, key, round). Several
	// devices can co-drive the same action, and a release only removes the
	// exact press round it names.
	held     map[device.Source]map[device.Key]uint64
	pending  map[device.Source]map[device.Key]uint64
	engaged  map[string]*engagement
	toggled  map[string]bool  // binding id -> switch state (per context)
	active   map[string]bool  // binding id currently emitting Activate
	switches []config.Binding // context switches deferred from this edge
}

// New builds an engine starting in the configured start context.
func New(cfg *config.Compiled) *Engine {
	return &Engine{
		cfg:     cfg,
		context: cfg.Start,
		held:    make(map[device.Source]map[device.Key]uint64),
		pending: make(map[device.Source]map[device.Key]uint64),
		engaged: make(map[string]*engagement),
		toggled: make(map[string]bool),
		active:  make(map[string]bool),
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

// Process handles one raw attributed device event. A new physical press must
// carry a fresh, non-zero Round which its release repeats; this is what lets
// an out-of-order old release be distinguished from the new press's release.
// Duplicate edges are filtered by the device layer, but the engine also
// ignores no-op repeats defensively.
func (e *Engine) Process(ev device.Event, nowMs int64) []Event {
	edge := e.applyPhysicalEdge(device.Edge{Source: ev.Source, Key: ev.Key, Phase: ev.Phase, Round: ev.Round})
	return e.reconcile(edge, nowMs)
}

// ApplyEdge handles one already-normalised edge.
func (e *Engine) ApplyEdge(edge device.Edge, nowMs int64) []Event {
	return e.reconcile(e.applyPhysicalEdge(edge), nowMs)
}

// ApplyRelease injects a forced release for a key on one source (used after
// focus loss).
func (e *Engine) ApplyRelease(source device.Source, key device.Key, nowMs int64) []Event {
	round := e.held[source][key]
	delete(e.held[source], key)
	return e.reconcile(&device.Edge{Source: source, Key: key, Phase: device.Release, Round: round}, nowMs)
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

func (e *Engine) applyPhysicalEdge(in device.Edge) *device.Edge {
	source, key, phase, round := in.Source, in.Key, in.Phase, in.Round
	switch phase {
	case device.Press:
		// Any press re-confirms the physical key. If an old message is still
		// queued, it has a different (explicit) round and therefore can never
		// close this new holder.
		delete(e.sourcePending(source), key)
		if current, isHeld := e.sourceHeld(source)[key]; isHeld && current == round {
			return nil
		}
		e.sourceHeld(source)[key] = round
		edge := device.Edge{Source: source, Key: key, Phase: device.Press, Round: round}
		return &edge
	case device.Release:
		current, isHeld := e.held[source][key]
		pendingRound, hasPending := e.pending[source][key]
		matchesCurrent := isHeld && (current == round || (round == 0 && current == 0 && (!hasPending || pendingRound == 0)))
		if !matchesCurrent {
			if set := e.pending[source]; set != nil {
				if oldRound, ok := set[key]; ok && (oldRound == round || round == 0) {
					delete(set, key)
				}
			}
			return nil
		}
		delete(e.held[source], key)
		edge := device.Edge{Source: source, Key: key, Phase: device.Release, Round: round}
		return &edge
	}
	return nil
}

func (e *Engine) sourceHeld(source device.Source) map[device.Key]uint64 {
	set, ok := e.held[source]
	if !ok {
		set = make(map[device.Key]uint64)
		e.held[source] = set
	}
	return set
}

func (e *Engine) sourcePending(source device.Source) map[device.Key]uint64 {
	set, ok := e.pending[source]
	if !ok {
		set = make(map[device.Key]uint64)
		e.pending[source] = set
	}
	return set
}

// reconcile recomputes chord ownership after an edge (nil edge means a pure
// recompute, e.g. after a device was removed) and diffs engagements.
func (e *Engine) reconcile(edge *device.Edge, nowMs int64) []Event {
	bindings := e.cfg.Bindings(e.context)
	want, wantBySource := e.resolveOwners(bindings)

	var out []Event
	releasedKey := edge != nil && edge.Phase == device.Release

	// Disappear first, in reverse priority order for deterministic release.
	// An engagement only ends once no remaining source satisfies its chord.
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
	if edge != nil && edge.Phase == device.Press {
		for _, b := range bindings {
			if e.engaged[b.ID] != nil || !wantBySource[edge.Source][b.ID] {
				continue
			}
			// A chord only engages on the press edge that completes it, on
			// the source that completed it. A key left held after a
			// higher-priority chord breaks must not retroactively start a
			// new engagement.
			if !containsKey(b.Chord, edge.Key) {
				continue
			}
			g := &engagement{binding: b, startMs: nowMs}
			e.engaged[b.ID] = g
			out = append(out, e.beginEngagement(g, nowMs)...)
		}
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

// resolveOwners greedily assigns keys to the highest-priority satisfied
// chord, independently per input source: a chord must be held on one device
// (chords never merge keys across devices), while several devices may each
// satisfy the same chord and co-drive its action. Bindings sharing an
// identical chord share ownership (so tap + long-press on the same keys
// coexist); overlapping different chords cannot share keys on one source.
// It returns the union of wanted binding ids plus the per-source breakdown
// used to decide which source may start a new engagement.
func (e *Engine) resolveOwners(bindings []config.Binding) (map[string]bool, map[device.Source]map[string]bool) {
	want := make(map[string]bool)
	wantBySource := make(map[device.Source]map[string]bool)
	sources := make([]device.Source, 0, len(e.held))
	for source := range e.held {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i] < sources[j] })
	for _, source := range sources {
		heldSet := e.held[source]
		owners := make(map[device.Key]string)
		for _, b := range bindings {
			group := chordGroup(b.Chord)
			ok := true
			for _, key := range b.Chord {
				if _, held := heldSet[key]; !held {
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
			if wantBySource[source] == nil {
				wantBySource[source] = make(map[string]bool)
			}
			wantBySource[source][b.ID] = true
		}
	}
	return want, wantBySource
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

// teardown is the unified cleanup shared by focus loss and context switches.
// Active continuous actions are deactivated and latched switches are reset
// to off (each emitting exactly one Deactivate); pending taps and long
// presses are cancelled silently. Keys still physically held retain their
// original rounds as unconfirmed holders: only the matching old release can
// consume that record, and it can never remove a newer holder.
func (e *Engine) teardown(nowMs int64) []Event {
	out := e.deactivateAll(nowMs)
	for source, set := range e.held {
		for key, round := range set {
			e.markPending(source, key, round)
		}
	}
	e.held = make(map[device.Source]map[device.Key]uint64)
	e.engaged = make(map[string]*engagement)
	e.toggled = make(map[string]bool)
	e.active = make(map[string]bool)
	return out
}

// deactivateAll emits exactly one Deactivate per currently active binding.
func (e *Engine) deactivateAll(nowMs int64) []Event {
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
	return out
}

func (e *Engine) markPending(source device.Source, key device.Key, round uint64) {
	set, ok := e.pending[source]
	if !ok {
		set = make(map[device.Key]uint64)
		e.pending[source] = set
	}
	set[key] = round
}

// Blur handles input-focus loss with the unified teardown: held keys are
// released and latched switches are turned off, so no action (including
// toggles such as a flashlight) stays active after the window loses focus.
func (e *Engine) Blur(nowMs int64) []Event {
	return e.teardown(nowMs)
}

// DeviceRemoved handles one input device being unplugged or reset. The
// cleanup is scoped to that source: keys it still held retain their old round
// as unconfirmed input (so late releases arriving after removal or reconnect
// consume only that old round), latched switches reset to off, and
// continuous actions end only when no other connected device still
// satisfies their chord. State held by other sources is untouched, so a
// reconnected device starts from a clean slate and never inherits the old
// device's held keys.
func (e *Engine) DeviceRemoved(source device.Source, nowMs int64) []Event {
	var out []Event
	// Latched switches are engine-level state; a device teardown resets them
	// exactly once, like Blur does.
	ids := make([]string, 0, len(e.toggled))
	for id, on := range e.toggled {
		if on && e.active[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, b := range e.cfg.Bindings(e.context) {
			if b.ID == id {
				out = append(out, Event{
					Type: Deactivate, Action: b.Action, BindingID: b.ID, TimeMs: nowMs,
				})
			}
		}
		delete(e.toggled, id)
		delete(e.active, id)
	}
	for key, round := range e.held[source] {
		e.markPending(source, key, round)
	}
	delete(e.held, source)
	out = append(out, e.reconcile(nil, nowMs)...)
	return out
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

// InputRef identifies one physical input on one input device.
type InputRef struct {
	Source device.Source `json:"source"`
	Key    device.Key    `json:"key"`
	Round  uint64        `json:"round,omitempty"`
}

// Snapshot is the persistable, JSON-serialisable part of the engine state:
// enough to resume after a save/reload without carrying hidden physical
// state across the restart.
type Snapshot struct {
	// Context is the active context at save time.
	Context string `json:"context"`
	// Held lists current holders at save time, sorted.
	Held []InputRef `json:"held,omitempty"`
	// Pending lists old-round holders awaiting one matching stale release.
	Pending []InputRef `json:"pending,omitempty"`
	// Lingering is retained for old snapshots that did not carry rounds.
	Lingering []InputRef `json:"lingering,omitempty"`
	// Toggled lists binding ids whose switch is latched on, sorted.
	Toggled []string `json:"toggled,omitempty"`
}

// Snapshot captures the recoverable engine state. In-progress holds and
// pending taps/long presses are intentionally not saved: after a reload the
// corresponding physical inputs must be re-confirmed (see PendingReconfirm).
func (e *Engine) Snapshot() Snapshot {
	snap := Snapshot{Context: e.context}
	snap.Held = flattenRefs(e.held)
	snap.Pending = flattenRefs(e.pending)
	for id, on := range e.toggled {
		if on {
			snap.Toggled = append(snap.Toggled, id)
		}
	}
	sort.Strings(snap.Toggled)
	return snap
}

// Restore rebuilds a saved state into the engine, replacing whatever was
// there. Previously held inputs are treated as unconfirmed: they join the
// pending set, so a leftover release consumes only the saved old round and
// only a fresh press re-syncs them into a new round. Latched switches are
// turned back on, emitting exactly one Activate each (sorted by binding id)
// so the game can re-enable them.
func (e *Engine) Restore(snap Snapshot, nowMs int64) []Event {
	e.context = snap.Context
	if e.context == "" {
		e.context = e.cfg.Start
	}
	e.held = make(map[device.Source]map[device.Key]uint64)
	e.pending = make(map[device.Source]map[device.Key]uint64)
	e.engaged = make(map[string]*engagement)
	e.toggled = make(map[string]bool)
	e.active = make(map[string]bool)
	e.switches = nil
	for _, ref := range snap.Held {
		e.markPending(ref.Source, ref.Key, ref.Round)
	}
	for _, ref := range snap.Pending {
		e.markPending(ref.Source, ref.Key, ref.Round)
	}
	for _, ref := range snap.Lingering {
		e.markPending(ref.Source, ref.Key, ref.Round)
	}
	var out []Event
	for _, id := range snap.Toggled {
		for _, b := range e.cfg.Bindings(e.context) {
			if b.ID == id && b.Mode == config.ModeSwitch {
				e.toggled[id] = true
				e.active[id] = true
				out = append(out, Event{
					Type: Activate, Action: b.Action, BindingID: id, TimeMs: nowMs,
				})
			}
		}
	}
	return out
}

// PendingReconfirm lists every input whose physical state is unconfirmed,
// including its old press round, sorted by source then key. After a Restore
// this is exactly the set of inputs the caller must re-confirm with a fresh
// press before they can drive actions again; matching stale releases only
// remove the old-round pending record.
func (e *Engine) PendingReconfirm() []InputRef {
	return flattenRefs(e.pending)
}

// Holders lists every currently confirmed input, including its press round.
func (e *Engine) Holders() []InputRef { return flattenRefs(e.held) }

func flattenRefs(sets map[device.Source]map[device.Key]uint64) []InputRef {
	var out []InputRef
	for source, set := range sets {
		for key, round := range set {
			out = append(out, InputRef{Source: source, Key: key, Round: round})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Key < out[j].Key
	})
	return out
}
