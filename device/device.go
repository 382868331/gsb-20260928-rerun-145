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

// Source identifies one input device feeding the engine, e.g. "kbd0" or
// "pad1". The zero value (DefaultSource) is a valid single-device source.
type Source string

// DefaultSource is the implicit source used when a caller does not name one.
const DefaultSource Source = ""

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
	Seq    int64
	Source Source
	Key    Key
	Phase  Phase
	// Round tags a raw message with the input round it belongs to, mirroring
	// Edge.Round. A press with Round 0 asks the device to mint a new round;
	// a release with Round 0 closes whatever round the device currently
	// considers live (legacy, in-order traffic). A release carrying a
	// non-zero round is forwarded verbatim even if the device already moved
	// on to a newer round, so a late release can be attributed instead of
	// being confused with the next round.
	Round int64
}

// Edge is a normalised transition emitted by a device.
// Duplicate physical edges collapse to at most one Edge per state change.
type Edge struct {
	Source Source
	Key    Key
	Phase  Phase
	// Round identifies the input round this edge belongs to. Every press
	// that starts a new down-stretch gets a fresh, monotonically increasing
	// round on that device; the matching release carries the same round.
	// A release whose round no longer matches the live round is provably a
	// stale or duplicated message from an earlier round.
	Round int64
}

// FocusEvent reports input-focus changes from the host window.
type FocusEvent struct {
	Focused bool
}
