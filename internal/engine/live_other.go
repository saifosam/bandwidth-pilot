//go:build !windows

package engine

import (
	"errors"
	"io"

	"bandwidthpilot/internal/shaper"
)

// NewLive returns the real packet engine for this OS. Only Windows has one
// so far; a macOS engine (pf + dummynet, or a Network Extension) plugs in here.
func NewLive(s *shaper.Shaper, opts *Options) (Engine, error) {
	return nil, errors.New("live traffic control is only available on Windows so far")
}

// SelfTest prints platform diagnostics.
func SelfTest(w io.Writer) { io.WriteString(w, "self-test is only available on Windows\n") }
