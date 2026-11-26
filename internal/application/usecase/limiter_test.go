package usecase_test

import (
	"sync"
	"testing"
	"time"

	"github.com/egkike/crm-ai-business/internal/application/usecase"
)

// TestMemoryLimiterAllowsLimitThenRejects pins the budget arithmetic: exactly N
// attempts per window are admitted, the N+1th is refused, and a refusal consumes
// nothing (otherwise the budget could never recover inside the window).
func TestMemoryLimiterAllowsLimitThenRejects(t *testing.T) {
	const limit = 3
	limiter := usecase.NewMemoryRegistrationLimiter(limit)

	for attempt := 1; attempt <= limit; attempt++ {
		if !limiter.Allow("+5491100000001") {
			t.Fatalf("intento %d rechazado, se esperaban %d admitidos", attempt, limit)
		}
	}
	for attempt := limit + 1; attempt <= limit+3; attempt++ {
		if limiter.Allow("+5491100000001") {
			t.Fatalf("intento %d admitido, se esperaba el rechazo tras el límite %d", attempt, limit)
		}
	}
}

// TestMemoryLimiterIsPerIdentifier pins the RF3 scope: the budget is per channel
// identifier, so one exhausted sender cannot block another.
func TestMemoryLimiterIsPerIdentifier(t *testing.T) {
	limiter := usecase.NewMemoryRegistrationLimiter(1)

	if !limiter.Allow("+5491100000001") {
		t.Fatal("el primer identificador debía admitirse")
	}
	if limiter.Allow("+5491100000001") {
		t.Fatal("el segundo intento del mismo identificador debía rechazarse")
	}
	if !limiter.Allow("+5491100000002") {
		t.Fatal("otro identificador debía tener su propio presupuesto")
	}
}

// TestMemoryLimiterWindowResetsBudget pins the window: a new window restores the
// whole budget, and the window boundary itself is the reset (now >= resetAt).
func TestMemoryLimiterWindowResetsBudget(t *testing.T) {
	const window = time.Hour
	now := time.Date(2026, time.November, 26, 10, 0, 0, 0, time.UTC)
	limiter := usecase.NewMemoryRegistrationLimiterWithClock(1, window, func() time.Time { return now })

	if !limiter.Allow("+5491100000001") {
		t.Fatal("el primer intento debía admitirse")
	}
	if limiter.Allow("+5491100000001") {
		t.Fatal("el segundo intento dentro de la ventana debía rechazarse")
	}

	now = now.Add(window - time.Second)
	if limiter.Allow("+5491100000001") {
		t.Fatal("un intento un segundo antes del reset debía rechazarse")
	}

	now = now.Add(time.Second)
	if !limiter.Allow("+5491100000001") {
		t.Fatal("el intento en el borde de la ventana debía admitirse (ventana nueva)")
	}
}

// TestMemoryLimiterRejectsEmptyIdentifier pins the fail-closed guard: an empty
// identifier has no budget and must never be admitted (it would share one bucket
// with every other anonymous request).
func TestMemoryLimiterRejectsEmptyIdentifier(t *testing.T) {
	limiter := usecase.NewMemoryRegistrationLimiter(10)
	if limiter.Allow("") {
		t.Fatal("Allow(\"\") debía rechazarse")
	}
}

// TestMemoryLimiterNonPositiveWindowFallsBack pins the defensive constructor: a
// window of zero or a nil clock still yields a bounded limiter instead of a
// panicking or unbounded one.
func TestMemoryLimiterNonPositiveWindowFallsBack(t *testing.T) {
	limiter := usecase.NewMemoryRegistrationLimiterWithClock(1, 0, nil)
	if !limiter.Allow("+5491100000001") {
		t.Fatal("el primer intento debía admitirse")
	}
	if limiter.Allow("+5491100000001") {
		t.Fatal("el segundo intento debía rechazarse dentro de la ventana por defecto")
	}

	since := time.Now()
	limiter = usecase.NewMemoryRegistrationLimiterWithClock(1, -time.Minute, nil)
	if !limiter.Allow("+5491100000002") {
		t.Fatalf("el primer intento debía admitirse (%v)", since)
	}
}

// TestMemoryLimiterConcurrentAllowIsSafe pins the mutex: concurrent attempts over
// one identifier admit exactly the budget, never more. Run under -race this is
// also the proof that the counter is not shared unsafely.
func TestMemoryLimiterConcurrentAllowIsSafe(t *testing.T) {
	const (
		limit   = 10
		callers = 64
	)
	limiter := usecase.NewMemoryRegistrationLimiter(limit)

	var (
		mu       sync.Mutex
		admitted int
		wg       sync.WaitGroup
	)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if limiter.Allow("+5491100000001") {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if admitted != limit {
		t.Fatalf("admitidos = %d, se esperaban exactamente %d", admitted, limit)
	}
}
