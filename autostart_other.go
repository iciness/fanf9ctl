//go:build !windows

package main

func autostartEnabled() bool { return false }

func setAutostart(on bool) error { return nil }
