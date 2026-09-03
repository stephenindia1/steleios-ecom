# ADR 0001 — Toolchain versions and frontend/backend compatibility

Date: 3 September 2026 · Status: accepted

## Decision

Pin the latest stable release of each toolchain, and make the compatibility between the Go backend and the TypeScript frontend a **checked contract** rather than an assumption.

| Component | Pinned version | Verified from |
|---|---|---|
| Go | **1.27.1** (`go.mod` `go` directive) | `https://go.dev/dl/?mode=json` |
| TypeScript | **7.0.2** (`latest` on npm) | npm registry `dist-tags` |
| Bun | 1.3.13 (installed; `packageManager` field pins it) | local toolchain |
| chi | v5.3.2 · pgx v5.10.0 · go-redis v9.22.0 · asynq v0.26.0 · goose v3.28.0 | `go.mod` |

TypeScript 7 is the native (Go-based) compiler. It is chosen deliberately for
type-checking throughput: `tsc --noEmit` is the second CI gate on every PR
(CLAUDE.md rule 46), and it runs on every keystroke in the editor. Faster type
checking makes the gate cheap enough to keep mandatory, which is the point.

## The compatibility contract

The backend and frontend meet at exactly one surface: **the HTTP API**. They are
compatible when, and only when, these hold.

1. **One source of truth.** The OpenAPI 3.1 document is generated from the Go
   handler request/response structs. It is committed to the repository.
2. **Generated types, never hand-written.** `web/shared/api/types.gen.ts` is
   generated from that document for TypeScript 7. Hand-written duplicates of a
   backend type are prohibited (TS-004, FE-03).
3. **CI fails on drift.** A gate regenerates both artefacts and fails if the
   working tree differs. A Go struct change that is not reflected in the
   committed TypeScript is a red build, not a runtime surprise in production.
4. **Money and quantities cross as integers.** `money.Paise` marshals as an
   integer number of paise; quantities marshal as integers in base UoM. TypeScript
   receives `number` and formats but never computes (TS-009, BR-PRC-01,
   BR-UOM-01). This is the single most important compatibility rule: it is what
   keeps IEEE-754 out of the money path on the client.
5. **API versioning is in the path** (`/api/v1`). A breaking change is a new
   version, never a redefinition (BR-VER-06). The frontend declares which API
   version it targets, and `/readyz` reports which versions the backend serves.
6. **Dates cross as RFC 3339 UTC strings.** Never epoch numbers, never local time
   (GO-071). Presentation converts to `Asia/Kolkata`.
7. **Enumerations cross as their string form** and are generated as TypeScript
   string-literal unions, so an unhandled backend enum value is a compile error
   on the frontend rather than a silent fallthrough (GO-045).

## Version drift policy

- Toolchain upgrades are their own pull request, with the changelog reviewed
  (GO-006, GO-102). Never bundled with a feature.
- The versions above are re-verified at each upgrade against the authoritative
  source, not from memory.
- Go's `toolchain` directive lets the build fetch 1.27.1 automatically where a
  developer has an older local Go. CI pins the version explicitly.

## Consequences

- A backend contributor cannot change a response shape without the frontend
  types moving in the same commit. That is the intended friction.
- The generated-types gate must exist before the first frontend endpoint is
  consumed, i.e. during Phase 2. Until then the contract is documented but not
  yet enforced, and that gap is tracked as Phase 2 work.
