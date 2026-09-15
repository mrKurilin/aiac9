//go:build !darwin && !linux

package main

import "io"

func terminalSize(io.Writer) (int, int) { return 80, 24 }
