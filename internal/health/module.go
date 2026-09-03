// Package health serves the liveness and readiness probes.
//
// Liveness answers "is the process running"; readiness answers "can it serve".
// They are deliberately different: a readiness failure withdraws traffic, while
// a liveness failure restarts the process, and checking dependencies in
// liveness turns a database blip into a restart loop (HLT-001, HLT-002).
package health

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stephenindia1/steleios-ecom/internal/platform/httpx"
	"github.com/stephenindia1/steleios-ecom/internal/platform/module"
	"github.com/stephenindia1/steleios-ecom/internal/platform/policy"
	"github.com/stephenindia1/steleios-ecom/internal/platform/postgres"
	"github.com/stephenindia1/steleios-ecom/internal/platform/redis"
)

// Mod is the health module.
type Mod struct {
	db       *postgres.Pool
	rdb      *redis.Client
	version  string
	revision string
	env      string
	started  time.Time
}

// New is the module factory (docs/03 §2.3).
func New(d *module.Deps) (*Mod, error) {
	if d.DB == nil || d.Redis == nil {
		return nil, errors.New("health: nil dependency")
	}
	return &Mod{
		db:       d.DB,
		rdb:      d.Redis,
		version:  d.Cfg.Version,
		revision: d.Cfg.Revision,
		env:      string(d.Cfg.Env),
		started:  d.Clock.Now(),
	}, nil
}

// Name identifies the module.
func (m *Mod) Name() string { return "health" }

// Health is the module's own contribution to readiness. The health module
// itself is always healthy; it reports on others.
func (m *Mod) Health(context.Context) error { return nil }

// Workers registers nothing: health has no background work.
func (m *Mod) Workers(*asynq.ServeMux) {}

// Routes mounts the probes.
func (m *Mod) Routes(g *httpx.Group) {
	g.GET("/healthz", policy.Probe, m.live)
	g.GET("/readyz", policy.Probe, m.ready)
}

// liveResponse is what a liveness probe returns.
type liveResponse struct {
	Status   string `json:"status"`
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Env      string `json:"env"`
	UptimeS  int64  `json:"uptime_seconds"`
}

// live answers liveness. It checks nothing beyond the process itself.
func (m *Mod) live(_ context.Context, req *httpx.Request) (httpx.Response, error) {
	return httpx.OK(liveResponse{
		Status:   "ok",
		Version:  m.version,
		Revision: m.revision,
		Env:      m.env,
		UptimeS:  int64(time.Since(m.started).Seconds()),
	}), nil
}

// readyResponse reports per-dependency status, so an operator sees which one is
// failing rather than a bare "not ready" (HLT-003).
type readyResponse struct {
	Status       string            `json:"status"`
	Version      string            `json:"version"`
	Revision     string            `json:"revision"`
	Dependencies map[string]string `json:"dependencies"`
}

// ready answers readiness by checking every dependency the process needs to
// serve a request.
func (m *Mod) ready(ctx context.Context, req *httpx.Request) (httpx.Response, error) {
	deps := make(map[string]string, 2) // DB-024
	healthy := true

	check := func(name string, err error) {
		if err != nil {
			// The reason is useful to an operator and is not sensitive: it names
			// a dependency, not a credential.
			deps[name] = err.Error()
			healthy = false
			return
		}
		deps[name] = "ok"
	}

	check("postgres", m.db.Health(ctx))
	check("redis", m.rdb.Health(ctx))

	body := readyResponse{
		Status:       "ready",
		Version:      m.version,
		Revision:     m.revision,
		Dependencies: deps,
	}

	if !healthy {
		// 503 so the load balancer withdraws traffic. Returning 200 with a
		// "degraded" body would keep sending requests to a process that cannot
		// serve them (HLT-002).
		body.Status = "not_ready"
		return httpx.Response{Status: http.StatusServiceUnavailable, Body: body}, nil
	}
	return httpx.OK(body), nil
}
