//go:build !darwin || !cgo

package platform

func InputCapability() error { return ErrInputUnavailable }

func BindInput(sourceID uint32) (InputBinding, error) { return nil, ErrInputUnavailable }

func OwnWindowFocused(title string) bool {
	focused, _ := OwnWindowFocusState(title)
	return focused
}

func OwnWindowFocusState(title string) (bool, uint64) { return false, 0 }
