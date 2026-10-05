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
	down   map[Key]int64 // key -> live round currently down
	// released remembers rounds force-ended by blur/reset whose physical
	// release message may still arrive after a newer press.
	released map[Key]int64
	round    int64 // latest round handed out on this device
}

// NewVirtualKeyboard creates an empty virtual keyboard. An optional source
// name tags every emitted edge so the engine can attribute input to this
// device; with no argument the DefaultSource is used.
func NewVirtualKeyboard(source ...Source) *VirtualKeyboard {
	src := DefaultSource
	if len(source) > 0 {
		src = source[0]
	}
	return &VirtualKeyboard{source: src, down: make(map[Key]int64), released: make(map[Key]int64)}
}

// NewVirtualKeyboardFromRound creates a device whose next minted round is
// round+1. Pass a persistent per-device counter across a reconnect so that
// rounds from the device's previous incarnation can never collide with the
// new one.
func NewVirtualKeyboardFromRound(source Source, round int64) *VirtualKeyboard {
	return &VirtualKeyboard{source: source, down: make(map[Key]int64), released: make(map[Key]int64), round: round}
}

// Round reports the latest round minted on this device (0 before any press).
func (k *VirtualKeyboard) Round() int64 { return k.round }

// Source reports the device name attached to emitted edges.
func (k *VirtualKeyboard) Source() Source { return k.source }

func (k *VirtualKeyboard) edge(key Key, phase Phase, round int64) Edge {
	return Edge{Source: k.source, Key: key, Phase: phase, Round: round}
}

// Push feeds one raw event and returns the edge it produced, if any.
// A repeated message that does not change key state yields no edge and ok=false.
func (k *VirtualKeyboard) Push(ev Event) (edge Edge, ok bool) {
	oldRound, held := k.down[ev.Key]
	switch {
	case ev.Phase == Press && !held:
		round := ev.Round
		if round == 0 {
			k.round++
			round = k.round
		} else if round <= k.round {
			return Edge{}, false
		}
		k.round = round
		k.down[ev.Key] = round
		return k.edge(ev.Key, Press, round), true
	case ev.Phase == Release:
		round := ev.Round
		if round == 0 {
			if held {
				if staleRound, waiting := k.released[ev.Key]; waiting && staleRound < oldRound {
					delete(k.released, ev.Key)
					return k.edge(ev.Key, Release, staleRound), true
				}
			} else {
				return Edge{}, false
			}
			round = oldRound
		}
		// A release that names the live round closes it. A release naming an
		// older round (arriving after a newer press, after blur+repress, or
		// duplicated) is still emitted with its own round so the action layer
		// can attribute and ignore it; swallowing it here would make the next
		// in-order release look stale and could wedge an action on.
		if held && round == oldRound {
			delete(k.down, ev.Key)
			delete(k.released, ev.Key)
		}
		return k.edge(ev.Key, Release, round), true
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
		k.released[key] = round
		edges = append(edges, k.edge(key, Release, round))
	}
	return edges
}

// Reset releases every held key, returning the synthetic release edges.
func (k *VirtualKeyboard) Reset() []Edge {
	return k.FocusChange(FocusEvent{Focused: false})
}
