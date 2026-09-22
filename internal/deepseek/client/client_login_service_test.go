package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ds2api/internal/config"
)

func TestLoginWithServiceReturnsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]string
		_ = json.Unmarshal(body, &payload)
		if payload["email"] != "a@test.com" || payload["password"] != "pw" {
			t.Fatalf("unexpected payload: %v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"token":"tok-123"}`))
	}))
	defer srv.Close()

	c := &Client{}
	acc := config.Account{Email: "a@test.com", Password: "pw"}
	token, err := c.loginWithService(context.Background(), srv.URL, acc)
	if err != nil {
		t.Fatalf("loginWithService error: %v", err)
	}
	if token != "tok-123" {
		t.Fatalf("token=%q want %q", token, "tok-123")
	}
}

func TestLoginWithServicePropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"success":false,"error":"RISK_DEVICE_DETECTED"}`))
	}))
	defer srv.Close()

	c := &Client{}
	acc := config.Account{Email: "a@test.com", Password: "pw"}
	_, err := c.loginWithService(context.Background(), srv.URL, acc)
	if err == nil || !strings.Contains(err.Error(), "RISK_DEVICE_DETECTED") {
		t.Fatalf("expected service error propagated, got %v", err)
	}
}

func TestLoginWithServiceMissingCredentials(t *testing.T) {
	c := &Client{}
	_, err := c.loginWithService(context.Background(), "http://example.com", config.Account{Password: "pw"})
	if err == nil || !strings.Contains(err.Error(), "missing email/mobile") {
		t.Fatalf("expected missing email/mobile error, got %v", err)
	}
}

func TestLoginWithServiceMobile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]string
		_ = json.Unmarshal(body, &payload)
		if payload["mobile"] != "13800138000" {
			t.Fatalf("unexpected mobile payload: %v", payload)
		}
		_, _ = w.Write([]byte(`{"success":true,"token":"tok-m"}`))
	}))
	defer srv.Close()

	c := &Client{}
	acc := config.Account{Mobile: "13800138000", Password: "pw"}
	token, err := c.loginWithService(context.Background(), srv.URL, acc)
	if err != nil {
		t.Fatalf("loginWithService error: %v", err)
	}
	if token != "tok-m" {
		t.Fatalf("token=%q want %q", token, "tok-m")
	}
}

func TestLoginServiceURLUnconfigured(t *testing.T) {
	c := &Client{}
	if url := c.loginServiceURL(); url != "" {
		t.Fatalf("expected empty login service URL, got %q", url)
	}
}
