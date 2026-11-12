//go:build windows

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func defaultInstallDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(base, "Programs", "Tash")
}

func executableSuffix() string { return ".exe" }

func validPayload(b []byte) error {
	if len(b) < 2 || !bytes.Equal(b[:2], []byte("MZ")) {
		return errors.New("embedded payload is not a Windows executable; rebuild the installer for windows")
	}
	return nil
}

// relaunchInTerminal is unnecessary on Windows: a console-subsystem executable
// opened from Explorer receives its own console window.
func relaunchInTerminal() (bool, error) { return false, nil }

func addToUserPath(dir string) (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer key.Close()

	current, kind, err := key.GetStringValue("Path")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, err
	}
	for _, part := range filepath.SplitList(current) {
		if strings.EqualFold(filepath.Clean(part), filepath.Clean(dir)) {
			return false, nil
		}
	}
	next := dir
	if strings.TrimSpace(current) != "" {
		next = strings.TrimRight(current, ";") + ";" + dir
	}
	if kind == registry.EXPAND_SZ {
		err = key.SetExpandStringValue("Path", next)
	} else {
		err = key.SetStringValue("Path", next)
	}
	if err != nil {
		return false, err
	}
	broadcastEnvironmentChange()
	return true, nil
}

const (
	hwndBroadcast     = 0xffff
	wmSettingChange   = 0x001a
	smtoAbortIfHung   = 0x0002
	settingChangeWait = 5000
)

var (
	user32             = windows.NewLazySystemDLL("user32.dll")
	sendMessageTimeout = user32.NewProc("SendMessageTimeoutW")
)

// broadcastEnvironmentChange tells Explorer and other listeners that the user
// PATH changed, so newly opened terminals pick up `tash` without a reboot.
func broadcastEnvironmentChange() {
	name, err := syscall.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	var result uintptr
	_, _, _ = sendMessageTimeout.Call(
		hwndBroadcast, wmSettingChange, 0,
		uintptr(unsafe.Pointer(name)), smtoAbortIfHung, settingChangeWait,
		uintptr(unsafe.Pointer(&result)),
	)
}
