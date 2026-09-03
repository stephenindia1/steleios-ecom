// Package config loads and validates all runtime configuration.
//
// Configuration comes from the environment only. Secrets are never read from a
// file in the repository and never appear in a log line (BR-SEC-07, GO-084).
//
// Validation happens once, at startup, and the process exits non-zero on invalid
// configuration. Starting degraded is prohibited (HLT-005).
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// buildVersion and buildRevision are stamped at link time by the Makefile, so a
// running process can be tied back to a commit without anyone remembering to
// set an environment variable (BR-VER-08, HLT-004). The environment overrides
// them where a deployment system supplies its own.
//
// These are the only package-level mutable variables in the codebase, and they
// are written once by the linker rather than by any code (OOP-03).
var (
	buildVersion  = "dev"
	buildRevision = "unknown"
)

// Environment names a deployment. It selects payment provider keys and nothing
// else may (BR-PAY-15).
type Environment string

// The environments Steleios runs in.
const (
	EnvLocal      Environment = "local"
	EnvTest       Environment = "test"
	EnvStaging    Environment = "staging"
	EnvProduction Environment = "production"
)

// Valid reports whether e is a known environment.
func (e Environment) Valid() error {
	switch e {
	case EnvLocal, EnvTest, EnvStaging, EnvProduction:
		return nil
	default:
		return fmt.Errorf("unknown environment %q", string(e))
	}
}

// IsProduction reports whether this deployment serves real customers. Used to
// require TLS and to disable debug logging.
func (e Environment) IsProduction() bool { return e == EnvProduction }

// Config is the complete runtime configuration.
type Config struct {
	Env      Environment
	Version  string // build version, reported at startup (HLT-004)
	Revision string // git SHA

	HTTP     HTTP
	Postgres Postgres
	SMS      SMS
	Redis    Redis
	Log      Log
	Security Security
}

// HTTP configures the API server.
type HTTP struct {
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownGrace   time.Duration
	MaxBodyBytes    int64
	TrustedProxies  []string // RealIP is honoured only from these (OBS-002)
	AllowedOrigins  []string // strict CORS allowlist
	RequestTimeout  time.Duration
	ReadHeaderLimit time.Duration
}

// SMS configures outbound SMS.
//
// SMS is the channel Steleios treats as proof — recovery, contact changes and
// first logins all verify against the registered mobile, because those flows
// assume the email may be lost (BR-REC-11). Provider "msg91" is the Indian
// gateway; "log" writes what would have been sent and is for development only.
//
// The template ids are DLT registrations. Under TRAI's rules a transactional
// SMS must match a template registered in advance or the carrier drops it
// silently and still bills for it, so a missing id is a startup failure rather
// than a message nobody receives.
type SMS struct {
	Provider string
	AuthKey  string
	SenderID string

	TemplateFirstLogin     string
	TemplateOTP            string
	TemplateRecoveryIssued string
	TemplateEmailChanged   string
}

// Postgres configures the database pool.
type Postgres struct {
	DSN string
	// AdminDSN is the privileged connection used ONLY by migrations and by test
	// setup that needs DDL. The application never opens it: a superuser is
	// exempt from row-level security entirely, so running the API under one
	// would silently disable every isolation policy in the schema (ADR 0007,
	// postgres.assertRLSApplies).
	AdminDSN          string
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	ConnectTimeout    time.Duration
	StatementTimeout  time.Duration // request-path ceiling (DB-012)
	IdleInTxTimeout   time.Duration // DB-034
	HealthCheckPeriod time.Duration
}

// Redis configures the cache, session store, rate limiter and queue backend.
type Redis struct {
	Addr         string
	Username     string
	Password     string
	DB           int
	PoolSize     int
	MinIdleConns int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	DialTimeout  time.Duration
	UseTLS       bool
}

// Log configures structured logging.
type Log struct {
	Level  string // debug | info | warn | error
	Format string // json | text (text is permitted locally only)
}

// Security holds cross-cutting security settings.
type Security struct {
	SessionTTL      time.Duration
	AdminSessionTTL time.Duration
	// ReauthWindow is how long a password confirmation stays valid for
	// high-consequence actions (BR-ADM-07). Short by design: it is the gap
	// between a console left unattended and a refund.
	ReauthWindow time.Duration
	CookieDomain string
	CookieSecure bool
	HSTSMaxAge   time.Duration
}

