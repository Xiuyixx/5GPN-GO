//go:build !linux

package main

import "fmt"

// run on non-linux hosts prints a clear message and exits non-zero.
// This lets `go build ./cmd/5gpn-ctl/` succeed on darwin so devs can
// run cross-platform builds while still guarding against accidental
// use of the client where SO_PEERCRED isn't available.
func run(_ request) (int, error) {
	return 1, fmt.Errorf("5gpn-ctl requires Linux for privileged operations")
}
