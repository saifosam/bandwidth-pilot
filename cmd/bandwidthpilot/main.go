// Bandwidth Pilot - per-app network monitor, speed limiter, blocker and prioritiser.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"bandwidthpilot/internal/core"
	"bandwidthpilot/internal/engine"
)

const version = "0.2.0"

type options struct {
	demo   bool
	serve  bool
	port   int
	noOpen bool
	debug  bool
}

func main() {
	var o options
	selftest := flag.Bool("selftest", false, "check the Windows plumbing and exit")
	flag.BoolVar(&o.demo, "demo", false, "use simulated traffic instead of the real network")
	flag.BoolVar(&o.serve, "serve", false, "serve the interface on localhost and open it in a browser instead of a window")
	flag.IntVar(&o.port, "port", 0, "port for --serve (default: pick a free one)")
	flag.BoolVar(&o.noOpen, "no-open", false, "with --serve, do not open a browser")
	flag.BoolVar(&o.debug, "debug", false, "enable the web inspector in the window")
	flag.Parse()

	dir := dataDir()
	if o.demo {
		// Keep simulated traffic out of the real rules and usage history.
		dir = filepath.Join(dir, "demo")
	}
	os.MkdirAll(dir, 0o755)

	if *selftest {
		var w io.Writer = os.Stdout
		if f, err := os.Create(filepath.Join(dir, "selftest.txt")); err == nil {
			defer f.Close()
			w = io.MultiWriter(os.Stdout, f)
		}
		engine.SelfTest(w)
		return
	}

	logger := log.New(io.Discard, "", log.LstdFlags)
	if f, err := os.OpenFile(filepath.Join(dir, "bandwidthpilot.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		defer f.Close()
		logger.SetOutput(f)
	}
	logf := func(format string, args ...any) { logger.Printf(format, args...) }
	logf("Bandwidth Pilot %s on %s/%s, data in %s", version, runtime.GOOS, runtime.GOARCH, dir)

	sv := core.New(dir, version, logf)
	sv.Run()
	defer sv.Close()

	if o.demo {
		sv.UseEngine(engine.NewDemo(sv.S))
	} else {
		live, err := engine.NewLive(sv.S, logf)
		if err == nil {
			err = sv.UseEngine(live)
		}
		if err != nil {
			logf("live engine unavailable: %v", err)
			if runtime.GOOS == "windows" {
				// Show the reason in the window; the user can switch to the demo.
				sv.UseEngine(engine.NewFailed(err.Error()))
			} else {
				sv.UseEngine(engine.NewDemo(sv.S))
			}
		}
	}

	if err := runShell(sv, o); err != nil {
		logf("fatal: %v", err)
		fmt.Fprintln(os.Stderr, "bandwidthpilot:", err)
	}
}

func dataDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "Bandwidth Pilot")
}
