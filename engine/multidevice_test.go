package engine_test

import (
	"reflect"
	"testing"

	"gameinput/config"
	"gameinput/device"
	"gameinput/engine"
)

func holdBindings() []config.Binding {
	return []config.Binding{
		{ID: "fire", Chord: []device.Key{"A"}, Mode: config.ModeHold, Action: "fire"},
		{ID: "aim", Chord: []device.Key{"B"}, Mode: config.ModeSwitch, Action: "aim"},
		{ID: "jump", Chord: []device.Key{"X"}, Mode: config.ModeTap, Action: "jump"},
		{ID: "menu", Chord: []device.Key{"Tab"}, Mode: config.ModeContextSwitch, TargetContext: "menu"},
		{ID: "back", Context: "menu", Chord: []device.Key{"Tab"}, Mode: config.ModeContextSwitch, TargetContext: config.DefaultContext},
		{ID: "ok", Context: "menu", Chord: []device.Key{"A"}, Mode: config.ModeTap, Action: "ok"},
	}
}

// Two devices holding the same action jointly: releasing one device must not
// end the action while the other device still sustains it. Counts start once
// and end once, regardless of interleaving.
func TestMultiDeviceSharedHold(t *testing.T) {
	cfg := compile(t, holdBindings())
	kbd := device.NewVirtualKeyboard("kbd")
	pad := device.NewVirtualController("pad")
	eng := engine.New(cfg)
	var out []engine.Event
	feed := func(dev interface {
		Push(device.Event) (device.Edge, bool)
	}, key device.Key, ph device.Phase, tt int64) {
		if edge, ok := dev.Push(device.Event{Device: "", Key: key, Phase: ph}); ok {
			out = append(out, eng.ApplyEdge(edge, tt)...)
		}
	}

	feed(kbd, "A", device.Press, 0)  // fire+
	feed(pad, "A", device.Press, 10) // pad joins: no second Activate
	feed(kbd, "A", device.Release, 20)
	// keyboard let go while the controller still holds: fire stays active.
	if !eng.Active("fire") {
		t.Fatal("fire must stay active while the controller holds it")
	}
	feed(pad, "A", device.Release, 30) // fire-

	got := actions(out)
	want := []string{"fire+ ", "fire- "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// A late release attributed to the old round must not cancel an action a
// fresh press of the same key on a later round just started.
func TestLateReleaseBelongsToOldRound(t *testing.T) {
	cfg := compile(t, holdBindings())
	eng := engine.New(cfg)
	var out []engine.Event

	out = append(out, eng.ApplyEdge(device.Edge{Device: "kbd", Key: "A", Phase: device.Press}, 0)...)
	out = append(out, eng.Blur(10)...) // fire-; A physically still down, lingers
	// The keyboard's old release finally arrives, then a brand-new press
	// starts the next round. The stale release cannot reach the new action.
	out = append(out, eng.ApplyEdge(device.Edge{Device: "kbd", Key: "A", Phase: device.Release}, 20)...)
	out = append(out, eng.ApplyEdge(device.Edge{Device: "kbd", Key: "A", Phase: device.Press}, 30)...)
	if !eng.Active("fire") {
		t.Fatal("new-round fire must be active; stale release killed it?")
	}
	out = append(out, eng.ApplyEdge(device.Edge{Device: "kbd", Key: "A", Phase: device.Release}, 40)...)

	got := actions(out)
	want := []string{"fire+ ", "fire- ", "fire+ ", "fire- "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// Removing a device tears down only its own holdings; a later release with
// that device id is a dead-session message. Re-attaching opens a fresh
// session that must not inherit the old held state. Interleaved presses and
// late releases across context restore are counted exactly.
func TestDeviceRemovedAndReconnected(t *testing.T) {
	cfg := compile(t, holdBindings())
	kbd := device.NewVirtualKeyboard("kbd")
	pad := device.NewVirtualController("pad")
	eng := engine.New(cfg)
	var out []engine.Event
	kfeed := func(key device.Key, ph device.Phase, tt int64) {
		if edge, ok := kbd.Push(device.Event{Key: key, Phase: ph}); ok {
			out = append(out, eng.ApplyEdge(edge, tt)...)
		}
	}
	pfeed := func(key device.Key, ph device.Phase, tt int64) {
		if edge, ok := pad.Push(device.Event{Key: key, Phase: ph}); ok {
			out = append(out, eng.ApplyEdge(edge, tt)...)
		}
	}

	kfeed("A", device.Press, 0) // fire+ on keyboard
	pfeed("A", device.Press, 10)
	out = append(out, eng.DeviceRemoved("kbd", 20)...) // controller keeps fire alive
	if !eng.Active("fire") {
		t.Fatal("fire survives keyboard removal because the pad holds it")
	}
	// The unplugged keyboard's late release is a dead-session message.
	out = append(out, eng.ApplyEdge(device.Edge{Device: "kbd", Key: "A", Phase: device.Release}, 30)...)
	if !eng.Active("fire") {
		t.Fatal("dead-session release must not cancel the pad-held action")
	}
	// Replug the keyboard (new session) and press A: it simply joins; no old
	// held state is carried onto the new device.
	eng.AttachDevice("kbd")
	kbd = device.NewVirtualKeyboard("kbd")
	kfeed("A", device.Press, 40)
	pfeed("A", device.Release, 50) // pad lets go, keyboard still holds
	if !eng.Active("fire") {
		t.Fatal("reconnected keyboard now sustains fire")
	}
	kfeed("A", device.Release, 60)

	// Context restore with interleaved presses: stale cross-context releases
	// never cancel fresh actions, starts/ends stay balanced.
	kfeed("Tab", device.Press, 70)
	kfeed("A", device.Press, 80) // lingering across switch
	kfeed("Tab", device.Release, 75)
	out = append(out, eng.ApplyEdge(device.Edge{Device: "kbd", Key: "A", Phase: device.Release}, 90)...)
	kfeed("A", device.Press, 100)
	kfeed("A", device.Release, 110) // ok!
	kfeed("Tab", device.Press, 120)
	out = append(out, eng.ApplyEdge(device.Edge{Device: "kbd", Key: "Tab", Phase: device.Release}, 130)...)

	got := actions(out)
	want := []string{
		"fire+ ",
		"fire- ",
		"ctx:default->menu",
		"ok!",
		"ctx:menu->default",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// Saved state is resumable: after reopening, inputs that were held are
// reported as needing reconfirmation and start unreleased, so stale messages
// cannot drive the new round until the user presses them again.
func TestSnapshotRestoreReconfirm(t *testing.T) {
	cfg := compile(t, holdBindings())
	eng := engine.New(cfg)
	eng.ApplyEdge(device.Edge{Device: "kbd", Key: "A", Phase: device.Press}, 0)
	eng.ApplyEdge(device.Edge{Device: "pad", Key: "A", Phase: device.Press}, 1)
	eng.ApplyEdge(device.Edge{Device: "kbd", Key: "B", Phase: device.Press}, 2) // aim on
	eng.ApplyEdge(device.Edge{Device: "kbd", Key: "B", Phase: device.Release}, 3)

	state := eng.Snapshot()
	data, err := eng.MarshalState()
	if err != nil {
		t.Fatal(err)
	}

	restored := engine.New(cfg)
	if err := restored.UnmarshalState(data); err != nil {
		t.Fatal(err)
	}
	wantPending := []engine.PendingInput{
		{Device: "kbd", Key: "A"},
		{Device: "pad", Key: "A"},
	}
	if got := restored.PendingReconfirm(); !reflect.DeepEqual(got, wantPending) {
		t.Fatalf("pending = %v want %v", got, wantPending)
	}
	if !restored.Active("aim") {
		t.Fatal("latched switch state must survive the restore")
	}
	if restored.Active("fire") {
		t.Fatal("held actions must not be assumed active before reconfirmation")
	}

	// A late release from before the save is ignored; nothing is held yet.
	restored.ApplyEdge(device.Edge{Device: "kbd", Key: "A", Phase: device.Release}, 10)
	// A fresh press reconfirms the input and starts the action.
	restored.ApplyEdge(device.Edge{Device: "kbd", Key: "A", Phase: device.Press}, 20)
	if !restored.Active("fire") {
		t.Fatal("fire starts only after a fresh press")
	}
	if got := restored.PendingReconfirm(); !reflect.DeepEqual(got, []engine.PendingInput{{Device: "pad", Key: "A"}}) {
		t.Fatalf("pending after reconfirm = %v", got)
	}

	if !reflect.DeepEqual(restored.Snapshot().Context, state.Context) {
		t.Fatal("context did not survive the restore")
	}
}
