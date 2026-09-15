package quota

import (
	"context"
	"log/slog"
	"time"
)

const (
	// DefaultInterval is how often the bar is refreshed. The windows it shows
	// move over hours, so this is about feeling live, not about precision.
	DefaultInterval = 60 * time.Second

	// defaultBackoffStep is the first pause after a failed provider fetch; it
	// doubles up to defaultBackoffMax. Endpoints we do not own get backed off
	// hard, never hammered once a minute forever.
	defaultBackoffStep = 5 * time.Minute
	defaultBackoffMax  = 30 * time.Minute
)

// Poller refreshes every configured provider on a fixed cadence, off the render
// path. Providers are independent: one that errors never blocks another.
type Poller struct {
	Store    *Store
	Fetchers []Fetcher
	Interval time.Duration
	Logger   *slog.Logger
	// Now is an injectable clock for the backoff decision.
	Now func() time.Time

	failures map[string]int
	nextTry  map[string]time.Time
}

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Poller) interval() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return DefaultInterval
}

// Refresh fetches every provider that is not inside its backoff window.
func (p *Poller) Refresh(ctx context.Context) {
	if p.failures == nil {
		p.failures = make(map[string]int)
		p.nextTry = make(map[string]time.Time)
	}

	now := p.now()
	for _, f := range p.Fetchers {
		provider := f.Provider()
		if next, ok := p.nextTry[provider]; ok && now.Before(next) {
			continue
		}

		snap, err := f.Fetch(ctx)
		if err != nil {
			p.penalize(provider, now)
			if errorIsMissingCredentials(err) {
				// Not signed in is not a state the user needs reported: the
				// provider is simply not shown. Drop any cached snapshot too,
				// or a signed-out account keeps painting last session's usage.
				p.Store.Delete(provider)
				continue
			}
			if p.Logger != nil {
				p.Logger.Debug("quota_fetch_failed", slog.String("provider", provider), slog.String("error", err.Error()))
			}
			p.Store.Set(Snapshot{Provider: provider, Err: err.Error(), UpdatedAt: now})
			continue
		}

		delete(p.failures, provider)
		delete(p.nextTry, provider)
		snap.Provider = provider
		snap.UpdatedAt = now
		p.Store.Set(snap)
	}

	if err := p.Store.Save(); err != nil && p.Logger != nil {
		p.Logger.Debug("quota_cache_save_failed", slog.String("error", err.Error()))
	}
}

func (p *Poller) penalize(provider string, now time.Time) {
	p.failures[provider]++
	delay := defaultBackoffStep << (p.failures[provider] - 1)
	if delay > defaultBackoffMax || delay <= 0 {
		delay = defaultBackoffMax
	}
	p.nextTry[provider] = now.Add(delay)
}

// Run refreshes immediately, then on every Interval tick until ctx is done.
func (p *Poller) Run(ctx context.Context) {
	p.Refresh(ctx)

	ticker := time.NewTicker(p.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Refresh(ctx)
		}
	}
}
