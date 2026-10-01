package device_test

import (
	"reflect"
	"testing"

	"gameinput/device"
)

func TestDeduplicatesRepeatedMessages(t *testing.T) {
	k := device.NewVirtualKeyboard()
	presses := []device.Event{
		{Seq: 1, Key: "A", Phase: device.Press},
		{Seq: 2, Key: "A", Phase: device.Press},
		{Seq: 3, Key: "A", Phase: device.Press},
	}
	edge, ok := k.Push(presses[0])
	if !ok || edge.Key != "A" || edge.Phase != device.Press {
		t.Fatalf("first press should yield one edge, got %+v ok=%v", edge, ok)
	}
	for _, ev := range presses[1:] {
		if _, ok := k.Push(ev); ok {
			t.Fatal("repeated press must be swallowed")
		}
	}
	if !k.Held("A") {
		t.Fatal("A should be held")
	}
	if _, ok := k.Push(device.Event{Seq: 5, Key: "A", Phase: device.Release}); !ok {
		t.Fatal("first release should yield an edge")
	}
	if _, ok := k.Push(device.Event{Seq: 6, Key: "A", Phase: device.Release}); ok {
		t.Fatal("repeated release must be swallowed")
	}
}

func TestBlurReleasesHeldKeysInOrder(t *testing.T) {
	k := device.NewVirtualKeyboard()
	k.Push(device.Event{Seq: 1, Key: "Z", Phase: device.Press})
	k.Push(device.Event{Seq: 2, Key: "A", Phase: device.Press})
	k.Push(device.Event{Seq: 3, Key: "M", Phase: device.Press})
	edges := k.FocusChange(device.FocusEvent{Focused: false})
	want := []device.Edge{
		{Key: "A", Phase: device.Release, Forced: true},
		{Key: "M", Phase: device.Release, Forced: true},
		{Key: "Z", Phase: device.Release, Forced: true},
	}
	if !reflect.DeepEqual(edgeKeys(edges), edgeKeys(want)) || !allForced(edges) {
		t.Fatalf("got %v want %v", edges, want)
	}
	if k.Held("A") || k.Held("M") || k.Held("Z") {
		t.Fatal("no key may remain held after blur")
	}
}

func edgeKeys(edges []device.Edge) []device.Edge {
	out := make([]device.Edge, len(edges))
	for i, e := range edges {
		out[i] = device.Edge{Key: e.Key, Phase: e.Phase}
	}
	return out
}

func allForced(edges []device.Edge) bool {
	for _, e := range edges {
		if !e.Forced {
			return false
		}
	}
	return true
}

// After a forced reset the late physical release carries the old epoch; a new
// press starts a fresh generation and the stale release cannot end it.
func TestStaleReleaseAcrossEpoch(t *testing.T) {
	k := device.NewVirtualKeyboard()
	k.Push(device.Event{Seq: 1, Key: "A", Phase: device.Press})
	k.Reset()
	stale, ok := k.Push(device.Event{Seq: 2, Key: "A", Phase: device.Release})
	if !ok || stale.Epoch != 0 || stale.Forced {
		t.Fatalf("stale release expected epoch 0, not forced, got %+v ok=%v", stale, ok)
	}
	fresh, ok := k.Push(device.Event{Seq: 3, Key: "A", Phase: device.Press}) // new generation
	if !ok || fresh.Epoch != 1 {
		t.Fatalf("fresh press expected epoch 1, got %+v ok=%v", fresh, ok)
	}
	if !k.Held("A") {
		t.Fatal("A should be held by the fresh press")
	}
	if dup, ok := k.Push(device.Event{Seq: 4, Key: "A", Phase: device.Release}); !ok || dup.Epoch != 1 {
		t.Fatalf("new generation release expected epoch 1, got %+v ok=%v", dup, ok)
	}

	// A fresh press while the stranded release has not arrived yet is a
	// hardware duplicate: suppressed. The stale release clears the slot and a
	// later press starts a real new generation.
	k2 := device.NewVirtualKeyboard()
	k2.Push(device.Event{Seq: 1, Key: "A", Phase: device.Press})
	k2.Reset()
	if fresh2, ok := k2.Push(device.Event{Seq: 2, Key: "A", Phase: device.Press}); ok {
		t.Fatalf("press before the stale release must be suppressed, got %+v", fresh2)
	}
	stale2, ok := k2.Push(device.Event{Seq: 3, Key: "A", Phase: device.Release})
	if !ok || stale2.Epoch != 0 {
		t.Fatalf("stranded release expected epoch 0, got %+v ok=%v", stale2, ok)
	}
	fresh3, ok := k2.Push(device.Event{Seq: 4, Key: "A", Phase: device.Press})
	if !ok || fresh3.Epoch != 1 {
		t.Fatalf("new press after stale release expected epoch 1, got %+v ok=%v", fresh3, ok)
	}
}

func TestRegainFocusEmitsNothing(t *testing.T) {
	k := device.NewVirtualKeyboard()
	k.Push(device.Event{Seq: 1, Key: "A", Phase: device.Press})
	k.FocusChange(device.FocusEvent{Focused: false})
	if edges := k.FocusChange(device.FocusEvent{Focused: true}); len(edges) != 0 {
		t.Fatalf("regaining focus emits no edges, got %v", edges)
	}
}
