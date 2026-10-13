package handler

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// SlidingWindowRateLimit configures a per-key sliding-window limiter.
type SlidingWindowRateLimit struct {
	Limit  int           // maximum requests per window; zero disables the gate
	Window time.Duration // sliding window length
}

// SlidingWindowRateLimiter is the contract implemented by both the in-memory
// and Redis-backed limiters.
//
// Implementations may expose non-consuming checks and retry hints through the
// optional interfaces below. Allow consumes one unit.
type SlidingWindowRateLimiter interface {
	Allow(ctx context.Context, key string) bool
	// Implementations may also satisfy slidingWindowRateLimiterInspector. Keeping
	// the base contract at Allow lets tests inject simple one-method gates.
}

type slidingWindowRateLimiterWithError interface {
	AllowWithError(ctx context.Context, key string) (bool, error)
}

type slidingWindowRateLimiterInspector interface {
	Check(ctx context.Context, key string) bool
	RetryAfter(ctx context.Context, key string) time.Duration
}

type slidingWindowRateLimiterCheckWithError interface {
	CheckWithError(ctx context.Context, key string) (bool, error)
}

// slidingWindowLimiterCheckWithError exposes check failures to callers, such
// as invitation admission, that must fail closed without consuming a budget.
func slidingWindowLimiterCheckWithError(ctx context.Context, limiter SlidingWindowRateLimiter, key string) (bool, error) {
	if limiter == nil {
		return true, nil
	}
	if withError, ok := limiter.(slidingWindowRateLimiterCheckWithError); ok {
		return withError.CheckWithError(ctx, key)
	}
	if inspector, ok := limiter.(slidingWindowRateLimiterInspector); ok {
		return inspector.Check(ctx, key), nil
	}
	return true, nil
}

func slidingWindowLimiterRetryAfter(ctx context.Context, limiter SlidingWindowRateLimiter, key string) time.Duration {
	if inspector, ok := limiter.(slidingWindowRateLimiterInspector); ok {
		return inspector.RetryAfter(ctx, key)
	}
	return time.Minute
}

// slidingWindowLimiterAllow exposes backend errors to callers that cannot
// safely fail open. Allow itself fails open on Redis errors.
func slidingWindowLimiterAllow(ctx context.Context, limiter SlidingWindowRateLimiter, key string) (bool, error) {
	if limiter == nil {
		return true, nil
	}
	if withError, ok := limiter.(slidingWindowRateLimiterWithError); ok {
		return withError.AllowWithError(ctx, key)
	}
	return limiter.Allow(ctx, key), nil
}

// ── In-memory implementation ────────────────────────────────────────────────

// memorySlidingWindowRateLimiter keeps per-key timestamps in a slice and prunes them
// on every call. Adequate for single-node dev / tests; production multi-node
// deployments should use the Redis-backed implementation so rate budgets are
// shared across pods.
type memorySlidingWindowRateLimiter struct {
	cfg       SlidingWindowRateLimit
	mu        sync.Mutex
	hit       map[string][]time.Time
	lastSweep time.Time
}

func newMemorySlidingWindowRateLimiter(cfg SlidingWindowRateLimit) SlidingWindowRateLimiter {
	return &memorySlidingWindowRateLimiter{cfg: cfg, hit: make(map[string][]time.Time)}
}

func (l *memorySlidingWindowRateLimiter) Allow(ctx context.Context, key string) bool {
	allowed, _ := l.AllowWithError(ctx, key)
	return allowed
}

func (l *memorySlidingWindowRateLimiter) AllowWithError(_ context.Context, key string) (bool, error) {
	return l.evaluate(key, true), nil
}

func (l *memorySlidingWindowRateLimiter) Check(ctx context.Context, key string) bool {
	allowed, _ := l.CheckWithError(ctx, key)
	return allowed
}

func (l *memorySlidingWindowRateLimiter) CheckWithError(_ context.Context, key string) (bool, error) {
	return l.evaluate(key, false), nil
}

func (l *memorySlidingWindowRateLimiter) evaluate(key string, consume bool) bool {
	if l.cfg.Limit <= 0 {
		return true
	}
	now := time.Now()
	cutoff := now.Add(-l.cfg.Window)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepExpired(now, cutoff)

	hits := l.hit[key]
	// Trim entries that fell out of the window.
	keep := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.cfg.Limit {
		l.hit[key] = keep
		return false
	}
	if consume {
		keep = append(keep, now)
	}
	l.hit[key] = keep
	return true
}

func (l *memorySlidingWindowRateLimiter) sweepExpired(now, cutoff time.Time) {
	sweepInterval := l.cfg.Window
	if sweepInterval <= 0 || sweepInterval > time.Minute {
		sweepInterval = time.Minute
	}
	if !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < sweepInterval {
		return
	}
	for key, hits := range l.hit {
		if len(hits) == 0 || !hits[len(hits)-1].After(cutoff) {
			delete(l.hit, key)
		}
	}
	l.lastSweep = now
}

