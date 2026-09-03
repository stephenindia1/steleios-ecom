package session_test

import (
	"github.com/stephenindia1/steleios-ecom/internal/platform/tenant"
)

// defaultShop is the tenant seeded by migration 00003. It is referenced by id
// rather than looked up, because looking one up requires a tenant scope
// already — the app role cannot list tenants it is not currently acting in,
// which is row-level security working as intended (ADR 0007).
const defaultShop = "00000000-0000-0000-0000-000000000001"

func seededShop() (tenant.ID, error) { return tenant.Parse(defaultShop) }
