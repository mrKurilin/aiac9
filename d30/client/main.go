package main

import (
	"aiac9-week6"
	"context"
	"fmt"
	"os"
)

func main() {
	var err error
	if len(os.Args) == 1 {
		if baseURL := os.Getenv("MRKAI_API_URL"); baseURL != "" {
			var client *remoteClient
			client, err = newRemoteClient(baseURL, os.Getenv("MRKAI_API_TOKEN"))
			if err == nil {
				err = week6.RunRemote(context.Background(), client, os.Stdin, os.Stdout)
			}
		} else {
			err = week6.Run(30)
		}
	} else {
		err = fmt.Errorf("использование: mrkai")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
