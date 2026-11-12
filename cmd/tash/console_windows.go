//go:build windows

package main

import "golang.org/x/sys/windows"

// The dashboard and installer use box-drawing characters and block art. Force
// the console to UTF-8 so legacy Command Prompt windows render them correctly.
func init() {
	_ = windows.SetConsoleCP(65001)
	_ = windows.SetConsoleOutputCP(65001)
}
