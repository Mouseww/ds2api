package client

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"ds2api/internal/account"
	"ds2api/internal/auth"
	"ds2api/internal/config"
	dsprotocol "ds2api/internal/deepseek/protocol"
	powpkg "ds2api/pow"
)

// newPolicyFreeTestClient builds a client whose HTTP layer is a scripted
// doer, plus a managed request lease from a real resolver/pool. The login
// recorder observes any lease-driven login (re-check / refresh / switch).
func newPolicyFreeTestClient(t *testing.T, respond func(call int, req *http.Request) (*http.Response, error)) (*Client, *auth.RequestAuth, *[]string) {
	t.Helper()
	t.Setenv("DS2API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@test.com","password":"pwd"},
			{"email":"acc2@test.com","password":"pwd"},
			{"email":"acc3@test.com","password":"pwd"}
		]
	}`)
	store := config.LoadStore()
	logins := &[]string{}
	resolver := auth.NewResolver(store, account.NewPool(store), func(_ context.Context, acc config.Account) (string, error) {
		*logins = append(*logins, acc.Identifier())
		return "fresh-" + acc.Identifier(), nil
	})
	req, _ := http.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer managed-key")
	a, err := resolver.Determine(req)
	if err != nil {
		t.Fatalf("determine failed: %v", err)
	}
	t.Cleanup(func() { resolver.Release(a) })

	call := 0
	client := &Client{
		Auth: resolver,
		regular: doerFunc(func(r *http.Request) (*http.Response, error) {
			call++
			return respond(call, r)
		}),
		fallback:   &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("fallback should not be used") })},
		maxRetries: 3,
	}
	return client, a, logins
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestCreateSessionCaptchaReturnsTypedFailureWithoutSwitching(t *testing.T) {
	client, a, logins := newPolicyFreeTestClient(t, func(call int, _ *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"code":1000,"msg":"captcha verification required"}`), nil
	})
	loginsBefore := len(*logins)

	_, err := client.CreateSession(context.Background(), a, 3)

	var failure *RequestFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected typed RequestFailure, got %T %v", err, err)
	}
	if failure.Kind != FailureCaptchaRequired {
		t.Fatalf("expected captcha failure kind, got %q", failure.Kind)
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("client must not switch accounts on captcha, lease moved to %q", a.AccountID)
	}
	if len(*logins) != loginsBefore {
		t.Fatalf("client must not drive re-check/refresh logins on captcha, logins=%v", *logins)
	}
}

