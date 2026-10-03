package device

// VirtualDevice is a keyboard/controller-shaped virtual input source.
//
// It never touches a real device. Callers push synthetic messages through it;
// it tracks the set of currently-down keys and suppresses redundant messages
// (two "down" messages with no "up" between them, and vice versa).
//
// Blur forces every held key up via synthetic release edges, guaranteeing the
// action layer cannot end up with keys stuck permanently after focus loss.
//
// Each instance owns a stable Device ID; edges it emits are tagged with that
// ID so the engine can attribute presses and releases independently. A
// disconnected device is simply discarded: its replacement gets a fresh ID
// (or an explicit ID change), which prevents old held state leaking onto the
// new device.
type VirtualDevice struct {
	id   string
	down map[Key]struct{}
}

// NewVirtualKeyboard creates an empty virtual keyboard with the given id.
// An empty id becomes DefaultDeviceID ("keyboard").
func NewVirtualKeyboard(id string) *VirtualDevice {
	return &VirtualDevice{id: NormalizeDeviceID(id), down: make(map[Key]struct{})}
}

// NewVirtualController creates an empty virtual controller/button device.
// Controller buttons use the same Key space; only the device id differs.
func NewVirtualController(id string) *VirtualDevice {
	return &VirtualDevice{id: NormalizeDeviceID(id), down: make(map[Key]struct{})}
}

// ID reports the device id stamped onto emitted edges.
func (k *VirtualDevice) ID() string { return k.id }

// Push feeds one raw event and returns the edge it produced, if any.
// A repeated message that does not change key state yields no edge and ok=false.
func (k *VirtualDevice) Push(ev Event) (edge Edge, ok bool) {
	_, held := k.down[ev.Key]
	switch {
	case ev.Phase == Press && !held:
		k.down[ev.Key] = struct{}{}
		return Edge{Device: k.id, Key: ev.Key, Phase: Press}, true
	case ev.Phase == Release && held:
		delete(k.down, ev.Key)
		return Edge{Device: k.id, Key: ev.Key, Phase: Release}, true
	default:
		return Edge{}, false
	}
}

// Held reports whether a key is currently down.
func (k *VirtualDevice) Held(key Key) bool {
	_, held := k.down[key]
	return held
}

// FocusChange handles a host focus event. On focus loss it synthesises release
// edges for every key still held, in deterministic key order, and clears
// internal state. Regaining focus emits no edges.
func (k *VirtualDevice) FocusChange(ev FocusEvent) []Edge {
	if ev.Focused {
		return nil
	}
	keys := make([]Key, 0, len(k.down))
	for key := range k.down {
		keys = append(keys, key)
	}
	sortKeys(keys)
	edges := make([]Edge, 0, len(keys))
	for _, key := range keys {
		delete(k.down, key)
		edges = append(edges, Edge{Device: k.id, Key: key, Phase: Release})
	}
	return edges
}

// Reset releases every held key, returning the synthetic release edges.
func (k *VirtualDevice) Reset() []Edge {
	return k.FocusChange(FocusEvent{Focused: false})
}