func (l *memorySlidingWindowRateLimiter) RetryAfter(_ context.Context, key string) time.Duration {
	if l.cfg.Limit <= 0 {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	hits := l.hit[key]
	if len(hits) == 0 {
		return l.cfg.Window
	}
	retry := time.Until(hits[0].Add(l.cfg.Window))
	if retry <= 0 {
		return time.Second
	}
	return retry
}

// ── Redis implementation ────────────────────────────────────────────────────

// <keyPrefix><key> is the ZSET we keep timestamps in. The score is
// the request's nanosecond timestamp so ZREMRANGEBYSCORE can drop everything
// older than the cutoff and ZCARD tells us the remaining count. The member is
// a per-request unique id (NOT the timestamp): two requests landing in the
// same nanosecond would otherwise collide on an identical member, ZADD would
// update-in-place instead of inserting, and the window would under-count.
const defaultSlidingWindowKeyPrefix = "mul:rate:"

// slidingWindowAllowSrc runs the slide-window check atomically on Redis:
//
//	KEYS[1] = ZSET key
//	ARGV[1] = now (unix nanos as string, used as the entry score)
//	ARGV[2] = cutoff (unix nanos as string)
//	ARGV[3] = limit
//	ARGV[4] = expiry seconds (TTL refresh, larger than window)
//	ARGV[5] = unique member id for this request
//
// Returns 1 when the request is admitted, 0 when it should be rejected.
//
// We trim first, then count, then optionally insert. Doing all three in a
// single Lua call avoids the classic "two pods both see count=limit-1 and
// both insert" race.
const slidingWindowAllowSrc = `
local key = KEYS[1]
local now = tonumber(ARGV[1])
local cutoff = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])
local member = ARGV[5]
redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)
local count = redis.call('ZCARD', key)
if count >= limit then
    return 0
end
redis.call('ZADD', key, now, member)
redis.call('EXPIRE', key, ttl)
return 1
`

var slidingWindowAllowScript = redis.NewScript(slidingWindowAllowSrc)

const slidingWindowCheckSrc = `
local key = KEYS[1]
local cutoff = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)
local count = redis.call('ZCARD', key)
if count >= limit then
    return 0
end
return 1
`

var slidingWindowCheckScript = redis.NewScript(slidingWindowCheckSrc)

type redisSlidingWindowRateLimiter struct {
	cfg       SlidingWindowRateLimit
	rdb       redis.UniversalClient
	keyPrefix string
}

func newRedisSlidingWindowRateLimiter(rdb redis.UniversalClient, cfg SlidingWindowRateLimit, keyPrefix string) SlidingWindowRateLimiter {
	return &redisSlidingWindowRateLimiter{cfg: cfg, rdb: rdb, keyPrefix: keyPrefix}
}

func (l *redisSlidingWindowRateLimiter) Allow(ctx context.Context, key string) bool {
	allowed, err := l.AllowWithError(ctx, key)
	if err != nil {
		// Allow is a safety net, not a correctness requirement.
		return true
	}
	return allowed
}

func (l *redisSlidingWindowRateLimiter) AllowWithError(ctx context.Context, key string) (bool, error) {
	if l.cfg.Limit <= 0 || l.rdb == nil {
		return true, nil
	}
	nowTime := time.Now()
	now := nowTime.UnixNano()
	cutoff := nowTime.Add(-l.cfg.Window).UnixNano()
	ttlSeconds := int64(l.cfg.Window/time.Second) * 2
	if ttlSeconds < 1 {
		ttlSeconds = 1
	}
	prefix := l.keyPrefix
	if prefix == "" {
		prefix = defaultSlidingWindowKeyPrefix
	}
	// Unique member per request: the score carries the timestamp for the
	// sliding-window trim, but two requests in the same nanosecond must not
	// collapse onto one ZSET member, or the window under-counts.
	member := uuid.NewString()
	res, err := slidingWindowAllowScript.Run(
		ctx,
		l.rdb,
		[]string{prefix + key},
		now, cutoff, l.cfg.Limit, ttlSeconds, member,
	).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

func (l *redisSlidingWindowRateLimiter) Check(ctx context.Context, key string) bool {
	allowed, err := l.CheckWithError(ctx, key)
	if err != nil {
		return true
	}
	return allowed
}

func (l *redisSlidingWindowRateLimiter) CheckWithError(ctx context.Context, key string) (bool, error) {
	if l.cfg.Limit <= 0 || l.rdb == nil {
		return true, nil
	}
	cutoff := time.Now().Add(-l.cfg.Window).UnixNano()
	prefix := l.keyPrefix
	if prefix == "" {
		prefix = defaultSlidingWindowKeyPrefix
	}
	res, err := slidingWindowCheckScript.Run(
		ctx,
		l.rdb,
		[]string{prefix + key},
		cutoff, l.cfg.Limit,
	).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

func (l *redisSlidingWindowRateLimiter) RetryAfter(_ context.Context, _ string) time.Duration {
	if l.cfg.Window <= 0 {
		return time.Second
	}
	// A full window is conservative and remains correct across replicas even
	// if the oldest ZSET member changes between rejection and this response.
	return l.cfg.Window
}
