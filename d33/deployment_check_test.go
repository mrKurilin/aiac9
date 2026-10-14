package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type deploymentTransport func(*http.Request) (*http.Response, error)

func (f deploymentTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeploymentCheckValidatesWebsiteAndProtectedCRM(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		var paths []string
		client := &http.Client{Transport: deploymentTransport(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Authorization") != "" {
				t.Fatal("deployment check sent credentials")
			}
			paths = append(paths, r.URL.Path)
			response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`<section id="support-chat"></section>`))}
			if r.URL.Path == "/crm" {
				response.StatusCode = 401
				response.Header.Set("WWW-Authenticate", `Basic realm="Garage 33 CRM"`)
			}
			return response, nil
		})}
		if err := checkDeployment(context.Background(), scheme+"://garage.example.com", client); err != nil {
			t.Fatal(err)
		}
		if strings.Join(paths, ",") != "/,/healthz,/crm" {
			t.Fatalf("paths: %v", paths)
		}
	}
}

func TestDeploymentCheckRejectsUnsafeAddressesBeforeRequest(t *testing.T) {
	credentialURL := &url.URL{Scheme: "http", Host: "garage.example.com", User: url.UserPassword("user", "placeholder")}
	client := &http.Client{Transport: deploymentTransport(func(*http.Request) (*http.Response, error) { t.Fatal("unsafe address was requested"); return nil, nil })}
	for _, address := range []string{"http://127.0.0.1", "file:///tmp", "https://garage.example.com/path", "http://garage.example.com:8080", credentialURL.String()} {
		if err := checkDeployment(context.Background(), address, client); err == nil {
			t.Fatal("unsafe address accepted")
		}
	}
}

func TestDeploymentCheckDoesNotReportUnprotectedCRMAsReady(t *testing.T) {
	client := &http.Client{Transport: deploymentTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`<section id="support-chat"></section>`))}, nil
	})}
	if err := checkDeployment(context.Background(), "http://garage.example.com", client); err == nil {
		t.Fatal("unprotected CRM reported ready")
	}
}

func TestHealthcheckUsesConfiguredLocalPort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatal("wrong health path")
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	t.Setenv("WEB_ADDR", strings.TrimPrefix(server.URL, "http://"))
	if err := healthcheck("web"); err != nil {
		t.Fatal(err)
	}
	if err := healthcheck("wrong"); err == nil {
		t.Fatal("unknown service accepted")
	}
}
