// Package pace is a minimal politeness pacer. crawlkit provides no rate/backoff
// primitive and Instagram is aggressive about non-human traffic, so a
// single-session serial crawler waits a jittered delay between calls. There is
// deliberately NO retry loop: the reliability spine STOPS on RATE·LIMITED rather
// than grinding a soft block into a hard one.
package pace

import (
	"context"
	"math/rand"
	"time"
)

// Pacer waits a jittered delay in [Min, Max] between API calls.
type Pacer struct {
	Min   time.Duration
	Max   time.Duration
	rand  func() float64                             // 0..1; injectable for tests
	sleep func(context.Context, time.Duration) error // injectable for tests
}

// New returns a pacer with conservative jittered delays. Lowering these risks
// the account.
func New(minDelay, maxDelay time.Duration) *Pacer {
	return &Pacer{Min: minDelay, Max: maxDelay, rand: rand.Float64, sleep: sleepCtx}
}

// NewForTest returns a pacer whose waits are instant and deterministic.
func NewForTest() *Pacer {
	return &Pacer{
		rand:  func() float64 { return 0.5 },
		sleep: func(context.Context, time.Duration) error { return nil },
	}
}

// Wait blocks for a jittered delay, or returns early if the context is done.
func (p *Pacer) Wait(ctx context.Context) error {
	if p == nil || p.Max <= 0 {
		return nil
	}
	r := p.rand
	if r == nil {
		r = rand.Float64
	}
	s := p.sleep
	if s == nil {
		s = sleepCtx
	}
	d := p.Min + time.Duration(r()*float64(p.Max-p.Min))
	return s(ctx, d)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
