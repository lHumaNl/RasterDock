package presentation

import "testing"

func TestPlaybackRepeatsMetadata(t *testing.T) {
	p := NewPlayback(2, 100)
	if p.Next() != 0 || p.Next() != 1 {
		t.Fatal("metadata must lead")
	}
	for i := uint64(2); i < 12; i++ {
		if n := p.Next(); n != i {
			t.Fatal(n)
		}
	}
	if p.Next() != 0 {
		t.Fatal("metadata not repeated after ten data pages")
	}
	empty := NewPlayback(2, 2)
	for i := range 10 {
		if empty.Next() != uint64(i%2) {
			t.Fatal("empty transfer cycle")
		}
	}
}
