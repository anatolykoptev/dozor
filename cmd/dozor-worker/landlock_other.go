//go:build !linux

package main

import "errors"

// Landlock is Linux-only; off Linux the worker fails closed.
func restrictBindTCP(_, _ uint16) error {
	return errors.New("landlock is not available on this platform")
}
