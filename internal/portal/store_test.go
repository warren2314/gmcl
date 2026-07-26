package portal

import (
	"testing"
	"time"
)

func TestDefaultSessionPolicy(t *testing.T) {
	policy := DefaultSessionPolicy()
	if err := policy.Validate(); err != nil {
		t.Fatalf("default policy is invalid: %v", err)
	}
	if policy.IdleLifetime != 30*time.Minute {
		t.Fatalf("idle lifetime = %s", policy.IdleLifetime)
	}
	if policy.AbsoluteLifetime != 12*time.Hour {
		t.Fatalf("absolute lifetime = %s", policy.AbsoluteLifetime)
	}
}

func TestSessionPolicyValidation(t *testing.T) {
	valid := DefaultSessionPolicy()
	tests := []struct {
		name   string
		mutate func(*SessionPolicy)
	}{
		{"short idle", func(p *SessionPolicy) { p.IdleLifetime = time.Minute }},
		{"absolute shorter than idle", func(p *SessionPolicy) { p.AbsoluteLifetime = 10 * time.Minute }},
		{"absolute too long", func(p *SessionPolicy) { p.AbsoluteLifetime = 25 * time.Hour }},
		{"zero touch", func(p *SessionPolicy) { p.TouchInterval = 0 }},
		{"touch longer than idle", func(p *SessionPolicy) { p.TouchInterval = p.IdleLifetime }},
		{"zero step up", func(p *SessionPolicy) { p.StepUpLifetime = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := valid
			tt.mutate(&policy)
			if err := policy.Validate(); err == nil {
				t.Fatal("invalid policy unexpectedly passed validation")
			}
		})
	}
}

func TestLoadSessionPolicyFromEnv(t *testing.T) {
	t.Setenv("CLUB_PORTAL_SESSION_IDLE_MINUTES", "45")
	t.Setenv("CLUB_PORTAL_SESSION_ABSOLUTE_HOURS", "10")
	t.Setenv("CLUB_PORTAL_STEP_UP_MINUTES", "12")
	policy, err := LoadSessionPolicyFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if policy.IdleLifetime != 45*time.Minute ||
		policy.AbsoluteLifetime != 10*time.Hour ||
		policy.StepUpLifetime != 12*time.Minute {
		t.Fatalf("unexpected environment policy: %#v", policy)
	}
}

func TestLoadSessionPolicyRejectsInvalidEnvironment(t *testing.T) {
	t.Setenv("CLUB_PORTAL_SESSION_IDLE_MINUTES", "not-a-number")
	if _, err := LoadSessionPolicyFromEnv(); err == nil {
		t.Fatal("invalid session environment unexpectedly accepted")
	}
}

func TestPrincipalRequiresStepUp(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	policy := DefaultSessionPolicy()
	principal := Principal{}
	if !principal.RequiresStepUp(now, policy) {
		t.Fatal("missing step-up timestamp was accepted")
	}
	recent := now.Add(-5 * time.Minute)
	principal.StepUpAt = &recent
	if principal.RequiresStepUp(now, policy) {
		t.Fatal("recent step-up was rejected")
	}
	old := now.Add(-20 * time.Minute)
	principal.StepUpAt = &old
	if !principal.RequiresStepUp(now, policy) {
		t.Fatal("stale step-up was accepted")
	}
}

func TestSanitizeUserAgent(t *testing.T) {
	if sanitizeUserAgent("  ") != nil {
		t.Fatal("blank user agent was retained")
	}
	long := make([]byte, 600)
	for i := range long {
		long[i] = 'a'
	}
	value, ok := sanitizeUserAgent(string(long)).(string)
	if !ok || len(value) != 512 {
		t.Fatalf("sanitized user agent length = %d", len(value))
	}
}
