package client

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"ds2api/internal/auth"
	"ds2api/internal/config"
)

// DeepSeek applies rate-based login risk control per egress IP: observed in
// production, ~5 logins within ~8 seconds through one shared proxy trip
// RISK_DEVICE_DETECTED for every later login through that IP — both the
// plain-API fast path and the browser login service are rejected — and the
// flag only decays ~10 minutes after login attempts stop. Hammering the IP
// while it is flagged keeps it flagged, so the client must pace itself.
//
// The login gate enforces two rules per egress key (one key per proxy;
// accounts without a proxy share the "direct" egress):
//   - logins through the same egress are spaced at least loginMinInterval
//     apart; concurrent logins queue on reserved slots instead of firing
//     together;
//   - after a RISK_DEVICE_DETECTED rejection the egress enters a
//     loginRiskCooldown window during which attempts fail fast with
//     auth.ErrLoginThrottled instead of re-flagging the IP.
//
// Both durations are package vars so tests can shrink them. Operators can
// override them at startup with DS2API_LOGIN_MIN_INTERVAL and
// DS2API_LOGIN_RISK_COOLDOWN (Go duration syntax, e.g. "30s", "10m").
var (
	loginMinInterval  = envLoginDuration("DS2API_LOGIN_MIN_INTERVAL", 30*time.Second)
	loginRiskCooldown = envLoginDuration("DS2API_LOGIN_RISK_COOLDOWN", 10*time.Minute)
)

// envLoginDuration parses a positive duration from the environment, falling
// back to def (with a warning) when unset, malformed, or non-positive.
func envLoginDuration(name string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		config.Logger.Warn("[login_gate] ignoring invalid env value, using default",
			"env", name, "value", raw, "default", def.String())
		return def
	}
	return d
}

// loginGate paces Client.Login per egress key. The zero value is ready to
// use; per-key state is created lazily.
type loginGate struct {
	mu     sync.Mutex
	states map[string]*loginGateState
}

type loginGateState struct {
	nextSlot      time.Time // earliest start time of the next login
	cooldownUntil time.Time // egress stays throttled until this instant
}

// acquire reserves the next login slot for key and blocks until the slot
// opens. label is a log-safe description of the egress (proxy ID or
// "direct"); the raw key embeds proxy credentials and must never be logged
// or surfaced. Fails fast with auth.ErrLoginThrottled while the egress is
// cooling down after a risk-control rejection; returns ctx.Err() if the
// caller cancels while waiting for its slot.
func (g *loginGate) acquire(ctx context.Context, key, label string) error {
	now := time.Now()

	g.mu.Lock()
	st := g.stateLocked(key)
	if now.Before(st.cooldownUntil) {
		remaining := st.cooldownUntil.Sub(now).Round(time.Second)
		g.mu.Unlock()
		return fmt.Errorf("%w: egress %s in risk-control cooldown, retry in %s", auth.ErrLoginThrottled, label, remaining)
	}
	start := st.nextSlot
	if start.Before(now) {
		start = now
	}
	st.nextSlot = start.Add(loginMinInterval)
	g.mu.Unlock()

	if wait := start.Sub(now); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// markRisk puts key into risk-control cooldown: DeepSeek flagged the egress
// IP, so further attempts through it would only prolong the flag. They fail
// fast for loginRiskCooldown. Extend-only — a fresh rejection never
// shortens an active cooldown.
func (g *loginGate) markRisk(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.stateLocked(key)
	if until := time.Now().Add(loginRiskCooldown); until.After(st.cooldownUntil) {
		st.cooldownUntil = until
	}
}

// stateLocked returns (creating if needed) the state for key. Callers must
// hold g.mu.
func (g *loginGate) stateLocked(key string) *loginGateState {
	if st, ok := g.states[key]; ok {
		return st
	}
	if g.states == nil {
		g.states = make(map[string]*loginGateState)
	}
	st := &loginGateState{}
	g.states[key] = st
	return st
}
