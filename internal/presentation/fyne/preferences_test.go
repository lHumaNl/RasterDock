package fyneui

import (
	"reflect"
	"sync"
	"testing"
)

func TestMemoryPreferencesScalarTypesAndFallbacks(t *testing.T) {
	p := &memoryPreferences{values: make(map[string]any)}
	p.SetBool("bool", true)
	p.SetInt("int", 7)
	p.SetFloat("float", 1.5)
	p.SetString("string", "value")
	if !p.Bool("bool") || p.Int("int") != 7 || p.Float("float") != 1.5 || p.String("string") != "value" {
		t.Fatal("scalar round trip")
	}
	if p.Bool("missing") || p.Int("missing") != 0 || p.Float("missing") != 0 || p.String("missing") != "" {
		t.Fatal("zero defaults")
	}
	if !p.BoolWithFallback("int", true) || p.IntWithFallback("bool", 9) != 9 {
		t.Fatal("typed fallback")
	}
	if p.FloatWithFallback("missing", 2.5) != 2.5 || p.StringWithFallback("missing", "fallback") != "fallback" {
		t.Fatal("fallback")
	}
}

func TestMemoryPreferencesListsAreDefensiveCopies(t *testing.T) {
	p := &memoryPreferences{values: make(map[string]any)}
	original := []string{"saved"}
	p.SetStringList("list", original)
	original[0] = "changed"
	read := p.StringList("list")
	read[0] = "changed again"
	if !reflect.DeepEqual(p.StringList("list"), []string{"saved"}) {
		t.Fatal("slice alias")
	}
	p.SetBoolList("bool", []bool{true})
	p.SetIntList("int", []int{7})
	p.SetFloatList("float", []float64{1.5})
	if !p.BoolList("bool")[0] || p.IntList("int")[0] != 7 || p.FloatList("float")[0] != 1.5 {
		t.Fatal("list round trip")
	}
	if p.StringList("missing") != nil || !p.BoolListWithFallback("missing", []bool{true})[0] {
		t.Fatal("list fallback")
	}
}

func TestMemoryPreferencesListenersCanReadAndAreCopied(t *testing.T) {
	p := &memoryPreferences{values: make(map[string]any)}
	changes := 0
	p.AddChangeListener(func() { changes++; _ = p.String("key") })
	p.AddChangeListener(nil)
	listeners := p.ChangeListeners()
	listeners[0] = nil
	p.SetString("key", "value")
	p.RemoveValue("key")
	if changes != 2 || p.String("key") != "" {
		t.Fatal("listener or removal")
	}
}

func TestMemoryPreferencesConcurrentAccess(t *testing.T) {
	p := &memoryPreferences{values: make(map[string]any)}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				p.SetInt("value", 1)
				_ = p.Int("value")
				p.SetStringList("list", []string{"value"})
				_ = p.StringList("list")
			}
		})
	}
	wg.Wait()
	if p.Int("value") != 1 {
		t.Fatal("concurrent value lost")
	}
}
