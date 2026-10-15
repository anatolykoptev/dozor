//go:build !linux

package main

import "errors"

// Off Linux the privilege and mount facts cannot be read, so the worker
// fails closed. The binary builds here only so the module builds everywhere.
func readProcStatus() (string, int, error) {
	return "", 0, errors.New("process status unsupported on this platform")
}

func checkMount(string) error { return errors.New("mount check unsupported on this platform") }
