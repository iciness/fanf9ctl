//go:build !windows

package main

import "fmt"

// RunDiag 在非 Windows 平台没有意义。
func RunDiag(match string) error { return fmt.Errorf("GATT 诊断仅支持 Windows") }
