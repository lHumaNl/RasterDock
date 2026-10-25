package platform

import "sync"

type windowIdentity struct {
	handle   uintptr
	pid, tid uint32
}

type tokenRegistry struct {
	mu    sync.Mutex
	next  uint64
	items map[uint32]windowIdentity
}

func newTokenRegistry() *tokenRegistry {
	return &tokenRegistry{items: make(map[uint32]windowIdentity)}
}

func (r *tokenRegistry) register(v windowIdentity) (uint32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, existing := range r.items {
		if existing == v {
			return id, nil
		}
	}
	if r.next >= uint64(^uint32(0)) {
		return 0, ErrInvalidSource
	}
	r.next++
	id := uint32(r.next)
	r.items[id] = v
	return id, nil
}

func (r *tokenRegistry) lookup(id uint32) (windowIdentity, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.items[id]
	return v, ok
}

func (r *tokenRegistry) invalidate(id uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, id)
}

func (r *tokenRegistry) retain(seen map[windowIdentity]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, v := range r.items {
		if !seen[v] {
			delete(r.items, id)
		}
	}
}
