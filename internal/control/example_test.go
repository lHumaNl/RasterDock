package control_test

import (
	"fmt"
	"time"

	"rasterdock/internal/control"
)

func ExampleParser() {
	var parser control.Parser
	now := time.Unix(100, 0)
	for _, key := range []string{"F8", "1", "Tab", "2", "Return"} {
		if command, accepted := parser.Key(key, now, 10); accepted {
			fmt.Println(command.Nonce, command.Ordinal)
		}
	}
	// Output: 1 2
}
