package github

import "time"

// SetGhTokenTimeout lets a test outlast a slow first exec of its fake gh on a loaded machine.
func SetGhTokenTimeout(d time.Duration) func() {
	prev := ghTokenTimeout
	ghTokenTimeout = d
	return func() { ghTokenTimeout = prev }
}
