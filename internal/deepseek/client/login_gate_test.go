package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ds2api/internal/auth"
)

// overrideLoginGateTiming shrinks the gate durations for the test and
// restores them on cleanup (same pattern as loginDeviceRotationPause).
func overrideLoginGateTiming(t *testing.T, interval, cooldown time.Duration) {
	t.Helper()
	origInterval, origCooldown := loginMinInterval, loginRiskCooldown
	loginMinInterval, loginRiskCooldown = interval, cooldown
	t.Cleanup(func() { loginMinInterval, loginRiskCooldown = origInterval, origCooldown })
}

// TestLoginGateSpacesLoginsPerKey guards the spacing rule: the first login
// through an egress starts immediately, the second must wait out the
// minimum interval.
func TestLoginGateSpacesLoginsPerKey(t *testing.T) {
	overrideLoginGateTiming(t, 80*time.Millisecond, time.Minute)
	g := &loginGate{}

	if err := g.acquire(context.Background(), "k", "p1"); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	start := time.Now()
	if err := g.acquire(context.Background(), "k", "p1"); err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if waited := time.Since(start); waited < 70*time.Millisecond {
		t.Fatalf("second acquire should wait out the spacing interval, waited %s", waited)
	}
}

// TestLoginGateDifferentKeysDoNotBlockEachOther guards egress isolation:
// accounts on different proxies must not inherit each other's spacing.
func TestLoginGateDifferentKeysDoNotBlockEachOther(t *testing.T) {
	overrideLoginGateTiming(t, 5*time.Second, time.Minute)
	g := &loginGate{}

	if err := g.acquire(context.Background(), "k1", "p1"); err != nil {
		t.Fatalf("first key acquire: %v", err)
	}
	start := time.Now()
	if err := g.acquire(context.Background(), "k2", "p2"); err != nil {
		t.Fatalf("second key acquire: %v", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("different egress must not inherit spacing, waited %s", waited)
	}
}

// TestLoginGateCooldownFailsFast guards the cooldown rule: after a
// risk-control rejection, further logins through the egress fail fast with
// auth.ErrLoginThrottled, and the error names the log-safe label instead of
// the raw key (which embeds proxy credentials).
func TestLoginGateCooldownFailsFast(t *testing.T) {
	overrideLoginGateTiming(t, time.Second, time.Minute)
	g := &loginGate{}

	rawKey := "proxy-1|socks5|warp|1080|user|SECRET-PASSWORD"
	g.markRisk(rawKey)

	start := time.Now()
	err := g.acquire(context.Background(), rawKey, "proxy-1")
	if waited := time.Since(start); waited > 500*time.Millisecond {
		t.Fatalf("cooldown acquire must fail fast, took %s", waited)
	}
	if !errors.Is(err, auth.ErrLoginThrottled) {
		t.Fatalf("expected auth.ErrLoginThrottled, got %v", err)
	}
	if !strings.Contains(err.Error(), "proxy-1") {
		t.Fatalf("error should name the egress label, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "SECRET-PASSWORD") {
		t.Fatal("error must not leak the raw key (contains proxy credentials)")
	}
}

// TestLoginGateContextCancelWhileWaiting guards cancellation: a waiter
// blocked on the spacing interval returns ctx.Err() promptly when the
// caller cancels.
func TestLoginGateContextCancelWhileWaiting(t *testing.T) {
	overrideLoginGateTiming(t, time.Minute, time.Minute)
	g := &loginGate{}

	if err := g.acquire(context.Background(), "k", "p1"); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := g.acquire(ctx, "k", "p1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("cancelled waiter should return promptly, took %s", waited)
	}
}

// TestLoginMarksGateOnRiskRejection guards the Client.Login wiring: a
// RISK_DEVICE_DETECTED failure puts the egress into cooldown, so an
// immediate second login fails fast with the throttle error instead of
// hammering the flagged IP.
func TestLoginMarksGateOnRiskRejection(t *testing.T) {
	overrideLoginGateTiming(t, time.Second, time.Minute)
	origPause := loginDeviceRotationPause
	loginDeviceRotationPause = 0
	t.Cleanup(func() { loginDeviceRotationPause = origPause })

	client, _ := newLoginRiskTestClient(t, func(_ string, _ int) string {
		return `{"code":40001,"msg":"RISK_DEVICE_DETECTED"}`
	})
	acc, ok := client.Store.FindAccount("risk@test.com")
	if !ok {
		t.Fatal("expected seeded account")
	}

	_, err := client.Login(context.Background(), acc)
	if !isRiskDeviceDetected(err) {
		t.Fatalf("expected risk rejection from first login, got %v", err)
	}

	_, err = client.Login(context.Background(), acc)
	if !errors.Is(err, auth.ErrLoginThrottled) {
		t.Fatalf("second login through flagged egress should be throttled, got %v", err)
	}
}

// TestLoginGateZeroValueReady guards that a freshly constructed Client (as
// built in tests and by NewClient) can log in without initializing gate
// state manually.
func TestLoginGateZeroValueReady(t *testing.T) {
	overrideLoginGateTiming(t, time.Millisecond, time.Millisecond)
	g := &loginGate{}
	if err := g.acquire(context.Background(), "k", "p1"); err != nil {
		t.Fatalf("zero-value gate acquire: %v", err)
	}
	g.markRisk("k")
	if err := g.acquire(context.Background(), "k", "p1"); !errors.Is(err, auth.ErrLoginThrottled) {
		t.Fatalf("zero-value gate markRisk/acquire: %v", err)
	}
}
