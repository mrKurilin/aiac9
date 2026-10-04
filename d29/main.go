package main

import (
	"aiac9-week6"
	"fmt"
	"os"
)

func main() {
	if err := week6.Run(29); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
