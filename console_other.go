//go:build !windows

package main

func ensureConsole() {}

func hasConsole() bool { return false }
