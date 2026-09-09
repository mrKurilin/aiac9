//go:build !darwin && !linux

package main

import "io"

func terminalColumns(io.Writer) int { return 80 }
