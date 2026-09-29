package config_test

import (
	"strings"
	"testing"

	"gameinput/config"
	"gameinput/device"
)

func TestRejectsInvalidBindings(t *testing.T) {
	cases := []config.Config{
		{}, // no bindings
		{Bindings: []config.Binding{{ID: "x", Chord: []device.Key{"A"}}}},                                 // no mode
		{Bindings: []config.Binding{{ID: "x", Chord: []device.Key{"A"}, Mode: config.ModeHold}}},          // no action
		{Bindings: []config.Binding{{ID: "x", Chord: []device.Key{"A"}, Mode: config.ModeContextSwitch}}}, // no target
		{Bindings: []config.Binding{
			{ID: "dup", Chord: []device.Key{"A"}, Mode: config.ModeTap, Action: "a"},
			{ID: "dup", Chord: []device.Key{"B"}, Mode: config.ModeTap, Action: "b"},
		}}, // duplicate ID
	}
	for i, cfg := range cases {
		if _, err := config.Compile(cfg); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
}

func TestPriorityOrdering(t *testing.T) {
	c, err := config.Compile(config.Config{Bindings: []config.Binding{
		{ID: "low", Chord: []device.Key{"A"}, Mode: config.ModeTap, Action: "a"},
		{ID: "high", Chord: []device.Key{"A"}, Mode: config.ModeHold, Action: "a", Priority: 5},
		{ID: "combo", Chord: []device.Key{"A", "B"}, Mode: config.ModeTap, Action: "ab"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := c.Bindings(config.DefaultContext)
	if got[0].ID != "high" || got[1].ID != "combo" || got[2].ID != "low" {
		var ids []string
		for _, b := range got {
			ids = append(ids, b.ID)
		}
		t.Fatalf("order = %s, want high,combo,low", strings.Join(ids, ","))
	}
}
