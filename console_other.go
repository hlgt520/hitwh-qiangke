//go:build !windows

package main

// setupConsoleCloseHandler 非 Windows 平台的占位实现（无控制台窗口关闭事件）。
func setupConsoleCloseHandler(fn func()) {}
