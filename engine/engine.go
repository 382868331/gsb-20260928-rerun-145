// Package engine turns normalised device edges into game actions.
//
// The engine is fully deterministic: wall time is never read. Replays supply
// monotonically increasing millisecond timestamps, so the same configuration
// and event sequence always produce the same action sequence.
package engine

import (
	"encoding/json"
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
	// sustainers records every (device, key-set) input currently holding the
	// whole chord. The action lives while at least one sustainer remains.
	sustainers map[inputSource]struct{}
}

// inputSource attributes a physical input: which device, and in which session
// (epoch) of that device the edge happened. Replugging a device bumps the
// epoch, so a late release from the unplugged session cannot cancel a press
// delivered by the freshly attached one.
type inputSource struct {
	device string
	epoch  int64
}

// Engine maps device edges to actions for one configuration.
type Engine struct {
	cfg *config.Compiled

	context string
	// held[inputSource][key] is the key currently pressed by that device session.
	held map[inputSource]map[device.Key]struct{}
	// lingering[inputSource] holds keys still physically down across a teardown;
	// their old-session releases are stale and must not cancel new actions.
	lingering map[inputSource]map[device.Key]struct{}
	// epochs tracks the current session number per attached device id.
	epochs    map[string]int64
	attached  map[string]bool
	reconfirm map[PendingInput]struct{} // inputs needing a fresh press after restore
	engaged   map[string]*engagement
	owner     map[inputSource]map[device.Key]string // key -> chord group claiming it
	toggled   map[string]bool                       // binding id -> switch state (per context)
	active    map[string]bool                       // binding id currently emitting Activate
	switches  []config.Binding                      // context switches deferred from this edge
}

// New builds an engine starting in the configured start context.
func New(cfg *config.Compiled) *Engine {
	return &Engine{
		cfg:       cfg,
		context:   cfg.Start,
		held:      make(map[inputSource]map[device.Key]struct{}),
		lingering: make(map[inputSource]map[device.Key]struct{}),
		epochs:    make(map[string]int64),
		attached:  make(map[string]bool),
		reconfirm: make(map[PendingInput]struct{}),
		engaged:   make(map[string]*engagement),
		owner:     make(map[inputSource]map[device.Key]string),
		toggled:   make(map[string]bool),
		active:    make(map[string]bool),
	}
}

