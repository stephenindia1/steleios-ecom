package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
)

// valid returns a Config that passes validation, so each test can mutate one
// field and assert that the single change is what fails.
func valid() config.Config {
	return config.Config{
		Env:      config.EnvProduction,
		Version:  "1.0.0",
		Revision: "abc123",
		HTTP: config.HTTP{
			Addr:           ":8080",
			MaxBodyBytes:   1 << 20,
			AllowedOrigins: []string{"https://steleios.example"},
		},
		Postgres: config.Postgres{
			DSN:      "postgres://user:pw@db:5432/steleios",
			MinConns: 2,
			MaxConns: 20,
		},
		Redis:    config.Redis{Addr: "redis:6379", UseTLS: true},
		Log:      config.Log{Level: "info", Format: "json"},
		Security: config.Security{CookieSecure: true},
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		mutate  func(*config.Config)
		wantErr string
	}{
		{name: "a valid production config passes", mutate: func(*config.Config) {}},
		{
			name: "local relaxes the production requirements",
			mutate: func(c *config.Config) {
				*c = config.Config{
					Env:      config.EnvLocal,
					Postgres: config.Postgres{DSN: "postgres://localhost/dev", MinConns: 1, MaxConns: 5},
					Redis:    config.Redis{Addr: "localhost:6379"},
					Log:      config.Log{Level: "debug", Format: "text"},
				}
			},
		},

		// Required values.
		{
			name:    "missing postgres dsn",
			mutate:  func(c *config.Config) { c.Postgres.DSN = "" },
			wantErr: "POSTGRES_DSN",
		},
		{
			name:    "missing redis address",
			mutate:  func(c *config.Config) { c.Redis.Addr = "" },
			wantErr: "REDIS_ADDR",
		},
		{
			name:    "unknown environment",
			mutate:  func(c *config.Config) { c.Env = "prod" },
			wantErr: "unknown environment",
		},
		{
			name:    "pool bounds inverted",
			mutate:  func(c *config.Config) { c.Postgres.MinConns, c.Postgres.MaxConns = 20, 2 },
			wantErr: "POSTGRES_MIN_CONNS",
		},
		{
			name:    "unknown log level",
			mutate:  func(c *config.Config) { c.Log.Level = "verbose" },
			wantErr: "LOG_LEVEL",
		},

		// Production tightening: these fail the boot rather than warn, because
		// each one is a security incident rather than an inconvenience.
		{
			name:    "insecure cookies in production",
			mutate:  func(c *config.Config) { c.Security.CookieSecure = false },
			wantErr: "COOKIE_SECURE",
		},
		{
			name:    "text logs in production",
			mutate:  func(c *config.Config) { c.Log.Format = "text" },
			wantErr: "LOG_FORMAT",
		},
		{
			name:    "debug logs in production",
			mutate:  func(c *config.Config) { c.Log.Level = "debug" },
			wantErr: "LOG_LEVEL debug",
		},
		{
			name:    "redis without tls in production",
			mutate:  func(c *config.Config) { c.Redis.UseTLS = false },
			wantErr: "REDIS_TLS",
		},
		{
			name:    "no cors allowlist in production",
			mutate:  func(c *config.Config) { c.HTTP.AllowedOrigins = nil },
			wantErr: "HTTP_ALLOWED_ORIGINS",
		},
		{
			name:    "wildcard cors in production",
			mutate:  func(c *config.Config) { c.HTTP.AllowedOrigins = []string{"*"} },
			wantErr: `must not contain "*"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := valid()
			tc.mutate(&cfg)
			err := cfg.Validate()

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error mentioning %q, got none", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	t.Parallel()

	// An operator fixing configuration should see every problem in one boot,
	// not discover them one restart at a time.
	cfg := valid()
	cfg.Postgres.DSN = ""
	cfg.Redis.Addr = ""
	cfg.Log.Level = "loud"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"POSTGRES_DSN", "REDIS_ADDR", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("joined error missing %q: %v", want, err)
		}
	}
}

func TestLoadDefaultsAndOverrides(t *testing.T) {
	// t.Setenv forbids t.Parallel.
	t.Setenv("STELEIOS_ENV", "local")
	t.Setenv("POSTGRES_DSN", "postgres://u:p@localhost:5432/steleios")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Defaults must be present and non-zero: a zero timeout is an unbounded
	// call (GO-033), so there is no such thing as "unset" here.
	if cfg.HTTP.Addr != ":8080" {
		t.Errorf("HTTP.Addr = %q, want :8080", cfg.HTTP.Addr)
	}
	if cfg.Postgres.StatementTimeout != 3*time.Second {
		t.Errorf("StatementTimeout = %s, want 3s (DB-012)", cfg.Postgres.StatementTimeout)
	}
	if cfg.Security.SessionTTL != 30*24*time.Hour {
		t.Errorf("SessionTTL = %s, want 720h (SES-004)", cfg.Security.SessionTTL)
	}
	if cfg.Security.AdminSessionTTL != 12*time.Hour {
		t.Errorf("AdminSessionTTL = %s, want 12h (BR-ADM-07)", cfg.Security.AdminSessionTTL)
	}
	if cfg.HTTP.MaxBodyBytes != 1<<20 {
		t.Errorf("MaxBodyBytes = %d, want 1MiB (DB-026)", cfg.HTTP.MaxBodyBytes)
	}

	// Every duration must be positive; a zero here would be an unbounded wait.
	durations := map[string]time.Duration{
		"http read":      cfg.HTTP.ReadTimeout,
		"http write":     cfg.HTTP.WriteTimeout,
		"http idle":      cfg.HTTP.IdleTimeout,
		"http request":   cfg.HTTP.RequestTimeout,
		"shutdown grace": cfg.HTTP.ShutdownGrace,
		"pg connect":     cfg.Postgres.ConnectTimeout,
		"pg statement":   cfg.Postgres.StatementTimeout,
		"pg idle in tx":  cfg.Postgres.IdleInTxTimeout,
		"redis read":     cfg.Redis.ReadTimeout,
		"redis write":    cfg.Redis.WriteTimeout,
		"redis dial":     cfg.Redis.DialTimeout,
	}
	for name, d := range durations {
		if d <= 0 {
			t.Errorf("%s timeout is %s; every call must be bounded (GO-033)", name, d)
		}
	}
}

func TestLoadRejectsMalformedValues(t *testing.T) {
	cases := []struct {
		name, key, value, wantErr string
	}{
		{name: "bad duration", key: "HTTP_READ_TIMEOUT", value: "soon", wantErr: "HTTP_READ_TIMEOUT"},
		{name: "zero duration", key: "HTTP_READ_TIMEOUT", value: "0s", wantErr: "must be positive"},
		{name: "negative duration", key: "HTTP_READ_TIMEOUT", value: "-1s", wantErr: "must be positive"},
		{name: "bad integer", key: "POSTGRES_MAX_CONNS", value: "many", wantErr: "POSTGRES_MAX_CONNS"},
		{name: "bad boolean", key: "REDIS_TLS", value: "yes-please", wantErr: "REDIS_TLS"},
		{name: "bad byte size", key: "HTTP_MAX_BODY_BYTES", value: "-1", wantErr: "HTTP_MAX_BODY_BYTES"},
		{name: "zero byte size", key: "HTTP_MAX_BODY_BYTES", value: "0", wantErr: "HTTP_MAX_BODY_BYTES"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STELEIOS_ENV", "local")
			t.Setenv("POSTGRES_DSN", "postgres://localhost/x")
			t.Setenv(tc.key, tc.value)

			_, err := config.Load()
			if err == nil {
				t.Fatalf("%s=%q should have failed to load", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadFailsWithoutRequiredValues(t *testing.T) {
	t.Setenv("STELEIOS_ENV", "local")

	// This test asserts the ABSENCE of a variable, so it must control that
	// variable rather than assume the ambient environment is clean. Without
	// this line the test passes locally and fails the moment anyone exports
	// POSTGRES_DSN — which CI does, for the database integration tests.
	//
	// t.Setenv restores the previous value afterwards, and Load treats an
	// empty value as unset.
	t.Setenv("POSTGRES_DSN", "")

	// HLT-005: starting degraded is prohibited, so a missing DSN is a boot
	// failure rather than a lazily-discovered connection error.
	if _, err := config.Load(); err == nil {
		t.Fatal("Load without POSTGRES_DSN should fail")
	}
}

func TestLoadParsesLists(t *testing.T) {
	t.Setenv("STELEIOS_ENV", "local")
	t.Setenv("POSTGRES_DSN", "postgres://localhost/x")
	t.Setenv("HTTP_ALLOWED_ORIGINS", " https://a.example , https://b.example ,, ")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := []string{"https://a.example", "https://b.example"}
	if len(cfg.HTTP.AllowedOrigins) != len(want) {
		t.Fatalf("origins = %v, want %v", cfg.HTTP.AllowedOrigins, want)
	}
	for i := range want {
		if cfg.HTTP.AllowedOrigins[i] != want[i] {
			t.Errorf("origin %d = %q, want %q", i, cfg.HTTP.AllowedOrigins[i], want[i])
		}
	}
}

func TestRedactedNeverLeaksSecrets(t *testing.T) {
	t.Parallel()

	// HLT-004 logs the effective configuration at startup. BR-SEC-07 means it
	// must be safe to do so.
	cfg := valid()
	cfg.Postgres.DSN = "postgres://appuser:hunter2@db.internal:5432/steleios?sslmode=require"
	cfg.Redis.Password = "redis-password-value"

	red := cfg.Redacted()

	for key, value := range red {
		s, ok := value.(string)
		if !ok {
			continue
		}
		for _, secret := range []string{"hunter2", "redis-password-value", "appuser"} {
			if strings.Contains(s, secret) {
				t.Errorf("Redacted()[%q] leaked %q: %s", key, secret, s)
			}
		}
	}

	// The host and database must survive — that is what an operator needs.
	dsn, _ := red["postgres_dsn"].(string)
	if !strings.Contains(dsn, "db.internal:5432/steleios") {
		t.Errorf("redacted dsn = %q, want the host and database to remain", dsn)
	}
	if got := red["redis_password"]; got != "set" {
		t.Errorf("redis_password = %v, want the presence indicator \"set\"", got)
	}

	cfg.Redis.Password = ""
	if got := cfg.Redacted()["redis_password"]; got != "unset" {
		t.Errorf("empty redis_password = %v, want \"unset\"", got)
	}
}

func TestRedactDSNEdgeCases(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, dsn, want string }{
		{name: "empty", dsn: "", want: "unset"},
		{name: "no scheme", dsn: "host=db user=app password=x", want: "set"},
		{name: "no credentials", dsn: "postgres://db:5432/steleios", want: "postgres://***@db:5432/steleios"},
		{name: "with credentials and options", dsn: "postgres://u:p@db:5432/steleios?sslmode=require", want: "postgres://***@db:5432/steleios"},
		{name: "password containing an at sign", dsn: "postgres://u:p@ss@db:5432/steleios", want: "postgres://***@db:5432/steleios"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := valid()
			cfg.Postgres.DSN = tc.dsn
			if got := cfg.Redacted()["postgres_dsn"]; got != tc.want {
				t.Errorf("redacted %q = %v, want %q", tc.dsn, got, tc.want)
			}
		})
	}
}

func TestEnvironmentPredicates(t *testing.T) {
	t.Parallel()

	if !config.EnvProduction.IsProduction() {
		t.Error("production should report IsProduction")
	}
	for _, e := range []config.Environment{config.EnvLocal, config.EnvTest, config.EnvStaging} {
		if e.IsProduction() {
			t.Errorf("%s should not report IsProduction", e)
		}
		if err := e.Valid(); err != nil {
			t.Errorf("%s should be a valid environment: %v", e, err)
		}
	}
	if err := config.Environment("").Valid(); err == nil {
		t.Error("the empty environment should be invalid")
	}
}
