package onboarding

import (
	"context"
	"fmt"

	"github.com/hibiken/asynq"
	"github.com/stephenindia1/steleios-ecom/internal/platform/httpx"
	"github.com/stephenindia1/steleios-ecom/internal/platform/module"
	"github.com/stephenindia1/steleios-ecom/internal/platform/passwd"
	"github.com/stephenindia1/steleios-ecom/internal/platform/policy"
	"github.com/stephenindia1/steleios-ecom/internal/platform/sms"
)

// Mod is the onboarding module.
type Mod struct {
	svc     *Service
	handler *Handler
	db      interface{ Health(context.Context) error }
}

// New is the module factory.
func New(d *module.Deps, sender sms.Sender) (*Mod, error) {
	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("onboarding: %w", err)
	}
	if sender == nil {
		return nil, fmt.Errorf("onboarding: nil sms sender")
	}

	svc, err := NewService(NewRepository(d.DB, d.UoW),
		passwd.NewDefault(), sender, d.Audit, d.Clock, d.Log)
	if err != nil {
		return nil, err
	}

	return &Mod{svc: svc, handler: NewHandler(svc, d.Log), db: d.DB}, nil
}

// Name identifies the module.
func (m *Mod) Name() string { return "onboarding" }

// Service exposes the service for other modules that provision alongside it.
func (m *Mod) Service() *Service { return m.svc }

// Routes mounts the vendor console's endpoints.
//
// Every one carries a PLATFORM policy, and those are the only two policies in
// the catalogue holding a platform action. Read the mount and the boundary is
// visible: this module operates the vendor's business, and nothing here can
// reach a client's (BR-ADM-14).
func (m *Mod) Routes(g *httpx.Group) { Mount(g, m.handler) }

// Mount registers the routes for a handler, so tests drive the real policies.
func Mount(g *httpx.Group, h *Handler) {
	g.Mount("/api/v1/platform", func(p *httpx.Group) {
		p.GET("/clients", policy.PlatformRead, h.ListClients)
		p.GET("/clients/{id}", policy.PlatformRead, h.Client)

		p.POST("/clients", policy.PlatformManage, h.Register)
		p.POST("/clients/{id}/owners", policy.PlatformManage, h.AddOwner)
		p.POST("/clients/{id}/shops", policy.PlatformManage, h.ProvisionShop)
		p.POST("/clients/{id}/users", policy.PlatformManage, h.IssueFirstUser)
		p.POST("/clients/{id}/confirm", policy.PlatformManage, h.Confirm)
	})
}

// Workers registers nothing yet. Subscription expiry lands here with docs/09.
func (m *Mod) Workers(*asynq.ServeMux) {}

// Health reports whether onboarding can serve.
func (m *Mod) Health(ctx context.Context) error { return m.db.Health(ctx) }
