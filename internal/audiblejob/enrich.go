package audiblejob

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kodestar/audiosilo-bridge/internal/jobs"
	"github.com/kodestar/audiosilo-bridge/internal/link"
	"github.com/kodestar/audiosilo-bridge/internal/store"
)

// The enrichment's pace. A first poll can backfill hundreds of ASINs, one PUT
// each, and the server's address limiter (shared with the house's players)
// answered most of a quick run 429. So the PUTs go out at most one per
// enrichGap, a 429 waits its Retry-After (else enrichBackoff, doubling) and
// slows the pace, and what the poll can't finish waits for the next one: those
// books still have no ASIN, so the next poll picks them up again.
const (
	// enrichGap is the shortest time between two enrichment PUTs (5 a second).
	enrichGap = 200 * time.Millisecond
	// enrichMaxGap is the slowest pace 429s push the gap to.
	enrichMaxGap = 2 * time.Second
	// enrichBackoff is the first wait after a 429 without a Retry-After; each
	// consecutive 429 doubles it, up to enrichMaxWait.
	enrichBackoff = 2 * time.Second
	// enrichMaxWait caps one wait: a Retry-After above it leaves the rest of
	// the backfill to the next poll rather than holding the job.
	enrichMaxWait = time.Minute
	// enrichMaxLimited is how many 429s in a row leave the rest to the next poll.
	enrichMaxLimited = 5
	// enrichBudget is the longest one poll spends enriching.
	enrichBudget = 10 * time.Minute
)

// hit is a matched book to give its title's ASIN.
type hit struct {
	path, asin string
}

// enrich backfills each hit's ASIN, paced (see enrichGap), counting into res:
// Enriched, EnrichFailed, and EnrichDeferred for the books left to the next
// poll. Only a cancelled job is an error.
func (h *Handler) enrich(ctx context.Context, srv Server, acct store.AudibleAccount, hits []hit, rep *jobs.Reporter, res *Result) error {
	if len(hits) == 0 {
		return nil
	}
	msg := "Adding the ASINs of the matched books"
	rep.Progress(jobs.Progress{Step: "enriching", Message: msg, Total: len(hits)})
	deadline := h.now().Add(enrichBudget)
	gap, limited := enrichGap, 0
	for i := 0; i < len(hits); {
		if i > 0 && limited == 0 { // a retry after a 429 has waited already
			if err := h.sleep(ctx, gap); err != nil {
				return err
			}
		}
		if !h.now().Before(deadline) {
			res.EnrichDeferred = len(hits) - i
			break
		}
		err := srv.Enrich(ctx, acct.LibraryID, hits[i].path, hits[i].asin)
		if retry, ok := rateLimited(err); ok {
			limited++
			wait, ok := limitWait(retry, limited)
			if !ok {
				res.EnrichDeferred = len(hits) - i
				break
			}
			gap = min(gap*2, enrichMaxGap)
			rep.Progress(jobs.Progress{Step: "enriching", Message: "The server asked the Bridge to slow down: waiting", Done: i, Total: len(hits)})
			if err := h.sleep(ctx, wait); err != nil {
				return err
			}
			continue // the same book again
		}
		limited = 0
		switch {
		case err == nil:
			res.Enriched++
		case errors.Is(err, link.ErrASINSet), errors.Is(err, link.ErrNoBook):
			// Another ASIN arrived meanwhile, or the book moved: the next poll sees it.
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			res.EnrichFailed++
			h.o.Log.Warn("audible: adding a book's ASIN failed", "account", acct.ID, "library", acct.LibraryID, "err", err)
		}
		i++
		rep.Progress(jobs.Progress{Step: "enriching", Message: msg, Done: i, Total: len(hits)})
	}
	if res.EnrichDeferred > 0 {
		h.o.Log.Info("audible: the rest of the ASIN backfill waits for the next poll", "account", acct.ID,
			"enriched", res.Enriched, "deferred", res.EnrichDeferred)
	}
	return nil
}

// limitWait is the wait before an enrichment PUT goes again after its
// limited-th 429 (e) in a row: the server's Retry-After when it sent one (an
// explicit zero is an immediate retry), else enrichBackoff doubling. false
// leaves the PUT: enrichMaxLimited 429s in a row, or a wait over enrichMaxWait.
func limitWait(e *link.APIError, limited int) (time.Duration, bool) {
	retry := e.RetryAfter
	if !e.RetryAfterSet {
		retry = enrichBackoff << (limited - 1)
	}
	if limited >= enrichMaxLimited || retry > enrichMaxWait {
		return 0, false
	}
	return retry, true
}

// rateLimited reports whether err is the server's 429 (for limitWait).
func rateLimited(err error) (*link.APIError, bool) {
	var e *link.APIError
	if errors.As(err, &e) && e.Status == http.StatusTooManyRequests {
		return e, true
	}
	return nil, false
}

// sleepCtx waits d, or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
