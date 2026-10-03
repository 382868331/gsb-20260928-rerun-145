package engine_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"gameinput/config"
	"gameinput/device"
	"gameinput/engine"
)

type step struct {
	t  int64
	ev device.Event
}

func replay(t *testing.T, cfg *config.Compiled, steps []step) []engine.Event {
	t.Helper()
	kbd := device.NewVirtualKeyboard()
	eng := engine.New(cfg)
	var out []engine.Event
	last := int64(0)
	for _, s := range steps {
		if s.t > last {
			out = append(out, eng.Update(s.t)...)
		}
		last = s.t
		edge, ok := kbd.Push(s.ev)
		if ok {
			out = append(out, eng.ApplyEdge(edge, s.t)...)
		}
	}
	return out
}

func actions(evs []engine.Event) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		switch ev.Type {
		case engine.Activate:
			out = append(out, ev.Action+"+ ")
		case engine.Deactivate:
			if ev.Action != "" {
				out = append(out, ev.Action+"- ")
			}
		case engine.Pulse:
			out = append(out, ev.Action+"!")
		case engine.ContextSwitched:
			out = append(out, "ctx:"+ev.FromContext+"->"+ev.ToContext)
		}
	}
	return out
}

func down(seq int64, key device.Key) step {
	return step{t: seq * 10, ev: device.Event{Seq: seq, Key: key, Phase: device.Press}}
}

func up(seq int64, key device.Key) step {
	return step{t: seq * 10, ev: device.Event{Seq: seq, Key: key, Phase: device.Release}}
}

func baseBindings() []config.Binding {
	return []config.Binding{
		{ID: "jump", Chord: []device.Key{"Space"}, Mode: config.ModeTap, Action: "jump"},
		{ID: "run", Chord: []device.Key{"Shift"}, Mode: config.ModeHold, Action: "run"},
		{ID: "quit", Chord: []device.Key{"Ctrl", "Q"}, Mode: config.ModeHold, Action: "quit"},
		{ID: "menu", Chord: []device.Key{"Tab"}, Mode: config.ModeContextSwitch, TargetContext: "menu"},
		{ID: "back", Context: "menu", Chord: []device.Key{"Tab"}, Mode: config.ModeContextSwitch, TargetContext: config.DefaultContext},
		{ID: "pause", Context: "menu", Chord: []device.Key{"P"}, Mode: config.ModeTap, Action: "pause"},
		{ID: "crouch", Chord: []device.Key{"C"}, Mode: config.ModeLongPress, Action: "crouch"},
		{ID: "light", Chord: []device.Key{"L"}, Mode: config.ModeSwitch, Action: "flashlight"},
	}
}

func compile(t *testing.T, bindings []config.Binding) *config.Compiled {
	t.Helper()
	c, err := config.Compile(config.Config{LongPressMs: 500, Bindings: bindings})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return c
}

