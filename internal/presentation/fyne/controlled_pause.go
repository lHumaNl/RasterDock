package fyneui

import (
	"fmt"

	"rasterdock/internal/adapters/platform"
	v1 "rasterdock/internal/protocol/v1"
)

// Resume retains durable state and exact native identity, never permission to refocus.
type receiveResume struct {
	command    command
	target     platform.ControlTarget
	descriptor v1.Descriptor
}

type receivePausedError struct {
	resume *receiveResume
	cause  error
}

func (e *receivePausedError) Error() string {
	return fmt.Sprintf("controlled receive paused: %v", e.cause)
}
func (e *receivePausedError) Unwrap() error { return e.cause }
