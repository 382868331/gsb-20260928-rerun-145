package device

// VirtualKeyboard is a keyboard-shaped virtual input source.
//
// It never touches a real device. Callers push synthetic messages through it;
// it tracks the set of currently-down keys and suppresses redundant messages
// (two "down" messages with no "up" between them, and vice versa).
//
// Blur forces every held key up via synthetic release edges, guaranteeing the
// action layer cannot end up with keys stuck permanently after focus loss.
type VirtualKeyboard struct {
	source Source
	down   map[Key]uint64
}

// NewVirtualKeyboard creates an empty virtual keyboard. An optional source
// name tags every emitted edge so the engine can attribute input to this
// device; with no argument the DefaultSource is used.
func NewVirtualKeyboard(source ...Source) *VirtualKeyboard {
	src := DefaultSource
	if len(source) > 0 {
		src = source[0]
	}
	return &VirtualKeyboard{source: src, down: make(map[Key]uint64)}
}

// Source reports the device name attached to emitted edges.
func (k *VirtualKeyboard) Source() Source { return k.source }

func (k *VirtualKeyboard) edge(key Key, phase Phase) Edge {
	return Edge{Source: k.source, Key: key, Phase: phase}
}

// Push feeds one raw event and returns the edge it produced, if any.
// A repeated message that does not change key state yields no edge and ok=false.
func (k *VirtualKeyboard) Push(ev Event) (edge Edge, ok bool) {
	round, held := k.down[ev.Key]
	switch {
	case ev.Phase == Press && (!held || round != ev.Round):
		k.down[ev.Key] = ev.Round
		return Edge{Source: k.source, Key: ev.Key, Phase: Press, Round: ev.Round}, true
	case ev.Phase == Release && held && round == ev.Round:
		delete(k.down, ev.Key)
		return Edge{Source: k.source, Key: ev.Key, Phase: Release, Round: ev.Round}, true
	case ev.Phase == Release && held && round != ev.Round:
		return Edge{Source: k.source, Key: ev.Key, Phase: Release, Round: ev.Round}, true
	default:
		return Edge{}, false
	}
}

// Held reports whether a key is currently down.
func (k *VirtualKeyboard) Held(key Key) bool {
	_, held := k.down[key]
	return held
}

// FocusChange handles a host focus event. On focus loss it synthesises release
// edges for every key still held, in deterministic key order, and clears
// internal state. Regaining focus emits no edges.
func (k *VirtualKeyboard) FocusChange(ev FocusEvent) []Edge {
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
		round := k.down[key]
		delete(k.down, key)
		edges = append(edges, Edge{Source: k.source, Key: key, Phase: Release, Round: round})
	}
	return edges
}

// Reset releases every held key, returning the synthetic release edges.
func (k *VirtualKeyboard) Reset() []Edge {
	return k.FocusChange(FocusEvent{Focused: false})
}