// Repeated raw messages must not trigger an action more than once.
func TestDuplicateMessages(t *testing.T) {
	cfg := compile(t, baseBindings())
	steps := []step{
		{t: 0, ev: device.Event{Seq: 1, Key: "Space", Phase: device.Press}},
		{t: 1, ev: device.Event{Seq: 2, Key: "Space", Phase: device.Press}},
		{t: 2, ev: device.Event{Seq: 3, Key: "Space", Phase: device.Press}},
		{t: 100, ev: device.Event{Seq: 4, Key: "Space", Phase: device.Release}},
		{t: 101, ev: device.Event{Seq: 5, Key: "Space", Phase: device.Release}},
	}
	got := actions(replay(t, cfg, steps))
	want := []string{"jump!"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// Chords require every member and only fire when complete; singles held while
// completing the chord must not fire on their own.
func TestComboChord(t *testing.T) {
	cfg := compile(t, baseBindings())
	steps := []step{
		down(1, "Ctrl"),
		down(2, "Q"),
		up(3, "Q"),
		up(4, "Ctrl"),
	}
	got := actions(replay(t, cfg, steps))
	want := []string{"quit+ ", "quit- "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf(gotString(got), want)
	}
}

// Switching context releases every old hold/toggle; keys held through the
// switch must not trigger actions in the new context until re-pressed.
func TestContextSwitchReleasesOldState(t *testing.T) {
	cfg := compile(t, baseBindings())
	steps := []step{
		down(1, "Shift"), // run+
		down(2, "L"),     // flashlight+ (toggle on)
		down(3, "P"),     // held physically, no binding in default context
		down(4, "Tab"),   // run-, flashlight-, ctx->menu
	}
	steps = append(steps,
		up(5, "P"),               // stale release carried across the switch
		down(6, "P"), up(7, "P"), // fresh press after switch -> pause!
		up(8, "Shift"), up(9, "L"), up(10, "Tab"), // stale releases ignored
	)
	got := actions(replay(t, cfg, steps))
	want := []string{
		"run+ ", "flashlight+ ",
		"flashlight- ", "run- ",
		"ctx:default->menu",
		"pause!",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf(gotString(got), want)
	}
}

// Losing input focus must not leave any action permanently held.
func TestBlurReleasesEverything(t *testing.T) {
	cfg := compile(t, baseBindings())
	kbd := device.NewVirtualKeyboard()
	eng := engine.New(cfg)
	feed := func(tt int64, ev device.Event) {
		if edge, ok := kbd.Push(ev); ok {
			eng.ApplyEdge(edge, tt)
		}
	}
	feed(0, device.Event{Key: "Shift", Phase: device.Press})
	feed(10, device.Event{Key: "Ctrl", Phase: device.Press})
	feed(20, device.Event{Key: "Q", Phase: device.Press})
	if !eng.Active("run") || !eng.Active("quit") {
		t.Fatal("expected run and quit active")
	}
	var out []engine.Event
	for _, edge := range kbd.FocusChange(device.FocusEvent{Focused: false}) {
		out = append(out, eng.ApplyEdge(edge, 30)...)
	}
	got := actions(out)
	want := []string{"quit- ", "run- "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf(gotString(got), want)
	}
}

// Same saved config + same events => same actions (JSON round-trip included).
func TestSaveConfigDeterministicReplay(t *testing.T) {
	raw := config.Config{LongPressMs: 500, Bindings: baseBindings()}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var loaded config.Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	c1, _ := config.Compile(raw)
	c2, _ := config.Compile(loaded)
	seq := []step{
		down(1, "Space"), up(2, "Space"),
		{t: 20, ev: device.Event{Seq: 3, Key: "C", Phase: device.Press}},
		{t: 530, ev: device.Event{Seq: 4, Key: "C", Phase: device.Release}},
		{t: 540, ev: device.Event{Seq: 5, Key: "L", Phase: device.Press}},
		{t: 550, ev: device.Event{Seq: 6, Key: "L", Phase: device.Release}},
	}
	r1 := actions(replay(t, c1, seq))
	r2 := actions(replay(t, c2, seq))
	if !reflect.DeepEqual(r1, r2) {
		t.Fatalf("runs differ: %v vs %v", r1, r2)
	}
	want := []string{"jump!", "crouch!", "flashlight+ "}
	if !reflect.DeepEqual(r1, want) {
		t.Fatalf(gotString(r1), want)
	}
}

// Conflict: Space alone (priority 0) vs Ctrl+Space (higher priority). While
// Ctrl is held the chord wins; releasing Ctrl falls back to the single key
// without a duplicate trigger.
func TestConflictPriority(t *testing.T) {
	b := append(baseBindings(),
		config.Binding{ID: "special", Chord: []device.Key{"Ctrl", "Space"}, Mode: config.ModeTap, Action: "special", Priority: 10},
	)
	cfg := compile(t, b)
	steps := []step{
		down(1, "Ctrl"),
		down(2, "Space"),
		up(3, "Space"), // special fires (chord complete), jump suppressed
		up(4, "Ctrl"),
		down(5, "Space"),
		up(6, "Space"), // plain jump now
	}
	got := actions(replay(t, cfg, steps))
	want := []string{"special!", "jump!"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf(gotString(got), want)
	}
}

// Interleaved taps and toggles: presses/releases of other keys between taps,
// and toggle keys mixed in, must not lose or duplicate triggers.
func TestInterleavedTapsAndSwitches(t *testing.T) {
	cfg := compile(t, baseBindings())
	steps := []step{
		down(1, "Space"),         // jump down
		down(2, "L"), up(3, "L"), // toggle on, interleaved
		up(4, "Space"), // jump!
		down(5, "Space"),
		down(6, "Shift"), up(7, "Shift"), // unrelated hold pulse interleaved
		up(8, "Space"),            // jump!
		down(9, "L"), up(10, "L"), // toggle off
	}
	got := actions(replay(t, cfg, steps))
	want := []string{
		"flashlight+ ",
		"jump!",
		"run+ ", "run- ",
		"jump!",
		"flashlight- ",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf(gotString(got), want)
	}
}

// Ties on explicit priority resolve by chord length, then binding ID.
func TestTieBreakDeterministic(t *testing.T) {
	b := []config.Binding{
		{ID: "b-long", Chord: []device.Key{"A", "B"}, Mode: config.ModeTap, Action: "long_chord"},
		{ID: "a-single", Chord: []device.Key{"A"}, Mode: config.ModeTap, Action: "single"},
		{ID: "b-single", Chord: []device.Key{"B"}, Mode: config.ModeTap, Action: "single_b"},
	}
	cfg := compile(t, b)
	steps := []step{down(1, "A"), down(2, "B"), up(3, "B"), up(4, "A")}
	got := actions(replay(t, cfg, steps))
	want := []string{"long_chord!"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf(gotString(got), want)
	}
}

// Mixed chords, long presses and switches across focus loss, device removal
// and context restore: teardown resets holds and latches exactly once,
// stale releases never cancel new-round actions, nothing stays active, and
// a saved (JSON round-tripped) config replays the identical stream.
func TestMixedTeardownAndReplayConsistency(t *testing.T) {
	raw := config.Config{LongPressMs: 500, Bindings: baseBindings()}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var loaded config.Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	c1, err := config.Compile(raw)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := config.Compile(loaded)
	if err != nil {
		t.Fatal(err)
	}

	run := func(cfg *config.Compiled) ([]engine.Event, *engine.Engine) {
		kbd := device.NewVirtualKeyboard()
		eng := engine.New(cfg)
		var out []engine.Event
		feed := func(tt int64, key device.Key, ph device.Phase) {
			if edge, ok := kbd.Push(device.Event{Key: key, Phase: ph}); ok {
				out = append(out, eng.ApplyEdge(edge, tt)...)
			}
		}
		press := func(tt int64, key device.Key) { feed(tt, key, device.Press) }
		release := func(tt int64, key device.Key) { feed(tt, key, device.Release) }

		press(0, "Shift")                     // run+
		press(10, "L")                        // flashlight+ (latch on)
		press(20, "C")                        // long-press pending
		out = append(out, eng.Blur(30)...)    // teardown: flashlight-, run-
		out = append(out, eng.Update(530)...) // cancelled long press must not fire
		release(40, "Shift")                  // stale releases from before the blur
		release(50, "L")
		release(60, "C")
		press(70, "Shift") // new round: run+ again
		press(80, "Ctrl")
		press(90, "Q") // chord: quit+
		release(100, "Q")
		release(110, "Ctrl")
		press(120, "L")
		release(130, "L")                                                  // latch on again
		out = append(out, eng.DeviceRemoved(device.DefaultSource, 140)...) // flashlight-, run-
		release(150, "Shift")                                              // stale release from the removed device
		press(160, "C")
		out = append(out, eng.Update(700)...) // crouch! after 540ms
		release(710, "C")
		press(720, "Tab")   // ctx default->menu
		release(725, "Tab") // stale release swallowed by the engine
		press(730, "P")
		release(740, "P")   // pause!
		press(750, "Tab")   // ctx menu->default (context restore)
		release(760, "Tab") // stale release carried across the switches
		press(770, "Space")
		release(780, "Space") // jump!
		return out, eng
	}

	want := []string{
		"run+ ", "flashlight+ ",
		"flashlight- ", "run- ", // blur teardown
		"run+ ",
		"quit+ ", "quit- ",
		"flashlight+ ",
		"flashlight- ", "run- ", // device removal teardown
		"crouch!",
		"ctx:default->menu",
		"pause!",
		"ctx:menu->default",
		"jump!",
	}
	for i, cfg := range []*config.Compiled{c1, c2} {
		out, eng := run(cfg)
		got := actions(out)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: got %v want %v", i, got, want)
		}
		for _, action := range []string{"run", "quit", "flashlight"} {
			if eng.Active(action) {
				t.Fatalf("run %d: %q still active after teardowns", i, action)
			}
		}
		// No duplicate releases: every Deactivate matches a distinct Activate.
		balance := make(map[string]int)
		for _, ev := range out {
			switch ev.Type {
			case engine.Activate:
				balance[ev.Action]++
			case engine.Deactivate:
				balance[ev.Action]--
				if balance[ev.Action] < 0 {
					t.Fatalf("run %d: duplicate release of %q", i, ev.Action)
				}
			}
		}
		for action, n := range balance {
			if n != 0 {
				t.Fatalf("run %d: %q has %d unbalanced activations", i, action, n)
			}
		}
	}
}

func gotString(got []string) string {
	return "got " + reflectStr(got)
}

func reflectStr(v []string) string {
	b, _ := json.Marshal(v)
	return string(b)
}
