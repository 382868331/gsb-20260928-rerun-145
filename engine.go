package input

import (
	"fmt"
	"sort"
)

type pendingHold struct {
	binding Binding
	dueMS   int64
}

type Engine struct {
	config        Config
	context       ContextID
	pressed       map[Button]struct{}
	active        map[string]Binding
	actionRefs    map[ActionID]int
	toggled       map[ActionID]Binding
	pending       map[string]pendingHold
	lastSeq       int64
	lastTimeMS    int64
	haveLastEvent bool
	lastEvent     InputEvent
}

func NewEngine(config Config) (*Engine, error) {
	normalized, err := config.Normalize()
	if err != nil {
		return nil, err
	}
	return &Engine{
		config:     normalized,
		context:    normalized.DefaultContext,
		pressed:    make(map[Button]struct{}),
		active:     make(map[string]Binding),
		actionRefs: make(map[ActionID]int),
		toggled:    make(map[ActionID]Binding),
		pending:    make(map[string]pendingHold),
	}, nil
}

func (engine *Engine) Context() ContextID { return engine.context }

func (engine *Engine) ActiveAction(action ActionID) bool {
	return engine.actionRefs[action] > 0
}

func (engine *Engine) Reset() {
	engine.context = engine.config.DefaultContext
	clear(engine.pressed)
	clear(engine.active)
	clear(engine.actionRefs)
	clear(engine.toggled)
	clear(engine.pending)
	engine.lastSeq = 0
	engine.lastTimeMS = 0
	engine.haveLastEvent = false
	engine.lastEvent = InputEvent{}
}

func (engine *Engine) Replay(events []InputEvent) (Result, error) {
	var combined Result
	for _, event := range events {
		result, err := engine.Process(event)
		if err != nil {
			return Result{}, err
		}
		combined.merge(result)
	}
	return combined, nil
}

func (engine *Engine) Process(event InputEvent) (Result, error) {
	if engine.haveLastEvent {
		if event.Seq == engine.lastSeq {
			if event == engine.lastEvent {
				return Result{}, nil
			}
			return Result{}, fmt.Errorf("seq %d reused for a different event", event.Seq)
		}
		if event.Seq < engine.lastSeq {
			return Result{}, fmt.Errorf("event seq %d is older than %d", event.Seq, engine.lastSeq)
		}
		if event.TimestampMS < engine.lastTimeMS {
			return Result{}, fmt.Errorf("event timestamp %d is older than %d", event.TimestampMS, engine.lastTimeMS)
		}
	}

	var result Result
	if event.Kind == FocusLost {
		clear(engine.pending)
	}
	result.merge(engine.fireDueHolds(event.Seq, event.TimestampMS))

	switch event.Kind {
	case ButtonDown:
		if event.Button == "" {
			return Result{}, fmt.Errorf("button down event requires a button")
		}
		if _, exists := engine.pressed[event.Button]; !exists {
			engine.pressed[event.Button] = struct{}{}
			result.merge(engine.onButtonDown(event))
		}
	case ButtonUp:
		if event.Button == "" {
			return Result{}, fmt.Errorf("button up event requires a button")
		}
		if _, exists := engine.pressed[event.Button]; exists {
			engine.cancelPending(event.Button)
			delete(engine.pressed, event.Button)
			result.merge(engine.releaseBindingsUsing(event.Seq, event.TimestampMS, event.Button))
		}
	case FocusLost:
		result.merge(engine.releaseAll(event.Seq, event.TimestampMS))
	default:
		return Result{}, fmt.Errorf("unsupported input event kind %d", event.Kind)
	}

	engine.lastSeq = event.Seq
	engine.lastTimeMS = event.TimestampMS
	engine.haveLastEvent = true
	engine.lastEvent = event
	return result, nil
}

func (engine *Engine) onButtonDown(event InputEvent) Result {
	binding, ok := engine.selectBinding(event.Button)
	if !ok {
		return Result{}
	}
	var result Result
	switch binding.Trigger {
	case TriggerTap:
		engine.pressAction(&result, event.Seq, event.TimestampMS, binding)
		engine.releaseAction(&result, event.Seq, event.TimestampMS, binding)
	case TriggerHold:
		engine.pending[binding.ID] = pendingHold{binding: binding, dueMS: event.TimestampMS + binding.LongPressMS}
	case TriggerToggle:
		if _, on := engine.toggled[binding.Action]; on {
			delete(engine.toggled, binding.Action)
			engine.releaseAction(&result, event.Seq, event.TimestampMS, binding)
		} else {
			engine.toggled[binding.Action] = binding
			engine.pressAction(&result, event.Seq, event.TimestampMS, binding)
		}
	case TriggerContext:
		engine.pressAction(&result, event.Seq, event.TimestampMS, binding)
		engine.releaseAction(&result, event.Seq, event.TimestampMS, binding)
		result.merge(engine.switchContext(event.Seq, event.TimestampMS, binding.Target))
	}
	return result
}

