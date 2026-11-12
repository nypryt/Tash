//go:build windows

package main

import "golang.org/x/sys/windows"

// The installer prints block art and box drawing. Force the console to UTF-8
// so legacy Command Prompt windows render it correctly.
func init() {
	_ = windows.SetConsoleCP(65001)
	_ = windows.SetConsoleOutputCP(65001)
}
