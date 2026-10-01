// Package device defines the hardware-adjacent input layer.
//
// Only virtual keyboard devices are provided in this iteration. Nothing here
// ever reads a real keyboard or any OS event source: callers feed events in.
// The device layer is intentionally action-agnostic; it normalises raw key
// transitions (deduplicates repeated messages, reports focus loss) so that
// higher layers only ever see clean press/release edges.
package device

import "fmt"

// Key is a physical/virtual key identifier, e.g. "A", "Space", "CtrlLeft".
type Key string

// Phase describes a key edge or a focus change.
type Phase int

const (
	// Press is a key-down edge.
	Press Phase = iota
	// Release is a key-up edge.
	Release
)

func (p Phase) String() string {
	switch p {
	case Press:
		return "press"
	case Release:
		return "release"
	default:
		return fmt.Sprintf("phase(%d)", int(p))
	}
}

// Event is one raw message delivered to a virtual device.
// Seq orders messages from one source; Down/Up share the same key space.
type Event struct {
	Seq   int64
	Key   Key
	Phase Phase
}

// Edge is a normalised transition emitted by a device.
// Duplicate physical edges collapse to at most one Edge per state change.
type Edge struct {
	Key   Key
	Phase Phase
	// Epoch identifies the input generation the edge belongs to. Forced
	// resets (focus loss, device removal, context switch) start a new
	// epoch, so a stale physical release left over from before a reset can
	// never release an action engaged by a later press.
	Epoch int64
	// Forced marks a synthetic edge produced by a reset rather than by a
	// real message. Action layers use it only for bookkeeping: it must not
	// fire taps or any other action (teardown is reported separately).
	Forced bool
}

// FocusEvent reports input-focus changes from the host window.
type FocusEvent struct {
	Focused bool
}
