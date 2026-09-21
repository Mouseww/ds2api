package client

import "testing"

// The refresh/recheck decision moved out of the client RPC loops into the
// shared failure policy (internal/completionruntime); what stays here is the
// response classification that decides which typed failure the policy sees.

func TestClassifyResponseFailureOnTokenInvalidSignal(t *testing.T) {
	failure := classifyResponseFailure("create session", 401, 0, 0, "unauthorized", "", true)
	if failure == nil || failure.Kind != FailureManagedUnauthorized {
		t.Fatalf("expected managed unauthorized on 401, got %#v", failure)
	}
	failure = classifyResponseFailure("create session", 401, 0, 0, "unauthorized", "", false)
	if failure == nil || failure.Kind != FailureDirectUnauthorized {
		t.Fatalf("expected direct unauthorized on 401 for direct tokens, got %#v", failure)
	}
}

func TestClassifyResponseFailureOnAuthIndicativeBizCodeFailure(t *testing.T) {
	failure := classifyResponseFailure("get pow", 200, 0, 400123, "", "login expired, token invalid", true)
	if failure == nil || failure.Kind != FailureManagedUnauthorized {
		t.Fatalf("expected managed unauthorized on auth-indicative biz failure, got %#v", failure)
	}
}

func TestClassifyResponseFailureNilOnNonAuthBizCodeFailure(t *testing.T) {
	if failure := classifyResponseFailure("get pow", 200, 0, 400123, "", "session create failed: quota reached", true); failure != nil {
		t.Fatalf("did not expect a typed failure on non-auth biz failure, got %#v", failure)
	}
}

func TestClassifyResponseFailureNilOnGenericServerError(t *testing.T) {
	if failure := classifyResponseFailure("create session", 500, 500, 0, "internal error", "", true); failure != nil {
		t.Fatalf("did not expect a typed failure on generic server error, got %#v", failure)
	}
}

func TestClassifyResponseFailureOnRateLimit(t *testing.T) {
	failure := classifyResponseFailure("create session", 429, 429, 0, "too many requests", "", true)
	if failure == nil || failure.Kind != FailureRateLimited {
		t.Fatalf("expected rate-limited failure on 429, got %#v", failure)
	}
}
