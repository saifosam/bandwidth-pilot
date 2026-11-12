//go:build !windows

package main

import "errors"

func setStartup(bool) error { return errors.New("start at sign-in is only available on Windows so far") }
