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
			outs := eng.ApplyEdge(edge, s.t)
			out = append(out, outs...)
			for _, o := range outs {
				// A context switch ends the input generation: the physical
				// keyboard must be reset as well so stranded releases become
				// stale instead of acting in the new context.
				if o.Type == engine.ContextSwitched {
					for _, forced := range kbd.Reset() {
						eng.ApplyEdge(forced, s.t) // forced edges are no-ops
					}
				}
			}
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

// Losing input focus must not leave any action active: holds deactivate and
// latched switches close and reset. A tap interrupted by blur never pulses,
// and a stale physical release arriving afterwards is ignored.
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
	feed(5, device.Event{Key: "L", Phase: device.Press})
	feed(6, device.Event{Key: "L", Phase: device.Release}) // switch latched on
	feed(10, device.Event{Key: "Ctrl", Phase: device.Press})
	feed(20, device.Event{Key: "Q", Phase: device.Press})
	feed(25, device.Event{Key: "Space", Phase: device.Press}) // interrupted tap
	if !eng.Active("run") || !eng.Active("quit") || !eng.Active("flashlight") {
		t.Fatal("expected run, quit and flashlight active")
	}
	out := eng.Blur(30)
	for _, edge := range kbd.FocusChange(device.FocusEvent{Focused: false}) {
		out = append(out, eng.ApplyEdge(edge, 30)...) // forced edges emit nothing
	}
	got := actions(out)
	want := []string{"flashlight- ", "quit- ", "run- "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf(gotString(got), want)
	}
	if eng.Active("run") || eng.Active("quit") || eng.Active("flashlight") {
		t.Fatal("no action may remain active after blur")
	}
	// Stranded physical releases belong to the pre-blur epoch: ignored, and
	// the interrupted Space tap never pulses.
	feed(40, device.Event{Key: "Space", Phase: device.Release})
	feed(41, device.Event{Key: "Shift", Phase: device.Release})
	feed(42, device.Event{Key: "Q", Phase: device.Release})
	feed(43, device.Event{Key: "Ctrl", Phase: device.Release})
	// A fresh press after focus returns behaves normally exactly once.
	feed(50, device.Event{Key: "Space", Phase: device.Press})
	feed(60, device.Event{Key: "Space", Phase: device.Release})
	// Switch starts from off after blur: one press cycle latches it on again.
	feed(70, device.Event{Key: "L", Phase: device.Press})
	feed(80, device.Event{Key: "L", Phase: device.Release})
	if !eng.Active("flashlight") {
		t.Fatal("switch should latch on again from a clean off state")
	}
}

// Device removal follows the same contract as blur.
func TestDeviceRemovedResetsActions(t *testing.T) {
	cfg := compile(t, baseBindings())
	kbd := device.NewVirtualKeyboard()
	eng := engine.New(cfg)
	feed := func(tt int64, ev device.Event) {
		if edge, ok := kbd.Push(ev); ok {
			eng.ApplyEdge(edge, tt)
		}
	}
	feed(0, device.Event{Key: "Shift", Phase: device.Press})
	feed(10, device.Event{Key: "L", Phase: device.Press})
	feed(20, device.Event{Key: "L", Phase: device.Release})
	out := eng.DeviceRemoved(30)
	for _, edge := range kbd.Removed() {
		out = append(out, eng.ApplyEdge(edge, 30)...)
	}
	got := actions(out)
	want := []string{"flashlight- ", "run- "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf(gotString(got), want)
	}
	if eng.Active("run") || eng.Active("flashlight") {
		t.Fatal("device removal must leave no active action")
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

// Mixed sequence: chords, holds, long press and a switch coexist; blur and
// context switches run the unified teardown; stale releases from old input
// generations are ignored. Saving the config as JSON and replaying must give
// the exact same stream, with each release emitted once and no residual
// active action except those deliberately latched by the new generation.
func TestMixedTeardownAndReplayParity(t *testing.T) {
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

	run := func(cfg *config.Compiled) ([]string, *engine.Engine) {
		kbd := device.NewVirtualKeyboard()
		eng := engine.New(cfg)
		var out []engine.Event
		feed := func(tt int64, ev device.Event) {
			out = append(out, eng.Update(tt)...)
			if edge, ok := kbd.Push(ev); ok {
				outs := eng.ApplyEdge(edge, tt)
				out = append(out, outs...)
				for _, o := range outs {
					if o.Type == engine.ContextSwitched {
						for _, forced := range kbd.Reset() {
							eng.ApplyEdge(forced, tt)
						}
					}
				}
			}
		}
		press := func(tt int64, key device.Key) { feed(tt, device.Event{Key: key, Phase: device.Press}) }
		release := func(tt int64, key device.Key) { feed(tt, device.Event{Key: key, Phase: device.Release}) }

		press(0, "Shift") // run+
		press(10, "L")
		release(20, "L") // flashlight+
		press(30, "Ctrl")
		press(40, "Q") // quit+ chord
		press(50, "C")
		out = append(out, eng.Update(560)...) // crouch!

		out = append(out, eng.Blur(570)...)
		for _, forced := range kbd.FocusChange(device.FocusEvent{Focused: false}) {
			out = append(out, eng.ApplyEdge(forced, 570)...)
		}
		release(580, "Q") // stale, ignored
		release(581, "Ctrl")
		release(582, "C")
		release(583, "Shift")
		release(584, "L")

		press(600, "Space")
		release(610, "Space") // jump! new generation

		press(620, "Tab") // ctx default -> menu, keyboard reset
		release(650, "Space")
		release(651, "Tab") // stale releases ignored
		press(660, "P")
		release(670, "P") // pause!
		press(700, "Tab") // ctx menu -> default

		press(730, "L")
		release(740, "L") // flashlight latches on from clean state
		return actions(out), eng
	}

	r1, e1 := run(c1)
	r2, e2 := run(c2)
	want := []string{
		"run+ ", "flashlight+ ", "quit+ ", "crouch!",
		"flashlight- ", "quit- ", "run- ",
		"jump!",
		"ctx:default->menu", "pause!", "ctx:menu->default",
		"flashlight+ ",
	}
	if !reflect.DeepEqual(r1, want) {
		t.Fatalf("stream: %s", gotString(r1))
	}
	if !reflect.DeepEqual(r1, r2) {
		t.Fatalf("saved-config replay differs: %v vs %v", r1, r2)
	}
	// Exactly one release per teardown, no residual holds; only the switch
	// deliberately re-latched by the final generation stays active.
	if e1.Active("run") || e1.Active("quit") || !e1.Active("flashlight") {
		t.Fatal("unexpected residual active state after replay")
	}
	if e2.Active("run") || e2.Active("quit") || !e2.Active("flashlight") {
		t.Fatal("saved-config replay leaves different active state")
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

func gotString(got []string) string {
	return "got " + reflectStr(got)
}

func reflectStr(v []string) string {
	b, _ := json.Marshal(v)
	return string(b)
}
