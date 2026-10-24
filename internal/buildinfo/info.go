// Package buildinfo holds linker-injected metadata shared by GUI and logging.
package buildinfo

var (
	// Version is a tag or dev for local builds.
	Version = "dev"
	// Commit identifies the built source commit.
	Commit = "dev"
	// BuildTime is the UTC build timestamp or dev.
	BuildTime = "dev"
)
