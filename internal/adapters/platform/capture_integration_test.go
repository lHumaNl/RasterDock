//go:build integration && darwin

package platform

import "testing"

// Enumeration never implicitly authorizes capture of somebody else's window.
func TestNativeSourceEnumeration(t *testing.T) {
	sources, err := Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) == 0 {
		t.Fatal("no shareable windows; desktop and Screen Recording permission required")
	}
	seen := make(map[uint32]bool)
	for _, source := range sources {
		if source.ID == 0 || seen[source.ID] {
			t.Fatal("invalid or duplicate native window ID")
		}
		seen[source.ID] = true
	}
}