// Load reads configuration from the environment and validates it.
//
// Every field is either explicitly set or has a documented default. There are no
// silent zero values, because a zero timeout is an unbounded call (GO-033).
func Load() (Config, error) {
	var errs []error
	get := func(key, def string) string { return lookup(key, def) }

	cfg := Config{
		Env:      Environment(get("STELEIOS_ENV", string(EnvLocal))),
		Version:  get("STELEIOS_VERSION", buildVersion),
		Revision: get("STELEIOS_REVISION", buildRevision),

		HTTP: HTTP{
			Addr:            get("HTTP_ADDR", ":8080"),
			ReadTimeout:     duration("HTTP_READ_TIMEOUT", 15*time.Second, &errs),
			WriteTimeout:    duration("HTTP_WRITE_TIMEOUT", 30*time.Second, &errs),
			IdleTimeout:     duration("HTTP_IDLE_TIMEOUT", 60*time.Second, &errs),
			ShutdownGrace:   duration("HTTP_SHUTDOWN_GRACE", 20*time.Second, &errs),
			RequestTimeout:  duration("HTTP_REQUEST_TIMEOUT", 10*time.Second, &errs),
			ReadHeaderLimit: duration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second, &errs),
			MaxBodyBytes:    bytesize("HTTP_MAX_BODY_BYTES", 1<<20, &errs), // 1 MiB (DB-026)
			TrustedProxies:  list("HTTP_TRUSTED_PROXIES", ""),
			AllowedOrigins:  list("HTTP_ALLOWED_ORIGINS", "http://localhost:5173"),
		},

		Postgres: Postgres{
			DSN:               get("POSTGRES_DSN", ""),
			AdminDSN:          get("POSTGRES_ADMIN_DSN", ""),
			MaxConns:          int32(number("POSTGRES_MAX_CONNS", 20, &errs)),
			MinConns:          int32(number("POSTGRES_MIN_CONNS", 2, &errs)),
			MaxConnLifetime:   duration("POSTGRES_MAX_CONN_LIFETIME", time.Hour, &errs),
			MaxConnIdleTime:   duration("POSTGRES_MAX_CONN_IDLE", 30*time.Minute, &errs),
			ConnectTimeout:    duration("POSTGRES_CONNECT_TIMEOUT", 5*time.Second, &errs),
			StatementTimeout:  duration("POSTGRES_STATEMENT_TIMEOUT", 3*time.Second, &errs),
			IdleInTxTimeout:   duration("POSTGRES_IDLE_IN_TX_TIMEOUT", 10*time.Second, &errs),
			HealthCheckPeriod: duration("POSTGRES_HEALTHCHECK_PERIOD", 30*time.Second, &errs),
		},

		Redis: Redis{
			Addr:         get("REDIS_ADDR", "localhost:6379"),
			Username:     get("REDIS_USERNAME", ""),
			Password:     get("REDIS_PASSWORD", ""),
			DB:           number("REDIS_DB", 0, &errs),
			PoolSize:     number("REDIS_POOL_SIZE", 20, &errs),
			MinIdleConns: number("REDIS_MIN_IDLE_CONNS", 2, &errs),
			ReadTimeout:  duration("REDIS_READ_TIMEOUT", 2*time.Second, &errs),
			WriteTimeout: duration("REDIS_WRITE_TIMEOUT", 2*time.Second, &errs),
			DialTimeout:  duration("REDIS_DIAL_TIMEOUT", 3*time.Second, &errs),
			UseTLS:       boolean("REDIS_TLS", false, &errs),
		},

		Log: Log{
			Level:  get("LOG_LEVEL", "info"),
			Format: get("LOG_FORMAT", "json"),
		},

		SMS: SMS{
			// "log" by default so a developer can run the whole onboarding flow
			// without an account. Validate below refuses that default in
			// production, where a silent no-op would mean nobody ever receives
			// their recovery code.
			Provider: get("SMS_PROVIDER", "log"),
			AuthKey:  get("MSG91_AUTH_KEY", ""),
			SenderID: get("MSG91_SENDER_ID", ""),

			TemplateFirstLogin:     get("MSG91_TEMPLATE_FIRST_LOGIN", ""),
			TemplateOTP:            get("MSG91_TEMPLATE_OTP", ""),
			TemplateRecoveryIssued: get("MSG91_TEMPLATE_RECOVERY_ISSUED", ""),
			TemplateEmailChanged:   get("MSG91_TEMPLATE_EMAIL_CHANGED", ""),
		},

		Security: Security{
			SessionTTL:      duration("SESSION_TTL", 30*24*time.Hour, &errs),    // SES-004
			AdminSessionTTL: duration("ADMIN_SESSION_TTL", 12*time.Hour, &errs), // BR-ADM-07
			ReauthWindow:    duration("REAUTH_WINDOW", 15*time.Minute, &errs),   // BR-ADM-07
			CookieDomain:    get("COOKIE_DOMAIN", ""),
			CookieSecure:    boolean("COOKIE_SECURE", true, &errs),
			HSTSMaxAge:      duration("HSTS_MAX_AGE", 365*24*time.Hour, &errs),
		},
	}

	if err := cfg.Validate(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return Config{}, fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// Validate checks cross-field invariants. It is exported so tests can build a
// Config literal and assert it is well formed.
func (c Config) Validate() error {
	var errs []error

	if err := c.Env.Valid(); err != nil {
		errs = append(errs, err)
	}
	if c.SMS.Provider != "log" && c.SMS.Provider != "msg91" {
		errs = append(errs, fmt.Errorf("SMS_PROVIDER %q is not one of log|msg91", c.SMS.Provider))
	}
	if c.Postgres.DSN == "" {
		errs = append(errs, errors.New("POSTGRES_DSN is required"))
	}
	if c.Redis.Addr == "" {
		errs = append(errs, errors.New("REDIS_ADDR is required"))
	}
	if c.Postgres.MinConns > c.Postgres.MaxConns {
		errs = append(errs, errors.New("POSTGRES_MIN_CONNS exceeds POSTGRES_MAX_CONNS"))
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("LOG_LEVEL %q is not one of debug|info|warn|error", c.Log.Level))
	}

	// Production tightening. These are the settings that, if wrong, are a
	// security incident rather than an inconvenience — so they fail the boot
	// rather than warn (BR-SEC-11).
	if c.Env.IsProduction() {
		if !c.Security.CookieSecure {
			errs = append(errs, errors.New("COOKIE_SECURE must be true in production"))
		}
		if c.Log.Format != "json" {
			errs = append(errs, errors.New("LOG_FORMAT must be json in production (LOG-001)"))
		}
		if c.Log.Level == "debug" {
			errs = append(errs, errors.New("LOG_LEVEL debug is prohibited in production (LOG-005)"))
		}
		if !c.Redis.UseTLS {
			errs = append(errs, errors.New("REDIS_TLS must be enabled in production"))
		}
		if len(c.HTTP.AllowedOrigins) == 0 {
			errs = append(errs, errors.New("HTTP_ALLOWED_ORIGINS must be set in production"))
		}
		// The logging sender writes a line and delivers nothing. In production
		// that would mean every recovery code, first password and OTP silently
		// going nowhere, and the failure only surfacing when an owner who has
		// lost their email cannot get back in — the worst possible moment
		// (BR-REC-11, BR-REC-14b).
		if c.SMS.Provider == "log" {
			errs = append(errs, errors.New("SMS_PROVIDER must be a real provider in production: recovery and contact changes are verified by SMS and cannot fall back to email"))
		}
		for _, o := range c.HTTP.AllowedOrigins {
			if o == "*" {
				errs = append(errs, errors.New(`HTTP_ALLOWED_ORIGINS must not contain "*" in production`))
			}
		}
	}

	return errors.Join(errs...)
}

