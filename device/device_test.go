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
	if !ok || edge != (device.Edge{Key: "A", Phase: device.Press, Round: 1}) {
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
		{Key: "A", Phase: device.Release, Round: 2},
		{Key: "M", Phase: device.Release, Round: 3},
		{Key: "Z", Phase: device.Release, Round: 1},
	}
	if !reflect.DeepEqual(edges, want) {
		t.Fatalf("got %v want %v", edges, want)
	}
	if k.Held("A") || k.Held("M") || k.Held("Z") {
		t.Fatal("no key may remain held after blur")
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

func TestEdgesCarryDeviceSource(t *testing.T) {
	k := device.NewVirtualKeyboard("pad1")
	if k.Source() != "pad1" {
		t.Fatalf("source = %q", k.Source())
	}
	edge, ok := k.Push(device.Event{Seq: 1, Key: "A", Phase: device.Press})
	if !ok || edge.Source != "pad1" {
		t.Fatalf("edge = %+v ok=%v, want source pad1", edge, ok)
	}
	edges := k.FocusChange(device.FocusEvent{Focused: false})
	if len(edges) != 1 || edges[0].Source != "pad1" {
		t.Fatalf("blur edges = %v, want one edge from pad1", edges)
	}
}
