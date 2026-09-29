// Package config defines the serialisable action-binding configuration.
//
// A configuration maps key chords inside a named context to game actions.
// Configurations are plain data: saving one and replaying the same device
// events through an engine built from it always yields the same action stream.
package config

import (
	"errors"
	"fmt"
	"sort"

	"gameinput/device"
)

// Mode decides how a chord is turned into action events.
type Mode string

const (
	// ModeHold keeps the action active for as long as the whole chord is held.
	ModeHold Mode = "hold"
	// ModeTap fires the action once when the chord is released quickly.
	ModeTap Mode = "tap"
	// ModeLongPress fires the action once after the chord is held continuously
	// past the long-press threshold; a quicker release becomes a tap only when
	// a separate ModeTap binding exists.
	ModeLongPress Mode = "long_press"
	// ModeSwitch toggles the action on/off each time the chord is pressed.
	ModeSwitch Mode = "switch"
	// ModeContextSwitch switches the engine into TargetContext when pressed.
	ModeContextSwitch Mode = "context_switch"
)

// DefaultContext is the context every engine starts in.
const DefaultContext = "default"

// Binding binds one key chord to one action within one context.
type Binding struct {
	// ID is the unique, stable identifier used in tie-breaking and logs.
	ID string `json:"id"`
	// Context limits where the binding is active. Empty means DefaultContext.
	Context string `json:"context,omitempty"`
	// Chord is the set of keys that must be held together (order-insensitive).
	Chord []device.Key `json:"chord"`
	// Mode selects hold/tap/long_press/switch/context_switch behaviour.
	Mode Mode `json:"mode"`
	// Action is the game-facing action name emitted for hold/tap/long/switch.
	Action string `json:"action,omitempty"`
	// TargetContext is the destination of a context_switch binding.
	TargetContext string `json:"target_context,omitempty"`
	// Priority resolves overlapping chords: larger value wins. Ties break in
	// favour of the longer (more specific) chord, then the smaller binding ID.
	Priority int `json:"priority,omitempty"`
	// LongPressMs overrides Config.LongPressMs for this binding.
	LongPressMs int64 `json:"long_press_ms,omitempty"`
}

// Config is the full saved binding set.
type Config struct {
	// DefaultContext overrides config.DefaultContext when non-empty.
	StartContext string `json:"start_context,omitempty"`
	// LongPressMs is the global hold threshold in milliseconds (default 500).
	LongPressMs int64     `json:"long_press_ms,omitempty"`
	Bindings    []Binding `json:"bindings"`
}

// Compiled is a validated, deterministically ordered configuration.
type Compiled struct {
	Start       string
	LongPressMs int64
	byContext   map[string][]Binding
}

type chordKey string

// Compile validates the config and precomputes per-context priority order.
func Compile(cfg Config) (*Compiled, error) {
	if len(cfg.Bindings) == 0 {
		return nil, errors.New("config: at least one binding is required")
	}
	start := cfg.StartContext
	if start == "" {
		start = DefaultContext
	}
	threshold := cfg.LongPressMs
	if threshold <= 0 {
		threshold = 500
	}

	c := &Compiled{
		Start:       start,
		LongPressMs: threshold,
		byContext:   make(map[string][]Binding),
	}
	seenIDs := make(map[string]struct{})
	for _, b := range cfg.Bindings {
		b = normalize(b, threshold)
		if err := validate(b, start); err != nil {
			return nil, err
		}
		if _, dup := seenIDs[b.ID]; dup {
			return nil, fmt.Errorf("config: duplicate binding id %q", b.ID)
		}
		seenIDs[b.ID] = struct{}{}
		c.byContext[b.Context] = append(c.byContext[b.Context], b)
	}
	for ctx, bindings := range c.byContext {
		// Drop bindings made redundant by an identical chord+mode that wins on
		// explicit priority, recording the survivor deterministically.
		winners := make(map[chordKey]Binding)
		for _, b := range bindings {
			key := chordKey(string(b.Mode) + "|" + string(chordIdentity(b.Chord)))
			if prev, ok := winners[key]; ok {
				if bindingLess(b, prev) {
					continue
				}
			}
			winners[key] = b
		}
		ordered := make([]Binding, 0, len(winners))
		for _, b := range winners {
			ordered = append(ordered, b)
		}
		sort.SliceStable(ordered, func(i, j int) bool { return bindingLess(ordered[i], ordered[j]) })
		c.byContext[ctx] = ordered
	}
	return c, nil
}

// Bindings returns the priority-sorted bindings for a context (may be empty).
func (c *Compiled) Bindings(context string) []Binding {
	return c.byContext[context]
}

func normalize(b Binding, threshold int64) Binding {
	if b.Context == "" {
		b.Context = DefaultContext
	}
	keys := append([]device.Key(nil), b.Chord...)
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	b.Chord = keys
	if b.LongPressMs <= 0 {
		b.LongPressMs = threshold
	}
	return b
}

func validate(b Binding, start string) error {
	switch {
	case b.ID == "":
		return errors.New("config: binding id is required")
	case len(b.Chord) == 0:
		return fmt.Errorf("config: binding %q has an empty chord", b.ID)
	case b.Mode == "":
		return fmt.Errorf("config: binding %q has no mode", b.ID)
	}
	switch b.Mode {
	case ModeHold, ModeTap, ModeLongPress, ModeSwitch:
		if b.Action == "" {
			return fmt.Errorf("config: binding %q requires an action", b.ID)
		}
	case ModeContextSwitch:
		if b.TargetContext == "" {
			return fmt.Errorf("config: context_switch binding %q requires target_context", b.ID)
		}
		if b.TargetContext == b.Context && b.Context == start {
			return fmt.Errorf("config: binding %q switches to its own context", b.ID)
		}
	default:
		return fmt.Errorf("config: binding %q has unknown mode %q", b.ID, b.Mode)
	}
	seen := make(map[device.Key]struct{}, len(b.Chord))
	for _, key := range b.Chord {
		if key == "" {
			return fmt.Errorf("config: binding %q has an empty key", b.ID)
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("config: binding %q lists key %q twice", b.ID, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// bindingLess implements the documented total priority order.
func bindingLess(a, z Binding) bool {
	if a.Priority != z.Priority {
		return a.Priority > z.Priority
	}
	if len(a.Chord) != len(z.Chord) {
		return len(a.Chord) > len(z.Chord)
	}
	return a.ID < z.ID
}

func chordIdentity(chord []device.Key) chordKey {
	out := ""
	for i, key := range chord {
		if i > 0 {
			out += "+"
		}
		out += string(key)
	}
	return chordKey(out)
}
