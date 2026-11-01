//go:build !windows

package main

func freeDiskBytes(string) uint64 { return 0 }
