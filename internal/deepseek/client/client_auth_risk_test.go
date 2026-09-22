package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ds2api/internal/config"
)

func TestIsRiskDeviceDetected(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"msg level", fmt.Errorf("login failed: %v", "RISK_DEVICE_DETECTED"), true},
		{"biz level", errors.New("login failed: risk_device_detected"), true},
		{"embedded in longer text", errors.New("login failed: 检测到风险设备 (RISK_DEVICE_DETECTED)，请稍后重试"), true},
		{"ordinary failure", errors.New("login failed: 密码错误"), false},
		{"empty message", errors.New("login failed: %!v(<nil>)"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRiskDeviceDetected(tc.err); got != tc.want {
				t.Fatalf("isRiskDeviceDetected(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// loginRiskRecorder fakes the DeepSeek HTTP surface for login tests: it
// captures the device_id of every login request and answers from
// respondLogin. Non-login endpoints (client settings) get a generic success
// payload so no fallback transport is ever exercised.
type loginRiskRecorder struct {
	mu          sync.Mutex
	loginDevice []string
	respond     func(deviceID string, attempt int) string
}

func (r *loginRiskRecorder) Do(req *http.Request) (*http.Response, error) {
	if !strings.Contains(req.URL.Path, "/users/login") {
		return loginRiskJSONResponse(req, `{"code":0,"data":{"biz_code":0,"biz_data":{}}}`), nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	deviceID, _ := payload["device_id"].(string)
	r.mu.Lock()
	attempt := len(r.loginDevice)
	r.loginDevice = append(r.loginDevice, deviceID)
	r.mu.Unlock()
	return loginRiskJSONResponse(req, r.respond(deviceID, attempt)), nil
}

func (r *loginRiskRecorder) loginDevices() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.loginDevice...)
}

func loginRiskJSONResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

const loginRiskSuccessBody = `{"code":0,"data":{"biz_code":0,"biz_data":{"user":{"token":"tok-fresh","id":"sso-1","chat":{"is_muted":0}}}}}`

func newLoginRiskTestClient(t *testing.T, respond func(deviceID string, attempt int) string) (*Client, *loginRiskRecorder) {
	t.Helper()
	t.Setenv("DS2API_CONFIG_JSON", `{"keys":["k"],"accounts":[{"email":"risk@test.com","password":"pw"}]}`)
	store := config.LoadStore()
	rec := &loginRiskRecorder{respond: respond}
	return &Client{Store: store, regular: rec, fallback: &http.Client{}}, rec
}

func TestLoginRotatesDeviceFingerprintOnRiskDeviceDetected(t *testing.T) {
	origPause := loginDeviceRotationPause
	loginDeviceRotationPause = 0
	t.Cleanup(func() { loginDeviceRotationPause = origPause })

	client, rec := newLoginRiskTestClient(t, func(deviceID string, attempt int) string {
		if attempt == 0 {
			return `{"code":40001,"msg":"RISK_DEVICE_DETECTED"}`
		}
		return loginRiskSuccessBody
	})

	acc, ok := client.Store.FindAccount("risk@test.com")
	if !ok {
		t.Fatal("expected seeded account")
	}
	token, err := client.Login(context.Background(), acc)
	if err != nil {
		t.Fatalf("Login error: %v", err)
	}
	if token != "tok-fresh" {
		t.Fatalf("token=%q want %q", token, "tok-fresh")
	}

	devices := rec.loginDevices()
	if len(devices) != 2 {
		t.Fatalf("expected 2 login attempts, got %d (%v)", len(devices), devices)
	}
	if devices[0] == devices[1] {
		t.Fatalf("expected rotated device fingerprint, both attempts used %q", devices[0])
	}
	stored, _ := client.Store.FindAccount("risk@test.com")
	if stored.DeviceID != devices[1] {
		t.Fatalf("persisted device_id=%q want rotated %q", stored.DeviceID, devices[1])
	}
}

func TestLoginRotatesDeviceFingerprintOnBizLevelRiskRejection(t *testing.T) {
	origPause := loginDeviceRotationPause
	loginDeviceRotationPause = 0
	t.Cleanup(func() { loginDeviceRotationPause = origPause })

	client, rec := newLoginRiskTestClient(t, func(deviceID string, attempt int) string {
		if attempt == 0 {
			return `{"code":0,"data":{"biz_code":40001,"biz_msg":"RISK_DEVICE_DETECTED"}}`
		}
		return loginRiskSuccessBody
	})

	acc, _ := client.Store.FindAccount("risk@test.com")
	token, err := client.Login(context.Background(), acc)
	if err != nil {
		t.Fatalf("Login error: %v", err)
	}
	if token != "tok-fresh" {
		t.Fatalf("token=%q want %q", token, "tok-fresh")
	}
	if devices := rec.loginDevices(); len(devices) != 2 {
		t.Fatalf("expected 2 login attempts, got %d", len(devices))
	}
}

func TestLoginGivesUpAfterDeviceRotationLimit(t *testing.T) {
	origPause := loginDeviceRotationPause
	loginDeviceRotationPause = 0
	t.Cleanup(func() { loginDeviceRotationPause = origPause })

	client, rec := newLoginRiskTestClient(t, func(_ string, _ int) string {
		return `{"code":40001,"msg":"RISK_DEVICE_DETECTED"}`
	})

	acc, _ := client.Store.FindAccount("risk@test.com")
	_, err := client.Login(context.Background(), acc)
	if err == nil {
		t.Fatal("expected Login to fail when every fingerprint is rejected")
	}
	if !isRiskDeviceDetected(err) {
		t.Fatalf("expected RISK_DEVICE_DETECTED error, got %v", err)
	}
	devices := rec.loginDevices()
	if len(devices) != loginDeviceRotationLimit+1 {
		t.Fatalf("expected %d login attempts, got %d", loginDeviceRotationLimit+1, len(devices))
	}
	if devices[0] == devices[len(devices)-1] {
		t.Fatalf("expected distinct fingerprints across attempts, all %q", devices[0])
	}
}

func TestLoginDoesNotRotateDeviceOnOrdinaryFailure(t *testing.T) {
	client, rec := newLoginRiskTestClient(t, func(_ string, _ int) string {
		return `{"code":40001,"msg":"密码错误"}`
	})

	acc, _ := client.Store.FindAccount("risk@test.com")
	_, err := client.Login(context.Background(), acc)
	if err == nil || !strings.Contains(err.Error(), "密码错误") {
		t.Fatalf("expected password error, got %v", err)
	}
	devices := rec.loginDevices()
	if len(devices) != 1 {
		t.Fatalf("expected 1 login attempt, got %d", len(devices))
	}
	stored, _ := client.Store.FindAccount("risk@test.com")
	if stored.DeviceID != devices[0] {
		t.Fatalf("persisted device_id=%q want %q (no rotation)", stored.DeviceID, devices[0])
	}
}

func TestAccountTimezoneOffset_SameAccountStable(t *testing.T) {
	a := accountTimezoneOffset("account-a")
	b := accountTimezoneOffset("account-a")
	if a != b {
		t.Fatalf("same account should map to stable timezone, got %d then %d", a, b)
	}
}

func TestAccountTimezoneOffset_DefaultForEmpty(t *testing.T) {
	if got := accountTimezoneOffset(""); got != 28800 {
		t.Fatalf("empty identifier should default to UTC+8, got %d", got)
	}
}

func TestAccountTimezoneOffset_DifferentAccountsSpread(t *testing.T) {
	seen := map[int]bool{}
	same := 0
	for i := 0; i < 100; i++ {
		o := accountTimezoneOffset(fmt.Sprintf("acct-%d@test.com", i))
		seen[o] = true
		if i > 0 && o == accountTimezoneOffset("acct-0@test.com") {
			same++
		}
	}
	if len(seen) < 3 {
		t.Fatalf("expected timezone spread across at least 3 offsets, got %d: %v", len(seen), seen)
	}
}

func TestLoginHeaders_PerAccountTimezoneHeader(t *testing.T) {
	acc1 := config.Account{Email: "a@test.com"}
	acc2 := config.Account{Email: "zzz@other.com"}

	h1 := loginHeaders(acc1)
	h2 := loginHeaders(acc2)

	tz1 := h1["x-client-timezone-offset"]
	tz2 := h2["x-client-timezone-offset"]

	if tz1 == "" || tz2 == "" {
		t.Fatal("x-client-timezone-offset missing from login headers")
	}

	// The two accounts may or may not hash to the same offset; the point is
	// that headers are well-formed and the timezone is some sensible integer.
	for _, v := range []string{tz1, tz2} {
		n, err := strconv.Atoi(v)
		if err != nil || n == 0 {
			t.Fatalf("timezone offset %q is not a valid non-zero integer", v)
		}
	}
}
