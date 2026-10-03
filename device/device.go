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
	// Device identifies the physical input source the message came from
	// ("kbd:0", "pad:1", ...). Empty means "keyboard" for convenience.
	Device string `json:"device,omitempty"`
	Seq    int64
	Key    Key
	Phase  Phase
}

// Edge is a normalised transition emitted by a device.
// Duplicate physical edges collapse to at most one Edge per state change.
type Edge struct {
	// Device records which input source produced the edge. The action layer
	// keeps attribution per device so a late release from one device can
	// never cancel an action held by another.
	Device string `json:"device,omitempty"`
	Key    Key
	Phase  Phase
}

// DefaultDeviceID is the source assigned to events/edges that carry no
// explicit device id.
const DefaultDeviceID = "keyboard"

// NormalizeDeviceID returns id, or DefaultDeviceID when id is empty.
func NormalizeDeviceID(id string) string {
	if id == "" {
		return DefaultDeviceID
	}
	return id
}

// FocusEvent reports input-focus changes from the host window.
type FocusEvent struct {
	Focused bool
}