func (engine *Engine) selectBinding(button Button) (Binding, bool) {
	var selected Binding
	found := false
	for _, binding := range engine.config.Bindings {
		if binding.Context == engine.context && containsButton(binding.Keys, button) && engine.allPressed(binding.Keys) {
			if !found || bindingLess(binding, selected) {
				selected = binding
				found = true
			}
		}
	}
	return selected, found
}

func (engine *Engine) allPressed(keys []Button) bool {
	for _, key := range keys {
		if _, ok := engine.pressed[key]; !ok {
			return false
		}
	}
	return true
}

func (engine *Engine) fireDueHolds(seq, timestampMS int64) Result {
	due := make([]pendingHold, 0, len(engine.pending))
	for _, item := range engine.pending {
		if item.dueMS <= timestampMS {
			due = append(due, item)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].dueMS != due[j].dueMS {
			return due[i].dueMS < due[j].dueMS
		}
		return due[i].binding.ID < due[j].binding.ID
	})
	var result Result
	for _, item := range due {
		current, stillPending := engine.pending[item.binding.ID]
		if !stillPending || current.dueMS != item.dueMS || !engine.allPressed(item.binding.Keys) {
			continue
		}
		delete(engine.pending, item.binding.ID)
		if _, exists := engine.active[item.binding.ID]; !exists {
			engine.active[item.binding.ID] = item.binding
			engine.pressAction(&result, seq, timestampMS, item.binding)
		}
	}
	return result
}

func (engine *Engine) cancelPending(button Button) {
	for id, item := range engine.pending {
		if containsButton(item.binding.Keys, button) {
			delete(engine.pending, id)
		}
	}
}

func (engine *Engine) releaseBindingsUsing(seq, timestampMS int64, button Button) Result {
	ids := make([]string, 0)
	for id, binding := range engine.active {
		if containsButton(binding.Keys, button) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var result Result
	for _, id := range ids {
		binding := engine.active[id]
		delete(engine.active, id)
		engine.releaseAction(&result, seq, timestampMS, binding)
	}
	return result
}

func (engine *Engine) releaseAll(seq, timestampMS int64) Result {
	result := engine.releasePersistentState(seq, timestampMS)
	clear(engine.pressed)
	return result
}

func (engine *Engine) switchContext(seq, timestampMS int64, target ContextID) Result {
	if target == engine.context {
		return Result{}
	}
	result := engine.releasePersistentState(seq, timestampMS)
	from := engine.context
	engine.context = target
	result.appendContext(seq, timestampMS, from, target)
	return result
}

func (engine *Engine) releasePersistentState(seq, timestampMS int64) Result {
	ids := make([]string, 0, len(engine.active))
	for id := range engine.active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var result Result
	for _, id := range ids {
		binding := engine.active[id]
		delete(engine.active, id)
		engine.releaseAction(&result, seq, timestampMS, binding)
	}

	actions := make([]ActionID, 0, len(engine.toggled))
	for action := range engine.toggled {
		actions = append(actions, action)
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i] < actions[j] })
	for _, action := range actions {
		binding := engine.toggled[action]
		delete(engine.toggled, action)
		engine.releaseAction(&result, seq, timestampMS, binding)
	}
	clear(engine.pending)
	return result
}

func (engine *Engine) pressAction(result *Result, seq, timestampMS int64, binding Binding) {
	if engine.actionRefs[binding.Action] == 0 {
		result.appendAction(seq, timestampMS, binding, ActionPressed)
	}
	engine.actionRefs[binding.Action]++
}

func (engine *Engine) releaseAction(result *Result, seq, timestampMS int64, binding Binding) {
	if engine.actionRefs[binding.Action] <= 0 {
		return
	}
	engine.actionRefs[binding.Action]--
	if engine.actionRefs[binding.Action] == 0 {
		result.appendAction(seq, timestampMS, binding, ActionReleased)
	}
}

func containsButton(keys []Button, wanted Button) bool {
	for _, key := range keys {
		if key == wanted {
			return true
		}
	}
	return false
}
