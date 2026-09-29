package input

import (
	"reflect"
	"testing"
)

func testConfig() Config {
	return Config{
		Contexts: []ContextID{"menu"},
		Bindings: []Binding{
			{ID: "jump", Action: "jump", Keys: []Button{"Space"}, Trigger: TriggerTap},
			{ID: "run", Action: "run", Keys: []Button{"Shift"}, Trigger: TriggerHold, LongPressMS: 300},
			{ID: "pause", Action: "pause", Keys: []Button{"P"}, Trigger: TriggerToggle},
			{ID: "attack", Action: "attack", Keys: []Button{"Ctrl", "X"}, Trigger: TriggerTap, Priority: 10},
			{ID: "x", Action: "secondary", Keys: []Button{"X"}, Trigger: TriggerTap, Priority: 1},
			{ID: "open-menu", Action: "open-menu", Keys: []Button{"Tab"}, Trigger: TriggerContext, Target: "menu"},
			{ID: "close-menu", Action: "close-menu", Keys: []Button{"Tab"}, Context: "menu", Trigger: TriggerContext, Target: "default"},
			{ID: "menu-run", Action: "run", Keys: []Button{"Shift"}, Context: "menu", Trigger: TriggerHold, LongPressMS: 100},
		},
	}
}

func TestComboWinsByExplicitPriorityAndSize(t *testing.T) {
	engine, err := NewEngine(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	result, err := engine.Replay([]InputEvent{
		{Seq: 1, TimestampMS: 0, Kind: ButtonDown, Button: "Ctrl"},
		{Seq: 2, TimestampMS: 1, Kind: ButtonDown, Button: "X"},
		{Seq: 3, TimestampMS: 2, Kind: ButtonUp, Button: "X"},
		{Seq: 4, TimestampMS: 3, Kind: ButtonUp, Button: "Ctrl"},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []ActionEvent{
		{Seq: 2, TimestampMS: 1, BindingID: "attack", Action: "attack", Context: "default", Phase: ActionPressed},
		{Seq: 2, TimestampMS: 1, BindingID: "attack", Action: "attack", Context: "default", Phase: ActionReleased},
	}
	if !reflect.DeepEqual(result.Actions, want) {
		t.Fatalf("actions = %#v, want %#v", result.Actions, want)
	}
}

func TestDuplicateDownDoesNotTriggerAgain(t *testing.T) {
	engine, err := NewEngine(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	firstDown := InputEvent{Seq: 1, TimestampMS: 0, Kind: ButtonDown, Button: "Space"}
	first, err := engine.Process(firstDown)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Actions) != 2 {
		t.Fatalf("first tap = %d events, want 2", len(first.Actions))
	}
	repeated, err := engine.Process(firstDown)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeated.Actions) != 0 {
		t.Fatalf("duplicate tap emitted %d actions", len(repeated.Actions))
	}
}

func TestHoldFiresOnceAndReleases(t *testing.T) {
	engine, err := NewEngine(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Replay([]InputEvent{
		{Seq: 1, TimestampMS: 0, Kind: ButtonDown, Button: "Shift"},
		{Seq: 2, TimestampMS: 100, Kind: ButtonDown, Button: "Space"},
		{Seq: 3, TimestampMS: 200, Kind: ButtonUp, Button: "Space"},
		{Seq: 4, TimestampMS: 300, Kind: ButtonDown, Button: "Space"},
		{Seq: 5, TimestampMS: 350, Kind: ButtonUp, Button: "Space"},
		{Seq: 6, TimestampMS: 400, Kind: ButtonUp, Button: "Shift"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []ActionEvent{
		{Seq: 2, TimestampMS: 100, BindingID: "jump", Action: "jump", Context: "default", Phase: ActionPressed},
		{Seq: 2, TimestampMS: 100, BindingID: "jump", Action: "jump", Context: "default", Phase: ActionReleased},
		{Seq: 4, TimestampMS: 300, BindingID: "run", Action: "run", Context: "default", Phase: ActionPressed},
		{Seq: 4, TimestampMS: 300, BindingID: "jump", Action: "jump", Context: "default", Phase: ActionPressed},
		{Seq: 4, TimestampMS: 300, BindingID: "jump", Action: "jump", Context: "default", Phase: ActionReleased},
		{Seq: 6, TimestampMS: 400, BindingID: "run", Action: "run", Context: "default", Phase: ActionReleased},
	}
	if !reflect.DeepEqual(result.Actions, want) {
		t.Fatalf("actions = %#v, want %#v", result.Actions, want)
	}
}

func TestContextSwitchReleasesOldState(t *testing.T) {
	engine, err := NewEngine(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Replay([]InputEvent{
		{Seq: 1, TimestampMS: 0, Kind: ButtonDown, Button: "P"},
		{Seq: 2, TimestampMS: 10, Kind: ButtonDown, Button: "Tab"},
		{Seq: 3, TimestampMS: 10, Kind: ButtonUp, Button: "Tab"},
		{Seq: 4, TimestampMS: 20, Kind: ButtonDown, Button: "Shift"},
		{Seq: 5, TimestampMS: 120, Kind: ButtonUp, Button: "Shift"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if engine.Context() != "menu" {
		t.Fatalf("context = %q, want menu", engine.Context())
	}
	if engine.ActiveAction("pause") {
		t.Fatal("toggled action remained active after context switch")
	}
	wantActions := []ActionEvent{
		{Seq: 1, TimestampMS: 0, BindingID: "pause", Action: "pause", Context: "default", Phase: ActionPressed},
		{Seq: 2, TimestampMS: 10, BindingID: "open-menu", Action: "open-menu", Context: "default", Phase: ActionPressed},
		{Seq: 2, TimestampMS: 10, BindingID: "open-menu", Action: "open-menu", Context: "default", Phase: ActionReleased},
		{Seq: 2, TimestampMS: 10, BindingID: "pause", Action: "pause", Context: "default", Phase: ActionReleased},
		{Seq: 5, TimestampMS: 120, BindingID: "menu-run", Action: "run", Context: "menu", Phase: ActionPressed},
		{Seq: 5, TimestampMS: 120, BindingID: "menu-run", Action: "run", Context: "menu", Phase: ActionReleased},
	}
	if !reflect.DeepEqual(result.Actions, wantActions) {
		t.Fatalf("actions = %#v, want %#v", result.Actions, wantActions)
	}
}

func TestFocusLostReleasesHoldsAndToggles(t *testing.T) {
	engine, err := NewEngine(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Replay([]InputEvent{
		{Seq: 1, TimestampMS: 0, Kind: ButtonDown, Button: "P"},
		{Seq: 2, TimestampMS: 0, Kind: ButtonDown, Button: "Shift"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Replay([]InputEvent{
		{Seq: 3, TimestampMS: 300, Kind: FocusLost},
	})
	if err != nil {
		t.Fatal(err)
	}
	if engine.ActiveAction("pause") || engine.ActiveAction("run") {
		t.Fatal("focus loss left an action active")
	}
	want := []ActionEvent{
		{Seq: 3, TimestampMS: 300, BindingID: "pause", Action: "pause", Context: "default", Phase: ActionReleased},
	}
	if !reflect.DeepEqual(result.Actions, want) {
		t.Fatalf("actions = %#v, want %#v", result.Actions, want)
	}
}

func TestInterleavedToggleTapAndReplayIsDeterministic(t *testing.T) {
	events := []InputEvent{
		{Seq: 1, TimestampMS: 0, Kind: ButtonDown, Button: "P"},
		{Seq: 2, TimestampMS: 1, Kind: ButtonDown, Button: "Space"},
		{Seq: 3, TimestampMS: 2, Kind: ButtonUp, Button: "Space"},
		{Seq: 4, TimestampMS: 3, Kind: ButtonUp, Button: "P"},
		{Seq: 5, TimestampMS: 4, Kind: ButtonDown, Button: "P"},
		{Seq: 6, TimestampMS: 5, Kind: ButtonDown, Button: "Space"},
		{Seq: 7, TimestampMS: 6, Kind: ButtonUp, Button: "Space"},
		{Seq: 8, TimestampMS: 7, Kind: ButtonUp, Button: "P"},
	}
	first, err := NewEngine(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	firstResult, err := first.Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewEngine(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := second.Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstResult, secondResult) {
		t.Fatalf("replays differ: %#v vs %#v", firstResult, secondResult)
	}
	wantPhases := []ActionPhase{ActionPressed, ActionPressed, ActionReleased, ActionReleased, ActionPressed, ActionReleased}
	if len(firstResult.Actions) != len(wantPhases) {
		t.Fatalf("got %d actions, want %d: %#v", len(firstResult.Actions), len(wantPhases), firstResult.Actions)
	}
	for i, phase := range wantPhases {
		if firstResult.Actions[i].Phase != phase {
			t.Fatalf("action %d phase = %q, want %q", i, firstResult.Actions[i].Phase, phase)
		}
	}
}

func TestSavedConfigReplayMatches(t *testing.T) {
	config := testConfig()
	path := t.TempDir() + "/config.json"
	saved, err := SaveConfig(path, config)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	events := []InputEvent{
		{Seq: 1, TimestampMS: 0, Kind: ButtonDown, Button: "Ctrl"},
		{Seq: 2, TimestampMS: 0, Kind: ButtonDown, Button: "X"},
		{Seq: 3, TimestampMS: 1, Kind: ButtonUp, Button: "X"},
		{Seq: 4, TimestampMS: 1, Kind: ButtonUp, Button: "Ctrl"},
	}
	savedEngine, err := NewEngine(saved)
	if err != nil {
		t.Fatal(err)
	}
	loadedEngine, err := NewEngine(loaded)
	if err != nil {
		t.Fatal(err)
	}
	savedResult, err := savedEngine.Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	loadedResult, err := loadedEngine.Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(savedResult, loadedResult) {
		t.Fatalf("saved/loaded results differ: %#v vs %#v", savedResult, loadedResult)
	}
}
