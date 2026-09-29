package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	getConsoleMode   = kernel32.NewProc("GetConsoleMode")
	setConsoleMode   = kernel32.NewProc("SetConsoleMode")
	setConsoleOutput = kernel32.NewProc("SetConsoleOutputCP")
	getConsoleOutput = kernel32.NewProc("GetConsoleOutputCP")
	setAttributes    = kernel32.NewProc("SetFileAttributesW")
)

// What the console was set to before, given back on the way out: the shell goes on with it
var oldOutputCp, oldMode uintptr
var modeSet bool

// Colours and non-Latin text need the console told: escape codes on, UTF-8 out
func setupTerminal() {
	oldOutputCp, _, _ = getConsoleOutput.Call()
	setConsoleOutput.Call(65001)
	handle := syscall.Handle(os.Stdout.Fd())
	var mode uint32
	if r, _, _ := getConsoleMode.Call(uintptr(handle), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return
	}
	oldMode, modeSet = uintptr(mode), true
	setConsoleMode.Call(uintptr(handle), uintptr(mode|0x0004))
}

func restoreTerminal() {
	if oldOutputCp != 0 {
		setConsoleOutput.Call(oldOutputCp)
	}
	if modeSet {
		setConsoleMode.Call(uintptr(syscall.Handle(os.Stdout.Fd())), oldMode)
	}
}

func hideDir(path string) {
	p, err := syscall.UTF16PtrFromString(path)
	if err == nil {
		setAttributes.Call(uintptr(unsafe.Pointer(p)), 0x2)
	}
}
