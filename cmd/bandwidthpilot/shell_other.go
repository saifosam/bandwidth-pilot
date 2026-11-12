//go:build !windows

package main

import "bandwidthpilot/internal/core"

// runShell shows the interface. Off Windows there is no native window yet,
// so the interface opens in the default browser. A macOS build would wrap
// the same page in a WKWebView here.
func runShell(sv *core.Service, o options) error {
	return serveHTTP(sv, o)
}
