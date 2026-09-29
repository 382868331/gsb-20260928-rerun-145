package input

import "fmt"

type Device interface {
	ID() string
}

type VirtualKeyboard struct {
	id    string
	state map[Button]bool
}

func NewVirtualKeyboard(id string) *VirtualKeyboard {
	return &VirtualKeyboard{id: id, state: make(map[Button]bool)}
}

func (keyboard *VirtualKeyboard) ID() string {
	return keyboard.id
}

func (keyboard *VirtualKeyboard) State(button Button) bool {
	return keyboard.state[button]
}

func (keyboard *VirtualKeyboard) Down(seq, timestampMS int64, button Button) InputEvent {
	keyboard.state[button] = true
	return InputEvent{Seq: seq, TimestampMS: timestampMS, DeviceID: keyboard.id, Kind: ButtonDown, Button: button}
}

func (keyboard *VirtualKeyboard) Up(seq, timestampMS int64, button Button) InputEvent {
	delete(keyboard.state, button)
	return InputEvent{Seq: seq, TimestampMS: timestampMS, DeviceID: keyboard.id, Kind: ButtonUp, Button: button}
}

func (keyboard *VirtualKeyboard) FocusLost(seq, timestampMS int64) InputEvent {
	clear(keyboard.state)
	return InputEvent{Seq: seq, TimestampMS: timestampMS, DeviceID: keyboard.id, Kind: FocusLost}
}

func (keyboard *VirtualKeyboard) Replay(events []InputEvent) error {
	for _, event := range events {
		if event.DeviceID != keyboard.id {
			return fmt.Errorf("event device %q does not match keyboard %q", event.DeviceID, keyboard.id)
		}
		switch event.Kind {
		case ButtonDown:
			keyboard.state[event.Button] = true
		case ButtonUp:
			delete(keyboard.state, event.Button)
		case FocusLost:
			clear(keyboard.state)
		}
	}
	return nil
}
