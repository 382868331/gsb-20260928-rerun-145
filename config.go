package input

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const DefaultContext ContextID = "default"

type ContextID string
type ActionID string
type Button string

type Trigger string

const (
	TriggerTap     Trigger = "tap"
	TriggerHold    Trigger = "hold"
	TriggerToggle  Trigger = "toggle"
	TriggerContext Trigger = "context"
)

type Binding struct {
	ID          string    `json:"id"`
	Action      ActionID  `json:"action"`
	Context     ContextID `json:"context,omitempty"`
	Keys        []Button  `json:"keys"`
	Trigger     Trigger   `json:"trigger"`
	Priority    int       `json:"priority,omitempty"`
	LongPressMS int64     `json:"long_press_ms,omitempty"`
	Target      ContextID `json:"target_context,omitempty"`
}

type Config struct {
	DefaultContext ContextID   `json:"default_context,omitempty"`
	Contexts       []ContextID `json:"contexts,omitempty"`
	Bindings       []Binding   `json:"bindings"`
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg.Normalize()
}

func SaveConfig(path string, cfg Config) (Config, error) {
	normalized, err := cfg.Normalize()
	if err != nil {
		return Config{}, err
	}
	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return Config{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Config{}, err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return Config{}, err
	}
	return normalized, nil
}

func (cfg Config) Normalize() (Config, error) {
	if cfg.DefaultContext == "" {
		cfg.DefaultContext = DefaultContext
	}
	contexts := map[ContextID]struct{}{
		DefaultContext:     {},
		cfg.DefaultContext: {},
	}
	for _, context := range cfg.Contexts {
		if context == "" {
			return Config{}, fmt.Errorf("context id must not be empty")
		}
		contexts[context] = struct{}{}
	}
	ids := make(map[string]struct{}, len(cfg.Bindings))
	for i := range cfg.Bindings {
		binding := &cfg.Bindings[i]
		if binding.Context == "" {
			binding.Context = cfg.DefaultContext
		}
		if binding.ID == "" {
			binding.ID = string(binding.Action)
		}
		if binding.Trigger == "" {
			binding.Trigger = TriggerTap
		}
		contexts[binding.Context] = struct{}{}
		if _, exists := ids[binding.ID]; exists {
			return Config{}, fmt.Errorf("duplicate binding id %q", binding.ID)
		}
		ids[binding.ID] = struct{}{}
		if err := validateBinding(*binding, contexts); err != nil {
			return Config{}, err
		}
	}
	sort.Slice(cfg.Bindings, func(i, j int) bool {
		return cfg.Bindings[i].ID < cfg.Bindings[j].ID
	})
	cfg.Contexts = normalizedContexts(contexts, cfg.DefaultContext)
	return cfg, nil
}

func validateBinding(binding Binding, contexts map[ContextID]struct{}) error {
	if binding.Action == "" {
		return fmt.Errorf("binding %q: action must not be empty", binding.ID)
	}
	if len(binding.Keys) == 0 {
		return fmt.Errorf("binding %q: keys must not be empty", binding.ID)
	}
	if _, ok := contexts[binding.Context]; !ok {
		return fmt.Errorf("binding %q: unknown context %q", binding.ID, binding.Context)
	}
	seen := make(map[Button]struct{}, len(binding.Keys))
	for _, key := range binding.Keys {
		if key == "" {
			return fmt.Errorf("binding %q: key must not be empty", binding.ID)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("binding %q: duplicate key %q", binding.ID, key)
		}
		seen[key] = struct{}{}
	}
	switch binding.Trigger {
	case TriggerTap, TriggerHold, TriggerToggle, TriggerContext:
	default:
		return fmt.Errorf("binding %q: unsupported trigger %q", binding.ID, binding.Trigger)
	}
	if binding.Trigger == TriggerHold {
		if binding.LongPressMS <= 0 {
			return fmt.Errorf("binding %q: hold trigger requires positive long_press_ms", binding.ID)
		}
	}
	if binding.Trigger == TriggerContext {
		if binding.Target == "" {
			return fmt.Errorf("binding %q: context trigger requires target_context", binding.ID)
		}
		if _, ok := contexts[binding.Target]; !ok {
			return fmt.Errorf("binding %q: unknown target context %q", binding.ID, binding.Target)
		}
	}
	return nil
}

func normalizedContexts(contexts map[ContextID]struct{}, defaultContext ContextID) []ContextID {
	result := make([]ContextID, 0, len(contexts))
	for context := range contexts {
		if context != DefaultContext || context == defaultContext {
			result = append(result, context)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func bindingLess(a, b Binding) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	if len(a.Keys) != len(b.Keys) {
		return len(a.Keys) > len(b.Keys)
	}
	if a.ID != b.ID {
		return a.ID < b.ID
	}
	return strings.Join(buttonsToStrings(a.Keys), ",") < strings.Join(buttonsToStrings(b.Keys), ",")
}

func buttonsToStrings(keys []Button) []string {
	result := make([]string, len(keys))
	for i, key := range keys {
		result[i] = string(key)
	}
	return result
}
