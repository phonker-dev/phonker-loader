package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Автозапуск бота вместе с Windows: запись в HKCU\...\Run (без прав администратора).
// Выключается в меню бота и удаляет запись полностью.

const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "PhonkerLoaderBot"
)

var (
	advapi32           = syscall.NewLazyDLL("advapi32.dll")
	procRegSetValueExW = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValue = advapi32.NewProc("RegDeleteValueW")
)

func autostartCommand() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return `"` + exe + `" --bot`
}

// autostartState: "" — выключен, иначе текущая команда в реестре.
func autostartState() string {
	key, err := openRunKey(syscall.KEY_READ)
	if err != nil {
		return ""
	}
	defer syscall.RegCloseKey(key)
	buf := make([]uint16, 1024)
	size := uint32(len(buf) * 2)
	name, _ := syscall.UTF16PtrFromString(runValueName)
	if syscall.RegQueryValueEx(key, name, nil, nil, (*byte)(unsafe.Pointer(&buf[0])), &size) != nil {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func setAutostart(on bool) error {
	key, err := openRunKey(syscall.KEY_WRITE)
	if err != nil {
		return err
	}
	defer syscall.RegCloseKey(key)
	name, _ := syscall.UTF16PtrFromString(runValueName)
	if !on {
		r, _, _ := procRegDeleteValue.Call(uintptr(key), uintptr(unsafe.Pointer(name)))
		if r != 0 && r != uintptr(syscall.ERROR_FILE_NOT_FOUND) {
			return syscall.Errno(r)
		}
		return nil
	}
	value, _ := syscall.UTF16FromString(autostartCommand())
	r, _, _ := procRegSetValueExW.Call(uintptr(key), uintptr(unsafe.Pointer(name)), 0, syscall.REG_SZ,
		uintptr(unsafe.Pointer(&value[0])), uintptr(len(value)*2))
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// refreshAutostart: после обновления exe лежит рядом под новым именем
// (PhonkerLoader_v1.2.0.exe вместо v1.1.0) — переводим автозапуск на него.
// Копии из других папок автозапуск не перехватывают.
func refreshAutostart() {
	state := autostartState()
	current := autostartCommand()
	if state == "" || current == "" || strings.EqualFold(state, current) {
		return
	}
	old := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(state), "--bot"))
	old = strings.Trim(old, `" `)
	exe, _ := os.Executable()
	if strings.EqualFold(filepath.Dir(old), filepath.Dir(exe)) {
		_ = setAutostart(true)
	}
}

func openRunKey(access uint32) (syscall.Handle, error) {
	path, _ := syscall.UTF16PtrFromString(runKeyPath)
	var key syscall.Handle
	err := syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, path, 0, access, &key)
	return key, err
}
