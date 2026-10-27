package platform

// ControlTarget pins the native source used independently by capture and input.
// A numeric window ID alone is not sufficient across process/window retirement.
type ControlTarget struct {
	Window  uint32
	PID     int
	Started uint64
	Title   string
}

// Valid rejects missing provenance instead of inferring identity from a title.
func (t ControlTarget) Valid() bool {
	return t.Window != 0 && t.PID > 0 && t.Started != 0 && t.Title != ""
}