func TestCreateSessionAuthFailureReturnsTypedFailureWithoutSwitching(t *testing.T) {
	client, a, logins := newPolicyFreeTestClient(t, func(call int, _ *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"code":40001,"msg":"unauthorized token"}`), nil
	})
	loginsBefore := len(*logins)

	_, err := client.CreateSession(context.Background(), a, 3)

	var failure *RequestFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected typed RequestFailure, got %T %v", err, err)
	}
	if failure.Kind != FailureManagedUnauthorized {
		t.Fatalf("expected managed unauthorized failure kind, got %q", failure.Kind)
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("client must not switch accounts on auth failure, lease moved to %q", a.AccountID)
	}
	if len(*logins) != loginsBefore {
		t.Fatalf("client must not drive re-check/refresh logins on auth failure, logins=%v", *logins)
	}
}

func TestCreateSessionRateLimitedReturnsTypedFailureWithoutSwitching(t *testing.T) {
	client, a, _ := newPolicyFreeTestClient(t, func(call int, _ *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusTooManyRequests, `{"code":429,"msg":"too many requests"}`), nil
	})

	_, err := client.CreateSession(context.Background(), a, 3)

	var failure *RequestFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected typed RequestFailure, got %T %v", err, err)
	}
	if failure.Kind != FailureRateLimited {
		t.Fatalf("expected rate-limited failure kind, got %q", failure.Kind)
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("client must not switch accounts on 429, lease moved to %q", a.AccountID)
	}
}

func TestUploadFileAuthFailureReturnsTypedFailureWithoutSwitching(t *testing.T) {
	challengeHash := powpkg.DeepSeekHashV1([]byte(powpkg.BuildPrefix("salt", 1712345678) + "42"))
	powResponse := `{"code":0,"msg":"ok","data":{"biz_code":0,"biz_data":{"challenge":{"algorithm":"DeepSeekHashV1","challenge":"` + hex.EncodeToString(challengeHash[:]) + `","salt":"salt","expire_at":1712345678,"difficulty":1000,"signature":"sig","target_path":"` + dsprotocol.DeepSeekUploadTargetPath + `"}}}}`
	calls := 0
	client, a, logins := newPolicyFreeTestClient(t, func(_ int, req *http.Request) (*http.Response, error) {
		calls++
		if strings.Contains(req.URL.Path, "pow") {
			return jsonResponse(http.StatusOK, powResponse), nil
		}
		return jsonResponse(http.StatusUnauthorized, `{"code":40001,"msg":"unauthorized token"}`), nil
	})
	loginsBefore := len(*logins)

	_, err := client.UploadFile(context.Background(), a, UploadFileRequest{
		Filename:    "demo.txt",
		ContentType: "text/plain",
		Purpose:     "assistants",
		Data:        []byte("hello"),
	}, 3)

	var failure *RequestFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected typed RequestFailure, got %T %v", err, err)
	}
	if failure.Kind != FailureManagedUnauthorized {
		t.Fatalf("expected managed unauthorized failure kind, got %q", failure.Kind)
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("client must not switch accounts on upload auth failure, lease moved to %q", a.AccountID)
	}
	if len(*logins) != loginsBefore {
		t.Fatalf("client must not drive refresh logins on upload auth failure, logins=%v", *logins)
	}
	if calls != 2 {
		t.Fatalf("expected one pow request and one upload request, got %d calls", calls)
	}
}

func TestGetPowAuthFailureReturnsTypedFailureWithoutSwitching(t *testing.T) {
	client, a, logins := newPolicyFreeTestClient(t, func(_ int, _ *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusForbidden, `{"code":40003,"msg":"token expired"}`), nil
	})
	loginsBefore := len(*logins)

	_, err := client.GetPow(context.Background(), a, 3)

	var failure *RequestFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected typed RequestFailure, got %T %v", err, err)
	}
	if failure.Kind != FailureManagedUnauthorized {
		t.Fatalf("expected managed unauthorized failure kind, got %q", failure.Kind)
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("client must not switch accounts on pow auth failure, lease moved to %q", a.AccountID)
	}
	if len(*logins) != loginsBefore {
		t.Fatalf("client must not drive refresh logins on pow auth failure, logins=%v", *logins)
	}
}

func TestGetSessionCountAuthFailureReturnsTypedFailureWithoutSwitching(t *testing.T) {
	client, a, logins := newPolicyFreeTestClient(t, func(_ int, _ *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"code":40001,"msg":"unauthorized token"}`), nil
	})
	loginsBefore := len(*logins)

	stats, err := client.GetSessionCount(context.Background(), a, 3)

	var failure *RequestFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected typed RequestFailure, got %T %v", err, err)
	}
	if failure.Kind != FailureManagedUnauthorized {
		t.Fatalf("expected managed unauthorized failure kind, got %q", failure.Kind)
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("client must not switch accounts on session count auth failure, lease moved to %q", a.AccountID)
	}
	if len(*logins) != loginsBefore {
		t.Fatalf("client must not drive refresh logins on session count auth failure, logins=%v", *logins)
	}
	if stats != nil && stats.Success {
		t.Fatalf("expected failed stats result, got %#v", stats)
	}
}

func TestDeleteSessionAuthFailureReturnsTypedFailureWithoutSwitching(t *testing.T) {
	client, a, logins := newPolicyFreeTestClient(t, func(_ int, _ *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"code":40001,"msg":"unauthorized token"}`), nil
	})
	loginsBefore := len(*logins)

	result, err := client.DeleteSession(context.Background(), a, "session-1", 3)

	var failure *RequestFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected typed RequestFailure, got %T %v", err, err)
	}
	if failure.Kind != FailureManagedUnauthorized {
		t.Fatalf("expected managed unauthorized failure kind, got %q", failure.Kind)
	}
	if a.AccountID != "acc1@test.com" {
		t.Fatalf("client must not switch accounts on delete session auth failure, lease moved to %q", a.AccountID)
	}
	if len(*logins) != loginsBefore {
		t.Fatalf("client must not drive refresh logins on delete session auth failure, logins=%v", *logins)
	}
	if result != nil && result.Success {
		t.Fatalf("expected failed delete result, got %#v", result)
	}
}
