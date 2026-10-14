package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata"
)

func setting(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func serveApplication(mode string) error {
	key := os.Getenv("MRKAI_SERVICE_KEY")
	if len(key) < 16 {
		return fmt.Errorf("задайте MRKAI_SERVICE_KEY (не менее 16 символов) для связи сервисов")
	}
	var handler http.Handler
	var addr string
	if mode == "web" {
		password := os.Getenv("CRM_ADMIN_PASSWORD")
		if len(password) < 12 {
			return fmt.Errorf("задайте CRM_ADMIN_PASSWORD (не менее 12 символов)")
		}
		location, err := time.LoadLocation("Europe/Moscow")
		if err != nil {
			return err
		}
		store, err := openCRMStore(setting("CRM_DB_PATH", "data/crm.sqlite"), func() time.Time { return time.Now().In(location) })
		if err != nil {
			return fmt.Errorf("не удалось открыть SQLite CRM")
		}
		defer store.db.Close()
		app := &crmHTTP{store: store, adminPassword: password, serviceKey: key, supportURL: setting("SUPPORT_URL", "http://127.0.0.1:8081"), client: &http.Client{Timeout: 90 * time.Second}}
		handler, addr = app.handler(), setting("WEB_ADDR", "127.0.0.1:8080")
	} else {
		if os.Getenv("DEEPSEEK_API_KEY") == "" {
			return fmt.Errorf("задайте DEEPSEEK_API_KEY для сервиса поддержки")
		}
		root := setting("MRKAI_PROJECT_ROOT", ".")
		search := searchIndex{root: root, path: filepath.Join(setting("SUPPORT_DATA_DIR", "data/support"), "index.json"), docsOnly: true}
		if _, err := search.build(context.Background(), func(step string) { log.Print(step) }); err != nil {
			return fmt.Errorf("не удалось индексировать FAQ: %w", err)
		}
		model := deepSeekClient{key: os.Getenv("DEEPSEEK_API_KEY"), url: setting("DEEPSEEK_BASE_URL", "https://api.deepseek.com/chat/completions"), model: setting("DEEPSEEK_MODEL", "deepseek-chat"), http: &http.Client{Timeout: 45 * time.Second}, jsonOutput: true}
		crm := &crmClient{url: setting("CRM_URL", "http://127.0.0.1:8080"), key: key, http: &http.Client{Timeout: 15 * time.Second}}
		app := &supportHTTP{agent: &serviceAgent{model: model, crm: crm, search: search}, key: key, slots: make(chan struct{}, 8)}
		handler, addr = app.handler(), setting("SUPPORT_ADDR", "127.0.0.1:8081")
	}
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 95 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Printf("mrkai %s: слушаю настроенный порт", mode)
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("не удалось запустить HTTP-сервис")
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}
