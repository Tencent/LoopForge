package main

import "errors"

// restartRunner fails closed because Windows Runner upgrades require a stopped process.
func restartRunner(_ string) error {
	return errors.New("automatic Runner restart is unsupported on Windows; stop the Runner before replacing its binaries")
}
