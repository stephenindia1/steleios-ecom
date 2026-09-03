-- +goose Up
-- +goose StatementBegin

-- The role catalogue in the database had drifted from the one in the code.
--
-- internal/platform/authz grants actions to fifteen roles. staff_roles listed
-- twelve. staff_role_assignments.role_code has a foreign key to staff_roles, so
-- the three missing ones could not be assigned to anybody at all:
--
--   delivery      added with the storefront delivery flow (docs/02 §9A)
--   saas_admin    added with the vendor/client split (docs/09 §6)
--   saas_support
--
-- Each was written into authz with its grants and its reasoning, and each was
-- unusable, because the insert that would have used it fails on the foreign
-- key. Nothing caught it: the grant table is a Go map and the catalogue is a
-- table, and until now nothing compared them.
--
-- TestTheDatabaseRoleCatalogueMatchesTheCodes is the durable fix. This
-- migration is only the correction.
--
-- The descriptions below say what each role may do AND what it deliberately may
-- not, because for these three the exclusion is the point.

insert into staff_roles (code, description) values
    ('delivery',
     'Delivery person: see assigned deliveries, mark delivered, assert the customer paid. Deliberately NOT payment verification — under UPI the person who takes a payment must never be the one who confirms it arrived (BR-STO-31)'),
    ('saas_admin',
     'VENDOR role. Creates and manages SaaS clients, their shops and their subscriptions. Holds no shop-data action whatsoever: cannot read or write any client''s orders, stock, customers or documents (docs/09 §6)'),
    ('saas_support',
     'VENDOR role. Read-only over client and subscription state, so a billing or provisioning question can be answered without touching the client''s business data')
on conflict (code) do nothing;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Only removable while unassigned; the foreign key protects anything granted.
delete from staff_roles where code in ('delivery', 'saas_admin', 'saas_support');
-- +goose StatementEnd
