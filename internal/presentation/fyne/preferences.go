package fyneui

import (
	"slices"
	"sync"

	"fyne.io/fyne/v2"
)

// memoryPreferences is intentionally process-local: file-dialog hints must not
// create a second settings root or a shared mutable recent-file registry.
type memoryPreferences struct {
	mu        sync.RWMutex
	values    map[string]any
	listeners []func()
}

var _ fyne.Preferences = (*memoryPreferences)(nil)

func preference[T any](p *memoryPreferences, key string, fallback T) T {
	p.mu.RLock()
	defer p.mu.RUnlock()
	value, ok := p.values[key].(T)
	if !ok {
		return fallback
	}
	return value
}

func (p *memoryPreferences) set(key string, value any) {
	p.mu.Lock()
	p.values[key] = value
	listeners := slices.Clone(p.listeners)
	p.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}

func (p *memoryPreferences) RemoveValue(key string) {
	p.mu.Lock()
	delete(p.values, key)
	listeners := slices.Clone(p.listeners)
	p.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}

func (p *memoryPreferences) AddChangeListener(listener func()) {
	if listener == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listeners = append(p.listeners, listener)
}

func (p *memoryPreferences) ChangeListeners() []func() {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return slices.Clone(p.listeners)
}

func (p *memoryPreferences) Bool(key string) bool { return p.BoolWithFallback(key, false) }
func (p *memoryPreferences) BoolWithFallback(key string, fallback bool) bool {
	return preference(p, key, fallback)
}
func (p *memoryPreferences) SetBool(key string, value bool) { p.set(key, value) }
func (p *memoryPreferences) Int(key string) int             { return p.IntWithFallback(key, 0) }
func (p *memoryPreferences) IntWithFallback(key string, fallback int) int {
	return preference(p, key, fallback)
}
func (p *memoryPreferences) SetInt(key string, value int) { p.set(key, value) }
func (p *memoryPreferences) Float(key string) float64     { return p.FloatWithFallback(key, 0) }
func (p *memoryPreferences) FloatWithFallback(key string, fallback float64) float64 {
	return preference(p, key, fallback)
}
func (p *memoryPreferences) SetFloat(key string, value float64) { p.set(key, value) }
func (p *memoryPreferences) String(key string) string           { return p.StringWithFallback(key, "") }
func (p *memoryPreferences) StringWithFallback(key, fallback string) string {
	return preference(p, key, fallback)
}
func (p *memoryPreferences) SetString(key, value string) { p.set(key, value) }

// Clone on both sides: callers must not mutate a stored slice outside the lock.
func (p *memoryPreferences) BoolList(key string) []bool { return p.BoolListWithFallback(key, nil) }
func (p *memoryPreferences) BoolListWithFallback(key string, fallback []bool) []bool {
	return slices.Clone(preference(p, key, fallback))
}
func (p *memoryPreferences) SetBoolList(key string, value []bool) { p.set(key, slices.Clone(value)) }
func (p *memoryPreferences) IntList(key string) []int             { return p.IntListWithFallback(key, nil) }
func (p *memoryPreferences) IntListWithFallback(key string, fallback []int) []int {
	return slices.Clone(preference(p, key, fallback))
}
func (p *memoryPreferences) SetIntList(key string, value []int) { p.set(key, slices.Clone(value)) }
func (p *memoryPreferences) FloatList(key string) []float64     { return p.FloatListWithFallback(key, nil) }
func (p *memoryPreferences) FloatListWithFallback(key string, fallback []float64) []float64 {
	return slices.Clone(preference(p, key, fallback))
}
func (p *memoryPreferences) SetFloatList(key string, value []float64) {
	p.set(key, slices.Clone(value))
}
func (p *memoryPreferences) StringList(key string) []string {
	return p.StringListWithFallback(key, nil)
}
func (p *memoryPreferences) StringListWithFallback(key string, fallback []string) []string {
	return slices.Clone(preference(p, key, fallback))
}
func (p *memoryPreferences) SetStringList(key string, value []string) {
	p.set(key, slices.Clone(value))
}
