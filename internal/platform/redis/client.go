// Package redis owns the connection to Redis and the narrow ports built on it:
// sessions, cart state, rate limiting, idempotency and caching.
//
// It is the only package that imports go-redis (RD-000). Domain code depends on
// the interfaces declared by its own module, never on a Redis client.
//
// Redis is a cache and a coordination store. Its loss MUST degrade the system,
// never corrupt it (RD-010): durable copies live in PostgreSQL. Security
// functions built on it — rate limiting, idempotency — fail closed (RD-011).
package redis

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
	"github.com/stephenindia1/steleios-ecom/internal/platform/ratelimit"
)

// Client wraps the Redis connection.
type Client struct {
	rdb *goredis.Client
	log *slog.Logger
}

// New opens and verifies the connection.
func New(ctx context.Context, cfg config.Redis, log *slog.Logger) (*Client, error) {
	opts := &goredis.Options{
		Addr:         cfg.Addr,
		Username:     cfg.Username,
		Password:     cfg.Password, // from env only (BR-SEC-07)
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize, // measured, not defaulted (DB-045)
		MinIdleConns: cfg.MinIdleConns,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout, // every call bounded (GO-033)
		WriteTimeout: cfg.WriteTimeout,
		MaxRetries:   2,
	}
	if cfg.UseTLS {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	rdb := goredis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close() //nolint:errcheck // we are already failing
		return nil, fmt.Errorf("redis: ping: %w", err)
	}

	log.Info("redis connected", "addr", cfg.Addr, "db", cfg.DB, "tls", cfg.UseTLS)
	return &Client{rdb: rdb, log: log}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.rdb.Close() }

// Health reports whether Redis is reachable, for readiness (HLT-002).
func (c *Client) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := c.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

// fixedWindowScript increments a counter and sets its expiry on first use, in
// one atomic round trip.
//
// RD-004: a multi-step read-modify-write against Redis must be a script or a
// transaction. Two separate calls would let concurrent requests each read the
// same count and each decide they are under the limit.
//
// The window start is part of the key, so the counter expires on its own rather
// than needing pruning, and the script stays O(1) (RD-005).
var fixedWindowScript = goredis.NewScript(`
local current = redis.call("INCR", KEYS[1])
if current == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
local ttl = redis.call("PTTL", KEYS[1])
return {current, ttl}
`)

// Limiter is the Redis-backed rate limiter.
type Limiter struct {
	c   *Client
	log *slog.Logger
}

// NewLimiter returns the production rate limiter.
func NewLimiter(c *Client) *Limiter { return &Limiter{c: c, log: c.log} }

// Allow counts one request against a rule.
//
// A fixed window rather than a sliding one: it is one INCR, it is exact at the
// boundary in the direction that matters (it never lets more through than the
// limit within a window), and its worst case — a burst spanning two windows —
// is acceptable for the limits in the policy catalogue. A sliding window would
// cost a sorted set and a range delete per request for a marginal gain.
func (l *Limiter) Allow(ctx context.Context, rule ratelimit.Rule, key string) (ratelimit.Result, error) {
	if err := rule.Validate(); err != nil {
		return ratelimit.Result{}, fmt.Errorf("%w: %w", ratelimit.ErrUnavailable, err)
	}

	windowStart := time.Now().UnixMilli() / rule.Window.Milliseconds() //nolint:forbidigo // window bucketing is wall-clock by definition
	redisKey := RateLimit("rl", string(rule.Scope), key, windowStart)

	res, err := fixedWindowScript.Run(ctx, l.c.rdb,
		[]string{redisKey}, rule.Window.Milliseconds()).Slice()
	if err != nil {
		// Fail closed. An unavailable limiter is refused capacity, never an
		// open door (RD-011, BR-SEC-11).
		return ratelimit.Result{}, fmt.Errorf("%w: %w", ratelimit.ErrUnavailable, err)
	}
	if len(res) != 2 {
		return ratelimit.Result{}, fmt.Errorf("%w: unexpected script result", ratelimit.ErrUnavailable)
	}

	count, ok := res[0].(int64)
	if !ok {
		return ratelimit.Result{}, fmt.Errorf("%w: unexpected count type", ratelimit.ErrUnavailable)
	}
	ttlMillis, _ := res[1].(int64)
	retryAfter := time.Duration(ttlMillis) * time.Millisecond
	if retryAfter <= 0 {
		retryAfter = rule.Window
	}

	remaining := rule.Limit - int(count)
	if remaining < 0 {
		remaining = 0
	}

	return ratelimit.Result{
		Allowed:    int(count) <= rule.Limit,
		Limit:      rule.Limit,
		Remaining:  remaining,
		RetryAfter: retryAfter,
		Rule:       rule,
	}, nil
}

// ---------------------------------------------------------------------------
// Idempotency
// ---------------------------------------------------------------------------

// ErrIdempotencyInFlight means the key is claimed by a request still running.
var ErrIdempotencyInFlight = errors.New("redis: idempotency key is in flight")

// IdempotencyStore records and replays responses by key (BR-CHK-02, BR-CHK-03).
type IdempotencyStore struct {
	c *Client
}

// NewIdempotencyStore returns the production idempotency store.
func NewIdempotencyStore(c *Client) *IdempotencyStore { return &IdempotencyStore{c: c} }

// storedResponse is the persisted form. Status and body are stored together so
// a replay reproduces the original response exactly.
type storedResponse struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

// Lookup returns a stored response and whether one was found.
//
// It returns primitives rather than an httpx type so this package stays free of
// the HTTP layer; a small adapter at the composition root satisfies the
// interface httpx declares (OOP-05).
//
// Claiming the key with SET NX is what makes this safe under concurrency: the
// first caller wins and proceeds; a second concurrent caller with the same key
// is told the request is in flight rather than being allowed to create a second
// order.
func (s *IdempotencyStore) Lookup(ctx context.Context, actorID, key string) (status int, body []byte, found bool, err error) {
	redisKey := Idempotency(actorID, key)

	raw, err := s.c.rdb.Get(ctx, redisKey).Result()
	switch {
	case err == nil:
		if raw == claimMarker {
			return 0, nil, false, ErrIdempotencyInFlight
		}
		var stored storedResponse
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			return 0, nil, false, fmt.Errorf("redis: decode stored response: %w", err)
		}
		return stored.Status, []byte(stored.Body), true, nil

	case errors.Is(err, goredis.Nil):
		// New key: claim it so a concurrent duplicate does not also proceed.
		claimed, err := s.c.rdb.SetNX(ctx, redisKey, claimMarker, TTLIdempotency).Result()
		if err != nil {
			return 0, nil, false, fmt.Errorf("redis: claim idempotency key: %w", err)
		}
		if !claimed {
			return 0, nil, false, ErrIdempotencyInFlight
		}
		return 0, nil, false, nil

	default:
		return 0, nil, false, fmt.Errorf("redis: read idempotency key: %w", err)
	}
}

// Save records the outcome for replay.
func (s *IdempotencyStore) Save(ctx context.Context, actorID, key string, status int, body []byte) error {
	payload, err := json.Marshal(storedResponse{Status: status, Body: string(body)})
	if err != nil {
		return fmt.Errorf("redis: encode stored response: %w", err)
	}
	if err := s.c.rdb.Set(ctx, Idempotency(actorID, key), payload, TTLIdempotency).Err(); err != nil {
		return fmt.Errorf("redis: save idempotent response: %w", err)
	}
	return nil
}

// claimMarker is the placeholder written while a request is in flight.
const claimMarker = "\x00in-flight"
