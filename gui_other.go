//go:build !windows

package main

// 非 Windows 平台没有原生界面，退回浏览器页面。
func runGUI(ctl *Controller, device string, startHidden bool) {}

func hideOwnConsole() {}
