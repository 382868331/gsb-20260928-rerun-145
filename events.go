package input

type EventKind int

const (
	ButtonDown EventKind = iota
	ButtonUp
	FocusLost
)

func (kind EventKind) String() string {
	switch kind {
	case ButtonDown:
		return "down"
	case ButtonUp:
		return "up"
	case FocusLost:
		return "focus_lost"
	default:
		return "unknown"
	}
}

type InputEvent struct {
	Seq         int64     `json:"seq"`
	TimestampMS int64     `json:"timestamp_ms"`
	DeviceID    string    `json:"device_id"`
	Kind        EventKind `json:"kind"`
	Button      Button    `json:"button,omitempty"`
}

type ActionPhase string

const (
	ActionPressed  ActionPhase = "pressed"
	ActionReleased ActionPhase = "released"
)

type ActionEvent struct {
	Seq         int64       `json:"seq"`
	TimestampMS int64       `json:"timestamp_ms"`
	BindingID   string      `json:"binding_id"`
	Action      ActionID    `json:"action"`
	Context     ContextID   `json:"context"`
	Phase       ActionPhase `json:"phase"`
}

type ContextEvent struct {
	Seq         int64     `json:"seq"`
	TimestampMS int64     `json:"timestamp_ms"`
	From        ContextID `json:"from"`
	To          ContextID `json:"to"`
}

type Result struct {
	Actions  []ActionEvent  `json:"actions"`
	Contexts []ContextEvent `json:"contexts"`
}

func (result *Result) appendAction(seq int64, timestampMS int64, binding Binding, phase ActionPhase) {
	result.Actions = append(result.Actions, ActionEvent{
		Seq:         seq,
		TimestampMS: timestampMS,
		BindingID:   binding.ID,
		Action:      binding.Action,
		Context:     binding.Context,
		Phase:       phase,
	})
}

func (result *Result) appendContext(seq int64, timestampMS int64, from, to ContextID) {
	result.Contexts = append(result.Contexts, ContextEvent{
		Seq:         seq,
		TimestampMS: timestampMS,
		From:        from,
		To:          to,
	})
}

func (result *Result) merge(other Result) {
	result.Actions = append(result.Actions, other.Actions...)
	result.Contexts = append(result.Contexts, other.Contexts...)
}