// AttachDevice registers an input device. An unknown device mentioned in an
// event is attached lazily on its first edge; explicit attachment is useful
// when reconnecting a known device id, which starts a fresh session (epoch)
// so stale messages and held keys from the old session cannot bleed in.
func (e *Engine) AttachDevice(deviceID string) {
	id := device.NormalizeDeviceID(deviceID)
	e.epochs[id]++
	e.attached[id] = true
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
// device layer, but the engine also ignores no-op repeats defensively). The
// event keeps its originating device id, so a late release from an old round
// or a removed session can never cancel an action held by another inputSource.
func (e *Engine) Process(ev device.Event, nowMs int64) []Event {
	src := e.sourceFor(ev.Device)
	edges := e.applyPhysical(src, ev.Key, ev.Phase)
	return e.reconcile(src, edges, nowMs)
}

// ApplyEdge handles one already-normalised edge, attributed to its device.
func (e *Engine) ApplyEdge(edge device.Edge, nowMs int64) []Event {
	id := device.NormalizeDeviceID(edge.Device)
	if !e.attached[id] {
		e.AttachDevice(id)
	}
	src := inputSource{device: id, epoch: e.epochs[id]}
	edges := e.applyPhysical(src, edge.Key, edge.Phase)
	return e.reconcile(src, edges, nowMs)
}

// ApplyRelease injects a forced release for a key of the given device (used
// after focus loss). An empty device id means the default keyboard.
func (e *Engine) ApplyRelease(deviceID string, key device.Key, nowMs int64) []Event {
	id := device.NormalizeDeviceID(deviceID)
	src := inputSource{device: id, epoch: e.epochs[id]}
	edges := e.applyPhysical(src, key, device.Release)
	return e.reconcile(src, edges, nowMs)
}

// sourceFor maps a raw event to its device session. An unseen device is
// attached lazily on its first edge. Events stamped with an epoch older than
// the current session (an unplugged/replugged device) belong to a dead round
// and are dropped.
func (e *Engine) sourceFor(id0 string) inputSource {
	id := device.NormalizeDeviceID(id0)
	if !e.attached[id] {
		e.AttachDevice(id)
	}
	return inputSource{device: id, epoch: e.epochs[id]}
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

func (e *Engine) applyPhysical(src inputSource, key device.Key, phase device.Phase) []device.Edge {
	switch phase {
	case device.Press:
		// A press always re-syncs this device's lingering key into the new
		// round; it never touches another device's attribution.
		delete(e.lingering[src], key)
		if _, isHeld := e.held[src][key]; isHeld {
			return nil
		}
		if e.held[src] == nil {
			e.held[src] = make(map[device.Key]struct{})
		}
		e.held[src][key] = struct{}{}
		delete(e.reconfirm, PendingInput{Device: src.device, Key: key})
		return []device.Edge{{Device: src.device, Key: key, Phase: device.Press}}
	case device.Release:
		if stale, ok := e.lingering[src]; ok {
			if _, isStale := stale[key]; isStale {
				// Continuation rule: a release left over from before a teardown
				// (focus loss, device removal, context switch) must not cancel
				// anything in the new round.
				delete(stale, key)
				if len(stale) == 0 {
					delete(e.lingering, src)
				}
				return nil
			}
		}
		if _, isHeld := e.held[src][key]; !isHeld {
			return nil
		}
		delete(e.held[src], key)
		if len(e.held[src]) == 0 {
			delete(e.held, src)
		}
		return []device.Edge{{Device: src.device, Key: key, Phase: device.Release}}
	}
	return nil
}

// reconcile recomputes chord ownership after an edge and diffs engagements.
// Ownership is resolved independently per device session: several devices
// may each hold the same chord and they sustain the action together.
func (e *Engine) reconcile(src inputSource, edges []device.Edge, nowMs int64) []Event {
	if len(edges) == 0 {
		return nil
	}

	bindings := e.cfg.Bindings(e.context)
	want := make(map[string]map[inputSource]bool) // binding id -> sustaining sources
	e.owner = make(map[inputSource]map[device.Key]string)
	for heldSrc := range e.held {
		for id := range e.resolveOwners(heldSrc, bindings) {
			if want[id] == nil {
				want[id] = make(map[inputSource]bool)
			}
			want[id][heldSrc] = true
		}
	}

	var out []Event
	releasedKey := edges[0].Phase == device.Release
	pressedKey := edges[0].Key

	// Refresh each live engagement's sustainer set; an action ends only when
	// its last sustaining device stops holding the chord.
	for id, g := range e.engaged {
		for s := range g.sustainers {
			if want[id] == nil || !want[id][s] {
				delete(g.sustainers, s)
			}
		}
	}

	// Appearances in priority order: a device that now satisfies the chord is
	// added as a sustainer. A brand-new engagement may only start on the press
	// edge that completes it (no retroactive triggering), while a second
	// device joining an already-started action just sustains it.
	for _, b := range bindings {
		g := e.engaged[b.ID]
		if want[b.ID] == nil || !want[b.ID][src] {
			continue
		}
		if g == nil {
			if releasedKey || !containsKey(b.Chord, pressedKey) {
				delete(want[b.ID], src)
				continue
			}
			g = &engagement{binding: b, startMs: nowMs, sustainers: make(map[inputSource]struct{})}
			e.engaged[b.ID] = g
			out = append(out, e.beginEngagement(g, nowMs)...)
		}
		g.sustainers[src] = struct{}{}
	}

	// Disappear last, in reverse priority order for deterministic release.
	gone := make([]string, 0)
	for id, g := range e.engaged {
		if len(g.sustainers) == 0 {
			gone = append(gone, id)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(gone)))
	for _, id := range gone {
		g := e.engaged[id]
		out = append(out, e.endEngagement(g, releasedKey, nowMs)...)
		delete(e.engaged, id)
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

// resolveOwners greedily assigns one device session's held keys to the
// highest-priority satisfied chord. Bindings sharing an identical chord
// share ownership (tap + long-press on the same keys coexist); overlapping
// different chords cannot share keys. Other devices resolve independently.
func (e *Engine) resolveOwners(src inputSource, bindings []config.Binding) map[string]bool {
	held := e.held[src]
	want := make(map[string]bool)
	owners := make(map[device.Key]string)
	for _, b := range bindings {
		group := chordGroup(b.Chord)
		ok := true
		for _, key := range b.Chord {
			if _, isHeld := held[key]; !isHeld {
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
	e.owner[src] = owners
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
	out := e.teardownAll(nowMs)
	e.context = sw.TargetContext
	out = append(out, Event{
		Type: ContextSwitched, BindingID: sw.ID,
		FromContext: from, ToContext: sw.TargetContext, TimeMs: nowMs,
	})
	return out
}

// emitDeactivations ends every active continuous action and resets latched
// switches to off, each emitting exactly one Deactivate. Pending taps and
// long presses are cancelled silently. When keep reports an id that other
// devices still sustain, that action is left running.
func (e *Engine) emitDeactivations(nowMs int64, keep func(string) bool) []Event {
	ids := make([]string, 0, len(e.active))
	for id, on := range e.active {
		if on && (keep == nil || !keep(id)) {
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
		delete(e.active, id)
		delete(e.toggled, id)
	}
	return out
}

// linger moves every key still physically held by the given sessions into the
// lingering set, attributed to the exact device session, so their eventual
// releases are treated as stale input from the previous round rather than
// cancelling new engagements. Other sessions are untouched.
func (e *Engine) linger(sources []inputSource) {
	for _, src := range sources {
		keys := e.held[src]
		if len(keys) == 0 {
			continue
		}
		if e.lingering[src] == nil {
			e.lingering[src] = make(map[device.Key]struct{})
		}
		for key := range keys {
			e.lingering[src][key] = struct{}{}
		}
		delete(e.held, src)
	}
}

// teardownAll is the unified cleanup shared by focus loss and context
// switches: every device session is torn down at once.
func (e *Engine) teardownAll(nowMs int64) []Event {
	out := e.emitDeactivations(nowMs, nil)
	sources := make([]inputSource, 0, len(e.held))
	for src := range e.held {
		sources = append(sources, src)
	}
	e.linger(sources)
	e.engaged = make(map[string]*engagement)
	e.owner = make(map[inputSource]map[device.Key]string)
	e.toggled = make(map[string]bool)
	e.active = make(map[string]bool)
	return out
}

// teardownDevice ends only what one device session was sustaining. An action
// also held by another device stays active, which is why a multi-device hold
// survives unplugging one inputSource. The removed session's held keys linger so
// its own late releases are swallowed without touching anyone else.
func (e *Engine) teardownDevice(src inputSource, nowMs int64) []Event {
	stillSustained := func(id string) bool {
		g := e.engaged[id]
		if g == nil {
			return false
		}
		for other := range g.sustainers {
			if other != src {
				return true
			}
		}
		return false
	}
	out := e.emitDeactivations(nowMs, stillSustained)
	e.linger([]inputSource{src})
	for id, g := range e.engaged {
		if _, gone := g.sustainers[src]; gone {
			delete(g.sustainers, src)
			if len(g.sustainers) == 0 {
				delete(e.engaged, id)
			}
		}
	}
	delete(e.owner, src)
	return out
}

// Blur handles input-focus loss: every device's held keys are released and
// latched switches turned off, so no action (including toggles such as a
// flashlight) stays active after the window loses focus.
func (e *Engine) Blur(nowMs int64) []Event {
	return e.teardownAll(nowMs)
}

// DeviceRemoved handles one input device being unplugged or reset. Only that
// device's session is torn down: an action still held on another device stays
// active. The device is detached and its session closed, so late releases and
// stale held state cannot reach a future replacement; reusing the same device
// id requires re-attaching it, which opens a fresh session (new epoch).
func (e *Engine) DeviceRemoved(deviceID string, nowMs int64) []Event {
	id := device.NormalizeDeviceID(deviceID)
	src := inputSource{device: id, epoch: e.epochs[id]}
	out := e.teardownDevice(src, nowMs)
	delete(e.attached, id)
	delete(e.lingering, src)
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

// PendingInput is one physical input whose state cannot be trusted after a
// restart: the player may or may not still be holding it, so it must be
// pressed again before it can drive any action.
type PendingInput struct {
	Device string     `json:"device"`
	Key    device.Key `json:"key"`
}

// SavedState is the serialisable, replayable engine state.
type SavedState struct {
	Context string          `json:"context"`
	Toggled map[string]bool `json:"toggled"`
	// Devices records each attached device id and its current session epoch.
	Devices []DeviceSession `json:"devices"`
	// Reconfirm lists inputs that were physically held (or held across a
	// teardown) when the snapshot was taken. After reopening, the engine
	// treats them as released; the user must press them again to reconfirm.
	Reconfirm []PendingInput `json:"reconfirm"`
}

// DeviceSession identifies one attached device and its session epoch.
// Reconnected devices carry a new epoch, so old-session messages never apply.
type DeviceSession struct {
	ID    string `json:"id"`
	Epoch int64  `json:"epoch"`
}

// Snapshot captures the resumable state. Everything physically held at save
// time is listed in Reconfirm: the restored engine never assumes a key is
// still down, so stale/late messages from before the save cannot drive the
// fresh round. Latched switch state and the current context survive.
func (e *Engine) Snapshot() SavedState {
	state := SavedState{
		Context: e.context,
		Toggled: make(map[string]bool),
	}
	ids := make([]string, 0, len(e.attached))
	for id := range e.attached {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		state.Devices = append(state.Devices, DeviceSession{ID: id, Epoch: e.epochs[id]})
	}
	for id, on := range e.toggled {
		if on {
			state.Toggled[id] = true
		}
	}
	seen := make(map[PendingInput]struct{})
	add := func(src inputSource, key device.Key) {
		p := PendingInput{Device: src.device, Key: key}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		state.Reconfirm = append(state.Reconfirm, p)
	}
	srcs := make([]inputSource, 0, len(e.held)+len(e.lingering))
	for src := range e.held {
		srcs = append(srcs, src)
	}
	for src := range e.lingering {
		srcs = append(srcs, src)
	}
	sort.Slice(srcs, func(i, j int) bool {
		if srcs[i].device != srcs[j].device {
			return srcs[i].device < srcs[j].device
		}
		return srcs[i].epoch < srcs[j].epoch
	})
	for _, src := range srcs {
		keys := make([]device.Key, 0)
		for key := range e.held[src] {
			keys = append(keys, key)
		}
		for key := range e.lingering[src] {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, key := range keys {
			add(src, key)
		}
	}
	return state
}

// Restore loads a snapshot into the engine. No physical input is assumed
// held: every entry of Reconfirm must be pressed again before it can start
// or sustain an action. Restored devices keep their recorded epoch, so any
// message tagged with an older session is still treated as stale.
func (e *Engine) Restore(state SavedState) {
	e.context = state.Context
	e.held = make(map[inputSource]map[device.Key]struct{})
	e.lingering = make(map[inputSource]map[device.Key]struct{})
	e.epochs = make(map[string]int64)
	e.attached = make(map[string]bool)
	e.engaged = make(map[string]*engagement)
	e.owner = make(map[inputSource]map[device.Key]string)
	e.active = make(map[string]bool)
	e.toggled = make(map[string]bool)
	e.reconfirm = make(map[PendingInput]struct{})
	for _, d := range state.Devices {
		e.attached[d.ID] = true
		e.epochs[d.ID] = d.Epoch
	}
	for id, on := range state.Toggled {
		e.toggled[id] = on
		e.active[id] = on
	}
	for _, p := range state.Reconfirm {
		e.reconfirm[p] = struct{}{}
	}
}

// PendingReconfirm reports the inputs that still need a fresh press after the
// last Restore (an input drops off the list once it is pressed again).
func (e *Engine) PendingReconfirm() []PendingInput {
	out := make([]PendingInput, 0, len(e.reconfirm))
	for p := range e.reconfirm {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Device != out[j].Device {
			return out[i].Device < out[j].Device
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// MarshalState is a JSON convenience wrapper around Snapshot.
func (e *Engine) MarshalState() ([]byte, error) {
	return json.Marshal(e.Snapshot())
}

// UnmarshalState is a JSON convenience wrapper around Restore.
func (e *Engine) UnmarshalState(data []byte) error {
	var state SavedState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	e.Restore(state)
	return nil
}
