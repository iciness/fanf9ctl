//go:build !windows

package main

func startCPUMonitor() {}

func cpuLoad() int { return 0 }