// Redacted returns the configuration with every secret replaced, suitable for
// the startup log line (HLT-004).
//
// It is a method on Config rather than a field-by-field call site so that a new
// secret field cannot be added without this function being the obvious place to
// handle it.
func (c Config) Redacted() map[string]any {
	return map[string]any{
		"env":                   string(c.Env),
		"version":               c.Version,
		"revision":              c.Revision,
		"http_addr":             c.HTTP.Addr,
		"http_max_body_bytes":   c.HTTP.MaxBodyBytes,
		"http_allowed_origins":  c.HTTP.AllowedOrigins,
		"postgres_dsn":          redactDSN(c.Postgres.DSN),
		"postgres_admin_dsn":    redactDSN(c.Postgres.AdminDSN),
		"sms_provider":          c.SMS.Provider,
		"msg91_auth_key":        secretPresence(c.SMS.AuthKey),
		"msg91_sender_id":       c.SMS.SenderID,
		"postgres_max_conns":    c.Postgres.MaxConns,
		"postgres_stmt_timeout": c.Postgres.StatementTimeout.String(),
		"redis_addr":            c.Redis.Addr,
		"redis_db":              c.Redis.DB,
		"redis_tls":             c.Redis.UseTLS,
		"redis_password":        secretPresence(c.Redis.Password),
		"log_level":             c.Log.Level,
		"log_format":            c.Log.Format,
		"session_ttl":           c.Security.SessionTTL.String(),
		"admin_session_ttl":     c.Security.AdminSessionTTL.String(),
		"reauth_window":         c.Security.ReauthWindow.String(),
		"cookie_secure":         c.Security.CookieSecure,
	}
}

// secretPresence reports whether a secret is configured without revealing it.
// "which secrets are set" is diagnostic; "what they are" is never logged.
func secretPresence(s string) string {
	if s == "" {
		return "unset"
	}
	return "set"
}

// redactDSN strips credentials from a connection string, keeping the host and
// database name, which are what an operator actually needs to see.
func redactDSN(dsn string) string {
	if dsn == "" {
		return "unset"
	}
	// postgres://user:password@host:port/db?opts  ->  postgres://***@host:port/db
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return "set"
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = rest[at+1:]
	}
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		rest = rest[:q]
	}
	return scheme + "://***@" + rest
}

func lookup(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func duration(key string, def time.Duration, errs *[]error) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not a duration: %w", key, raw, err))
		return def
	}
	if d <= 0 {
		*errs = append(*errs, fmt.Errorf("%s: must be positive, got %s", key, d))
		return def
	}
	return d
}

func number(key string, def int, errs *[]error) int {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not an integer: %w", key, raw, err))
		return def
	}
	return n
}

func bytesize(key string, def int64, errs *[]error) int64 {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		*errs = append(*errs, fmt.Errorf("%s: %q is not a positive byte count", key, raw))
		return def
	}
	return n
}

func boolean(key string, def bool, errs *[]error) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not a boolean: %w", key, raw, err))
		return def
	}
	return b
}

func list(key, def string) []string {
	raw := lookup(key, def)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts)) // DB-024
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
