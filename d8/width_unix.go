//go:build darwin || linux

package main

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

func terminalColumns(out io.Writer) int {
	if file, ok := out.(*os.File); ok {
		var size struct{ Rows, Columns, X, Y uint16 }
		_, _, err := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&size)))
		if err == 0 && size.Columns > 0 {
			return int(size.Columns)
		}
	}
	return 80
}
