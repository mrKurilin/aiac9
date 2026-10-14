package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func healthcheck(mode string) error {
	address := setting("WEB_ADDR", "127.0.0.1:8080")
	if mode == "support" {
		address = setting("SUPPORT_ADDR", "127.0.0.1:8081")
	} else if mode != "web" {
		return fmt.Errorf("неверный сервис")
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("неверный порт сервиса")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/healthz")
	if err != nil {
		return fmt.Errorf("сервис ещё не готов")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("сервис ещё не готов")
	}
	return nil
}

func validPublicHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return false
		}
		if v4 := ip.To4(); v4 != nil {
			if v4[0] == 0 || (v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127) || (v4[0] == 198 && (v4[1] == 18 || v4[1] == 19)) {
				return false
			}
		}
		return true
	}
	if len(host) > 253 || !strings.Contains(host, ".") {
		return false
	}
	for _, suffix := range []string{".local", ".internal", ".intranet", ".corp", ".lan", ".svc", ".priv", ".vpn"} {
		if strings.HasSuffix(strings.ToLower(host), suffix) {
			return false
		}
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func checkDeployment(ctx context.Context, address string, client *http.Client) error {
	target, err := url.Parse(address)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.User != nil || target.Port() != "" || target.Path != "" || target.RawQuery != "" || target.Fragment != "" || !validPublicHost(target.Hostname()) {
		return fmt.Errorf("укажите публичный HTTP/HTTPS адрес без пароля, порта и пути")
	}
	copyClient := *client
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	for _, path := range []string{"/", "/healthz", "/crm"} {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address+path, nil)
		if err != nil {
			return fmt.Errorf("неверный адрес")
		}
		response, err := copyClient.Do(request)
		if err != nil {
			return fmt.Errorf("публичный сайт ещё недоступен")
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 256<<10))
		response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("не удалось проверить сайт")
		}
		if path == "/crm" {
			if response.StatusCode != 401 || !strings.HasPrefix(response.Header.Get("WWW-Authenticate"), "Basic ") {
				return fmt.Errorf("CRM не отвечает или не защищена")
			}
		} else if response.StatusCode != 200 {
			return fmt.Errorf("сайт ещё не готов")
		}
		if path == "/" && !strings.Contains(string(body), `id="support-chat"`) {
			return fmt.Errorf("по публичному адресу отвечает другой сайт")
		}
	}
	return nil
}
