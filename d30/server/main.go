package main

import (
	"aiac9-week6"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := serve(); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}

func serve() error {
	client := week6.NewOllama(os.Getenv("OLLAMA_BASE_URL"))
	agent := week6.NewAgent(client, os.Getenv("OLLAMA_MODEL"))
	service, err := week6.NewService(agent, os.Getenv("SERVICE_BIND"), os.Getenv("SERVICE_API_TOKEN"))
	if err != nil {
		return err
	}
	service.ChatLogger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := service.Start(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "HTTP API слушает", service.Bind)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return service.Stop(shutdownCtx)
}
