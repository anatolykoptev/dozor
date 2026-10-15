//go:build linux

package main

import "testing"

func TestReadProcStatus_Real(t *testing.T) {
	capEff, nnp, err := readProcStatus()
	if err != nil || capEff == "" || nnp < 0 || nnp > 1 {
		t.Fatalf("capEff=%q nnp=%d err=%v", capEff, nnp, err)
	}
}

func TestCheckMount_RootFilesystemRefused(t *testing.T) {
	if err := checkMount("/"); err == nil {
		t.Fatal("/ accepted as a separate mount")
	}
	if err := checkMount("/nonexistent-dir"); err == nil {
		t.Fatal("missing directory accepted")
	}
}
