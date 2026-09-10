//go:build !darwin && !linux

package main

import "io"

func terminalColumns(io.Writer) int     { return 80 }
func terminalSize(io.Writer) (int, int) { return 80, 24 }
