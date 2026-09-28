//go:build !darwin && !linux

package main

import "io"

func terminalSize(out io.Writer) (int, int) {
	if sized, ok := out.(interface{ terminalSize() (int, int) }); ok {
		return sized.terminalSize()
	}
	return 80, 24
}
