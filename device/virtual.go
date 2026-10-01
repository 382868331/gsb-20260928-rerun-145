package device

// VirtualKeyboard is a keyboard-shaped virtual input source.
//
// It never touches a real device. Callers push synthetic messages through it;
// it tracks the set of currently-down keys and suppresses redundant messages
// (two "down" messages with no "up" between them, and vice versa).
//
// Blur/removal/reset force every held key up via synthetic, Forced-tagged
// release edges and start a new input epoch, guaranteeing the action layer
// neither ends up with keys stuck permanently nor accepts stale releases.
type VirtualKeyboard struct {
	down    map[Key]struct{}
	epoch   int64
	pending map[Key]int64 // keys from a reset generation still awaiting their stale physical release
}

// NewVirtualKeyboard creates an empty virtual keyboard.
func NewVirtualKeyboard() *VirtualKeyboard {
	return &VirtualKeyboard{down: make(map[Key]struct{}), pending: make(map[Key]int64)}
}

// Push feeds one raw event and returns the edge it produced, if any.
// A repeated message that does not change key state yields no edge and ok=false.
func (k *VirtualKeyboard) Push(ev Event) (edge Edge, ok bool) {
	if ev.Phase == Release {
		if _, held := k.down[ev.Key]; !held {
			// A release for a key not currently down can only be stale. If it
			// answers a press stranded before a forced reset, deliver it tagged
			// with the old epoch so the action layer can refuse it.
			if pressEpoch, waiting := k.pending[ev.Key]; waiting {
				delete(k.pending, ev.Key)
				return Edge{Key: ev.Key, Phase: Release, Epoch: pressEpoch}, true
			}
			return Edge{}, false
		}
		delete(k.down, ev.Key)
		return Edge{Key: ev.Key, Phase: Release, Epoch: k.epoch}, true
	}
	if _, held := k.down[ev.Key]; held {
		return Edge{}, false
	}
	// A press while a stranded pre-reset press is still awaiting its physical
	// release is a hardware-level duplicate (the key never actually came up):
	// suppress it. The late stale release clears the pending slot, after which
	// a genuinely new press is accepted.
	if _, waiting := k.pending[ev.Key]; waiting {
		return Edge{}, false
	}
	k.down[ev.Key] = struct{}{}
	return Edge{Key: ev.Key, Phase: Press, Epoch: k.epoch}, true
}

// Held reports whether a key is currently down.
func (k *VirtualKeyboard) Held(key Key) bool {
	_, held := k.down[key]
	return held
}

// FocusChange handles a host focus event. On focus loss it performs a forced
// reset (see Reset): every key still held gets a synthetic release edge tagged
// Forced, in deterministic key order, and a new input epoch begins so stale
// physical releases can never affect the next focused session. Regaining
// focus emits no edges.
func (k *VirtualKeyboard) FocusChange(ev FocusEvent) []Edge {
	if ev.Focused {
		return nil
	}
	return k.Reset()
}

// Removed reports that the device was unplugged. It behaves like focus loss:
// forced, ordered release edges plus a fresh input epoch.
func (k *VirtualKeyboard) Removed() []Edge {
	return k.Reset()
}

// Reset forcibly ends the current input generation. It synthesises release
// edges for every key still held, in deterministic key order, remembers the
// stranded presses so their late physical releases can be identified as
// stale, and advances the input epoch.
func (k *VirtualKeyboard) Reset() []Edge {
	keys := make([]Key, 0, len(k.down))
	for key := range k.down {
		keys = append(keys, key)
	}
	sortKeys(keys)
	edges := make([]Edge, 0, len(keys))
	for _, key := range keys {
		delete(k.down, key)
		if _, exists := k.pending[key]; !exists {
			k.pending[key] = k.epoch
		}
		edges = append(edges, Edge{Key: key, Phase: Release, Epoch: k.epoch, Forced: true})
	}
	k.epoch++
	return edges
}

// Epoch reports the current input generation.
func (k *VirtualKeyboard) Epoch() int64 { return k.epoch }
