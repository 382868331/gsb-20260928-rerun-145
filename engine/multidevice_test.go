package engine_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"gameinput/config"
	"gameinput/device"
	"gameinput/engine"
)

// multiSetup compiles a config and creates named virtual devices feeding
// edges into the engine.
func multiSetup(t *testing.T, bindings []config.Binding) (*engine.Engine, map[device.Source]*device.VirtualKeyboard) {
	t.Helper()
	cfg, err := config.Compile(config.Config{LongPressMs: 500, Bindings: bindings})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	devs := map[device.Source]*device.VirtualKeyboard{
		"kbd": device.NewVirtualKeyboard("kbd"),
		"pad": device.NewVirtualKeyboard("pad"),
	}
	return engine.New(cfg), devs
}

func feed(eng *engine.Engine, devs map[device.Source]*device.VirtualKeyboard, src device.Source, tMs int64, key device.Key, ph device.Phase) []engine.Event {
	edge, ok := devs[src].Push(device.Event{Key: key, Phase: ph})
	if !ok {
		return nil
	}
	return eng.ApplyEdge(edge, tMs)
}

func countType(evs []engine.Event, typ engine.EventType) int {
	n := 0
	for _, ev := range evs {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// Keyboard and controller co-drive the same hold action: engaging from both
// devices activates once, and releasing one device must not end the action
// while the other still holds it.
func TestMultiDeviceCoDriveSameAction(t *testing.T) {
	eng, devs := multiSetup(t, []config.Binding{
		{ID: "run", Chord: []device.Key{"Shift"}, Mode: config.ModeHold, Action: "run"},
	})
	var all []engine.Event
	all = append(all, feed(eng, devs, "kbd", 0, "Shift", device.Press)...)
	all = append(all, feed(eng, devs, "pad", 10, "Shift", device.Press)...)
	if got := countType(all, engine.Activate); got != 1 {
		t.Fatalf("second device must not re-activate, activates=%d", got)
	}
	if out := feed(eng, devs, "kbd", 20, "Shift", device.Release); len(out) != 0 {
		t.Fatalf("releasing one device must not end the action, got %v", out)
	}
	if !eng.Active("run") {
		t.Fatal("run must stay active while the controller still holds it")
	}
	all = append(all, feed(eng, devs, "pad", 30, "Shift", device.Release)...)
	if got := countType(all, engine.Deactivate); got != 1 {
		t.Fatalf("exactly one deactivate after the last device releases, got %d", got)
	}
	if eng.Active("run") {
		t.Fatal("run must be inactive after both devices released")
	}
}

// A release left over from before a blur only cancels the input it belongs
// to: it must not terminate an action re-engaged by another device.
func TestStaleReleaseAttributedToOldRound(t *testing.T) {
	eng, devs := multiSetup(t, []config.Binding{
		{ID: "run", Chord: []device.Key{"Shift"}, Mode: config.ModeHold, Action: "run"},
	})
	var all []engine.Event
	all = append(all, feed(eng, devs, "kbd", 0, "Shift", device.Press)...)  // run+
	all = append(all, eng.Blur(10)...)                                      // run-
	all = append(all, feed(eng, devs, "pad", 20, "Shift", device.Press)...) // run+ again
	if out := feed(eng, devs, "kbd", 30, "Shift", device.Release); len(out) != 0 {
		t.Fatalf("stale keyboard release must be swallowed, got %v", out)
	}
	if !eng.Active("run") {
		t.Fatal("late release from the old round must not cancel the new action")
	}
	all = append(all, feed(eng, devs, "pad", 40, "Shift", device.Release)...) // run-
	if got := countType(all, engine.Activate); got != 2 {
		t.Fatalf("activates=%d, want 2", got)
	}
	if got := countType(all, engine.Deactivate); got != 2 {
		t.Fatalf("deactivates=%d, want 2", got)
	}
}

// Interleaved presses across devices and a context switch: the switch
// releases old-context actions, stale releases from the switching device are
// swallowed per source, and actions work again after switching back.
func TestContextSwitchAcrossDevicesAndRestore(t *testing.T) {
	eng, devs := multiSetup(t, []config.Binding{
		{ID: "run", Chord: []device.Key{"Shift"}, Mode: config.ModeHold, Action: "run"},
		{ID: "to-menu", Chord: []device.Key{"Tab"}, Mode: config.ModeContextSwitch, TargetContext: "menu"},
		{ID: "back", Context: "menu", Chord: []device.Key{"Tab"}, Mode: config.ModeContextSwitch, TargetContext: config.DefaultContext},
		{ID: "pause", Context: "menu", Chord: []device.Key{"P"}, Mode: config.ModeTap, Action: "pause"},
	})
	var all []engine.Event
	all = append(all, feed(eng, devs, "kbd", 0, "Shift", device.Press)...) // run+
	all = append(all, feed(eng, devs, "kbd", 10, "Tab", device.Press)...)  // run-, ctx->menu
	if out := feed(eng, devs, "kbd", 20, "Tab", device.Release); len(out) != 0 {
		t.Fatalf("release of the switch key must be stale, got %v", out)
	}
	if out := feed(eng, devs, "kbd", 30, "Shift", device.Release); len(out) != 0 {
		t.Fatalf("Shift release carried across the switch must be stale, got %v", out)
	}
	// Controller drives the menu context while the keyboard is stale.
	all = append(all, feed(eng, devs, "pad", 40, "P", device.Press)...)
	all = append(all, feed(eng, devs, "pad", 50, "P", device.Release)...) // pause!
	// Restore the default context; the keyboard must re-sync cleanly.
	all = append(all, feed(eng, devs, "kbd", 60, "Tab", device.Press)...)     // ctx->default
	all = append(all, feed(eng, devs, "kbd", 70, "Tab", device.Release)...)   // stale
	all = append(all, feed(eng, devs, "kbd", 80, "Shift", device.Press)...)   // run+ again
	all = append(all, feed(eng, devs, "kbd", 90, "Shift", device.Release)...) // run-

	got := actions(all)
	want := []string{
		"run+ ",
		"run- ", "ctx:default->menu",
		"pause!",
		"ctx:menu->default",
		"run+ ", "run- ",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// Removing one device is scoped to that source: the action survives on the
// remaining device, late releases from the removed device are swallowed, and
// a reconnected device starts clean without inheriting old held state.
func TestDeviceRemovalScopedAndReconnect(t *testing.T) {
	eng, devs := multiSetup(t, []config.Binding{
		{ID: "run", Chord: []device.Key{"Shift"}, Mode: config.ModeHold, Action: "run"},
	})
	var all []engine.Event
	all = append(all, feed(eng, devs, "kbd", 0, "Shift", device.Press)...)
	all = append(all, feed(eng, devs, "pad", 10, "Shift", device.Press)...) // run+ once
	if out := eng.DeviceRemoved("kbd", 20); len(out) != 0 {
		t.Fatalf("removing the keyboard must not end an action the pad still drives, got %v", out)
	}
	if !eng.Active("run") {
		t.Fatal("run must survive keyboard removal while the pad holds it")
	}
	// A late release from the removed keyboard arrives after removal.
	if out := eng.ApplyEdge(device.Edge{Source: "kbd", Key: "Shift", Phase: device.Release, Round: 1}, 30); len(out) != 0 {
		t.Fatalf("late release from removed device must be swallowed, got %v", out)
	}
	// The keyboard reconnects: no held state carries over, a fresh press
	// re-syncs it, and it must not produce a duplicate activation.
	devs["kbd"] = device.NewVirtualKeyboardFromRound("kbd", eng.Round("kbd"))
	if out := feed(eng, devs, "kbd", 40, "Shift", device.Press); len(out) != 0 {
		t.Fatalf("re-joining an already-engaged action must not re-activate, got %v", out)
	}
	if out := feed(eng, devs, "kbd", 50, "Shift", device.Release); len(out) != 0 {
		t.Fatalf("pad still holds the action, got %v", out)
	}
	all = append(all, feed(eng, devs, "pad", 60, "Shift", device.Release)...) // run-
	// Both devices work normally in the next round.
	all = append(all, feed(eng, devs, "kbd", 70, "Shift", device.Press)...)   // run+
	all = append(all, feed(eng, devs, "kbd", 80, "Shift", device.Release)...) // run-
	if got := countType(all, engine.Activate); got != 2 {
		t.Fatalf("activates=%d, want 2", got)
	}
	if got := countType(all, engine.Deactivate); got != 2 {
		t.Fatalf("deactivates=%d, want 2", got)
	}
}

// A saved snapshot survives a JSON round-trip; after Restore the engine
// reports exactly which inputs still need re-confirmation, stale releases
// from before the save are swallowed, and latched switches come back on.
func TestSnapshotRestorePendingReconfirm(t *testing.T) {
	bindings := []config.Binding{
		{ID: "run", Chord: []device.Key{"Shift"}, Mode: config.ModeHold, Action: "run"},
		{ID: "light", Chord: []device.Key{"L"}, Mode: config.ModeSwitch, Action: "flashlight"},
	}
	eng, devs := multiSetup(t, bindings)
	feed(eng, devs, "kbd", 0, "Shift", device.Press)
	feed(eng, devs, "pad", 10, "Shift", device.Press)
	feed(eng, devs, "kbd", 20, "L", device.Press) // flashlight+ latched on

	snap := eng.Snapshot()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var loaded engine.Snapshot
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	// Restart with a fresh engine from the same config.
	cfg, err := config.Compile(config.Config{LongPressMs: 500, Bindings: bindings})
	if err != nil {
		t.Fatal(err)
	}
	eng2 := engine.New(cfg)
	out := eng2.Restore(loaded, 1000)
	if got := actions(out); !reflect.DeepEqual(got, []string{"flashlight+ "}) {
		t.Fatalf("restore must re-activate latched switches, got %v", got)
	}
	wantPending := []engine.InputRef{
		{Source: "kbd", Key: "L", Round: 2},
		{Source: "kbd", Key: "Shift", Round: 1},
		{Source: "pad", Key: "Shift", Round: 1},
	}
	if got := eng2.PendingReconfirm(); !reflect.DeepEqual(got, wantPending) {
		t.Fatalf("pending reconfirm = %v, want %v", got, wantPending)
	}
	// Releases left over from before the save are stale and swallowed.
	if out := eng2.ApplyEdge(device.Edge{Source: "kbd", Key: "Shift", Phase: device.Release, Round: 1}, 1010); len(out) != 0 {
		t.Fatalf("pre-save release must not cancel anything, got %v", out)
	}
	if out := eng2.ApplyEdge(device.Edge{Source: "pad", Key: "Shift", Phase: device.Release, Round: 1}, 1020); len(out) != 0 {
		t.Fatalf("pre-save release must not cancel anything, got %v", out)
	}
	// A fresh press re-confirms the input and starts a new round.
	if out := eng2.ApplyEdge(device.Edge{Source: "kbd", Key: "Shift", Phase: device.Press, Round: 3}, 1030); countType(out, engine.Activate) != 1 {
		t.Fatalf("re-confirmed press must activate, got %v", out)
	}
	if got := eng2.PendingReconfirm(); !reflect.DeepEqual(got, []engine.InputRef{
		{Source: "kbd", Key: "L", Round: 2},
	}) {
		t.Fatalf("pending after re-confirm = %v", got)
	}
	out = eng2.ApplyEdge(device.Edge{Source: "kbd", Key: "Shift", Phase: device.Release, Round: 3}, 1040)
	if countType(out, engine.Deactivate) != 1 {
		t.Fatalf("release of the re-confirmed key must deactivate, got %v", out)
	}
}

func TestBlurRepressThenOldReleaseOnlyEndsOldRound(t *testing.T) {
	eng, devs := multiSetup(t, []config.Binding{
		{ID: "run", Chord: []device.Key{"Shift"}, Mode: config.ModeHold, Action: "run"},
	})

	first := feed(eng, devs, "kbd", 0, "Shift", device.Press)
	devs["kbd"].Reset()
	blur := eng.Blur(10)
	second := feed(eng, devs, "kbd", 20, "Shift", device.Press)
	late := feed(eng, devs, "kbd", 30, "Shift", device.Release)
	current := feed(eng, devs, "kbd", 40, "Shift", device.Release)

	all := append(append(append(first, blur...), second...), append(late, current...)...)
	if got := countType(all, engine.Activate); got != 2 {
		t.Fatalf("activates=%d, want 2", got)
	}
	if got := countType(all, engine.Deactivate); got != 2 {
		t.Fatalf("deactivates=%d, want 2", got)
	}
	if got := eng.Holders(); len(got) != 0 {
		t.Fatalf("current release must clear its holder, got %v", got)
	}
}

func TestDeviceReconnectContinuesRoundsAndSaveRestoresCounter(t *testing.T) {
	bindings := []config.Binding{
		{ID: "run", Chord: []device.Key{"Shift"}, Mode: config.ModeHold, Action: "run"},
	}
	eng, devs := multiSetup(t, bindings)

	feed(eng, devs, "kbd", 0, "Shift", device.Press)
	feed(eng, devs, "pad", 10, "Shift", device.Press)
	eng.DeviceRemoved("kbd", 20)
	late := feed(eng, devs, "kbd", 30, "Shift", device.Release)
	if len(late) != 0 {
		t.Fatalf("late removed-device release changed actions: %v", late)
	}

	devs["kbd"] = device.NewVirtualKeyboardFromRound("kbd", eng.Round("kbd"))
	reconnected := feed(eng, devs, "kbd", 40, "Shift", device.Press)
	releaseKbd := feed(eng, devs, "kbd", 50, "Shift", device.Release)
	if len(reconnected) != 0 || len(releaseKbd) != 0 {
		t.Fatalf("pad co-hold must keep action active: reconnect=%v release=%v", reconnected, releaseKbd)
	}

	snap := eng.Snapshot()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var loaded engine.Snapshot
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	cfg := compile(t, bindings)
	restored := engine.New(cfg)
	restored.Restore(loaded, 60)

	devs["kbd"] = device.NewVirtualKeyboardFromRound("kbd", restored.Round("kbd"))
	fresh := feed(restored, devs, "kbd", 70, "Shift", device.Press)
	if countType(fresh, engine.Activate) != 1 {
		t.Fatalf("fresh restored-device press must start its own action, got %v", fresh)
	}
	ending := feed(restored, devs, "kbd", 80, "Shift", device.Release)
	if countType(ending, engine.Deactivate) != 1 {
		t.Fatalf("new round release must end its own action, got %v", ending)
	}
}
