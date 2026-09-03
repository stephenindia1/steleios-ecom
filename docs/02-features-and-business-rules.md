# Steleios — Features and Business Rules

Functional specification for the Steleios commerce platform.
Stack: Go backend · Vue 3 + Pinia (Bun/Vite) frontend · PostgreSQL · Redis · Razorpay · INR.

Status: draft · 3 September 2026

---

## 0. Conventions

These apply to every rule in this document.

| Convention | Rule |
|---|---|
| Rule IDs | `BR-<MODULE>-<nn>`. Stable forever. Never renumber; deprecate instead. |
| Testability | Every `BR-*` must be expressible as at least one passing and one failing test case. If it can't, it's a guideline, not a rule — move it out of the numbered list. |
| Money | `int64` paise. Never float, never `numeric` for currency. Currency code stored explicitly (`INR`). |
| Rates | Basis points (`int`). `1800` = 18%. |
| Rounding | Round-half-up, applied **per line**, then summed. Implemented in exactly one function (`pricing.Round`). |
| Time | `timestamptz`, stored UTC, displayed IST (`Asia/Kolkata`). |
| Identifiers | Internal: UUIDv7. Customer-facing order number: `STL-<YY>-<6 digits>`, never sequential-guessable across customers. |
| Authority | The server is the sole authority on price, tax, stock, discount and shipping. Client input proposes; the server decides. |
| Enforcement layer | Business rules live in the **service layer**. Handlers validate shape; services enforce meaning. |

**Rule severity tags used below**

- `[MONEY]` — a defect here causes financial loss or an incorrect invoice.
- `[SEC]` — a defect here is a security vulnerability.
- `[LEGAL]` — required by Indian law (GST, legal metrology, consumer protection).

---

## 1. Catalog

### Features

- Product with many variants; variant is the sellable unit (SKU).
- Draft / active / archived lifecycle with scheduled publish.
- Product media: ordered images per product, optional per-variant override.
- Categories (nested) and free-form tags.
- Faceted browse: category, price range, tags, variant options, availability.
- Search with typo tolerance and synonyms.
- SEO fields per product: slug, meta title, meta description, canonical URL, JSON-LD.
- Bulk CSV import/export for products, variants and prices.

### Business rules

| ID | Rule |
|---|---|
| BR-CAT-01 | A product has at least one variant. A product with zero variants cannot be set to `active`. |
| BR-CAT-02 | `sku` is globally unique and immutable once the variant has appeared on any order line. |
| BR-CAT-03 | `slug` is unique, lowercase, `[a-z0-9-]` only. Changing a slug creates a permanent 301 from the old slug; old slugs are never reused for a different product. |
| BR-CAT-04 | Variant `options` must use the same option keys as every other variant of the same product. `{size, colour}` and `{size}` cannot coexist under one product. |
| BR-CAT-05 | `(product_id, options)` is unique. Two variants cannot describe the same combination. |
| BR-CAT-06 | Only `active` products are visible on the storefront. `draft` and `archived` return 404 on the storefront, and are visible in admin. |
| BR-CAT-07 | Archiving a product does not delete it and does not affect existing orders. Archived variants cannot be added to a cart. |
| BR-CAT-08 | `[LEGAL]` An active variant must carry a `gst_rate_bps` and its product must carry an `hsn_code`. Publishing without both is rejected. |
| BR-CAT-09 | `[LEGAL]` If `mrp_paise` is set it must be `>= price_paise`, and the storefront must display MRP alongside the selling price when they differ. |
| BR-CAT-10 | `price_paise >= 0`. A zero-price variant is allowed (freebies) but is flagged in admin and excluded from discount percentage calculations. |
| BR-CAT-11 | A price change never alters any existing order, cart line already priced at checkout, or invoice. Price history is retained for audit. |
| BR-CAT-12 | Deleting a product is soft-delete only. Hard delete is available exclusively for products that have never been ordered. |
| BR-CAT-13 | Catalog cache entries are invalidated explicitly on publish, price change, media change and archive — never left to TTL expiry. |
| BR-CAT-14 | Bulk import is transactional per row with a validation report. A file with any invalid row imports the valid rows and reports the rest; it never partially applies a single row. |

---

## 1A. Product media, attributes and provenance

### Features

- Many images per product, and per variant where the variant looks different.
- Originals in object storage (S3 or Google Cloud Storage), served as generated renditions through a CDN.
- Ordered gallery with one primary image; click opens a lightbox.
- Typed attribute sets per category, driving a short summary panel and a full details view.
- Allergen and dietary information as first-class structured data.
- Source and provenance: country of origin, manufacturer/packer, and supplier attribution where known.

### 1A.1 Media storage

| ID | Rule |
|---|---|
| BR-MED-01 | Media lives in object storage (S3 or GCS), never in PostgreSQL and never on the application filesystem. The database holds metadata and keys only. |
| BR-MED-02 | Storage is reached through one `blobstore.Store` port with an S3 and a GCS implementation selected by configuration (the provider factory pattern, docs/03 §2.6). No module talks to a cloud SDK directly. |
| BR-MED-03 | Object keys are **content-addressed** (`media/<sha256>/<rendition>.<ext>`). Identical uploads deduplicate, and every object is immutable, so the CDN can cache it forever. |
| BR-MED-04 | `[SEC]` The bucket is private. Nothing is publicly listable. Renditions are served through the CDN; originals are never served to customers. |
| BR-MED-05 | `[SEC]` Uploads go directly to storage via a short-lived presigned URL, so bytes never pass through the API. The presign is issued only to an actor holding `catalog:write`, is scoped to one key, one content type and one size ceiling, and expires in 15 minutes. |
| BR-MED-06 | `[SEC]` After upload the worker verifies the object by **content sniffing**, not by the client-declared content type, and rejects anything that is not an allowed image format. An unverified object is never attached to a product (BR-SEC-08). |
| BR-MED-07 | `[SEC]` EXIF and every other metadata block are stripped during rendition generation. Customer-uploaded review photos routinely carry GPS coordinates. |
| BR-MED-08 | Renditions (thumbnail, card, detail, zoom) are generated by an asynq job, not in the request path. Until renditions exist the image is `pending` and is not displayed. |
| BR-MED-09 | Every rendition records its pixel width and height, and the frontend emits them, so images reserve their space and do not shift the layout (SEO-015, CLS budget). |
| BR-MED-10 | Modern formats (AVIF, WebP) are generated alongside a JPEG fallback and offered via `srcset`/`<picture>`. |
| BR-MED-11 | Deletion is soft. An object is removed from storage only by a sweeper, after a grace period, once nothing references it — because a hard delete of a shared content-addressed object would break every product using it. |
| BR-MED-12 | Limits are enforced server-side: at most 12 images per product and 6 per variant, 10 MB per original, allowed types JPEG/PNG/WebP/AVIF. |

### 1A.2 Gallery and lightbox

| ID | Rule |
|---|---|
| BR-MED-20 | A product has an **ordered** gallery. Exactly one image is primary; it is what appears in listings, cart lines, order confirmations and structured data. |
| BR-MED-21 | A variant may carry its own images. Selecting a variant swaps the gallery to that variant's images, falling back to the product's where the variant has none. |
| BR-MED-22 | `[LEGAL]` Every image carries `alt` text describing the product. Empty or filename `alt` text fails validation — it is an accessibility requirement and a discovery signal alike (SEO-015). |
| BR-MED-23 | The gallery and its primary image are **server-rendered**. A gallery that only appears after hydration is invisible to AI answer surfaces (SEO-001/003). |
| BR-MED-24 | Clicking an image opens a lightbox: full-resolution rendition, zoom, next/previous, and a caption. |
| BR-MED-25 | The lightbox is keyboard-operable and accessible — `Esc` closes, arrow keys navigate, focus is trapped while open and returns to the triggering thumbnail on close, and the overlay is announced as a dialog. |
| BR-MED-26 | The lightbox preloads only the adjacent renditions, and respects `prefers-reduced-motion` for its transitions. |
| BR-MED-27 | The lightbox is progressive enhancement: with JavaScript unavailable, the thumbnail links to the full rendition and still works. |

### 1A.3 Attributes, allergens and provenance

| ID | Rule |
|---|---|
| BR-ATR-01 | Attributes are **typed and defined per category**: text, integer, decimal-with-unit, enum, multi-enum, boolean. Free-form key/value pairs are prohibited — they cannot be filtered, validated or rendered consistently. |
| BR-ATR-02 | A category defines which attributes apply, which are required to publish, and which appear in the **summary panel** versus the full details view. |
| BR-ATR-03 | Enum attributes draw from a controlled vocabulary held as reference data and versioned (BR-VER-01). Values are never typed free-hand at product entry. |
| BR-ATR-04 | Attributes with units use the `uom` package's dimensions, so "500 g" and "0.5 kg" are the same fact and are filterable as one (BR-UOM-20). |
| BR-ATR-05 | The summary panel is a short, structured digest — the three to six attributes that decide a purchase in this category. It is rendered as a real `<table>` or `<dl>`, not a styled grid, so it is machine-extractable (SEO-032). |
| BR-ATR-06 | Attributes are emitted in `Product` structured data and must match the visible page exactly (SEO-021). |

**Allergen and dietary information**

| ID | Rule |
|---|---|
| BR-ATR-10 | `[LEGAL]` Allergen information is a first-class structured attribute group, never prose in a description. Each declared allergen carries one of three states: **contains**, **may contain** (cross-contamination), or **free from**. Absence of a statement is not "free from" and must never be displayed as such. |
| BR-ATR-11 | `[LEGAL]` For food categories, publishing requires: ingredients list, allergen declaration, nutritional information, the veg/non-veg mark, net quantity, best-before or use-by, FSSAI licence number of the manufacturer or packer, and the name and address of the manufacturer, packer or importer. A product missing any of these cannot be set to `active`. |
| BR-ATR-12 | `[LEGAL]` Allergen and ingredient data are versioned with the product (BR-VER-01). An order snapshots the version shown at purchase, so a later recipe change never rewrites what a customer was told (BR-ORD-03). |
| BR-ATR-13 | `[LEGAL]` Allergen changes require re-approval by an actor holding `catalog:write` and are audited. This is the highest-consequence content field on the platform — the failure mode is somebody in hospital. |
| BR-ATR-14 | Allergen statements are shown on the product page **above the fold or in the summary panel**, not buried in a details accordion, and are repeated on the order confirmation for food orders. |
| BR-ATR-15 | Customers may save dietary preferences and allergen exclusions; the storefront then flags matching products. The flag is an aid, never a guarantee — the disclaimer is displayed alongside it, because "free from" data is only as good as the supplier's declaration. |
| BR-ATR-16 | A dietary or allergen filter that excludes a product MUST NOT silently hide products with **missing** data. They are shown, marked as "information not available", because a hidden unknown reads to the customer as a confirmed safe absence. |

**Source and provenance**

| ID | Rule |
|---|---|
| BR-ATR-20 | `[LEGAL]` Country of origin is mandatory on every listing and is displayed on the product page. |
| BR-ATR-21 | `[LEGAL]` Manufacturer, packer or importer name and full address are stored and displayed, per packaged-commodities requirements. |
| BR-ATR-22 | Provenance detail — producer, farm, region, harvest or production date, certifications (organic, FSSAI, ISI, fair trade) — is recorded where the supplier provides it, with the certificate reference and its expiry. |
| BR-ATR-23 | `[LEGAL]` A certification claim MUST NOT be displayed past the certificate's expiry date. An expired certificate removes the claim automatically and alerts purchasing. |
| BR-ATR-24 | Provenance may be linked to the **supplier and batch** that delivered the stock (§2A), so "where did this actually come from" is answerable per shipment, not just per product. Where batches from different origins are mixed, the page shows the origin of the batch the customer would be allocated (BR-BAT-31). |
| BR-ATR-25 | Supplier commercial terms and cost prices are never exposed through provenance display (BR-SUP-06). Origin is public; margin is not. |

---

## 1B. Item identification: barcode, QR and numeric code

Staff need to identify an item at a counter, at goods receipt, and while picking. Three input methods, one resolution path.

> **Scope note.** This introduces a **counter / point-of-sale channel** alongside the storefront. A counter sale is a real order that decrements real stock, so it goes through the same pricing, tax, batch allocation and audit machinery — it is not a separate ledger. It is a distinct build phase and is called out as such in docs/01 §7.

### Features

- Every variant carries a scannable code; every batch can carry its own.
- Three input methods, interchangeable: **barcode scan**, **QR scan**, and **manual numeric code** typed on a keypad.
- After a scan or a code entry, if more than one batch is sellable, the counter operator picks the batch.
- Short numeric codes for shops without scanners, generated with a check digit.
- Label printing for shelf-edge and batch labels.

### 1B.1 The code model

| ID | Rule |
|---|---|
| BR-SCN-01 | A variant may carry an external **GTIN/EAN/UPC** — the manufacturer's barcode. It is stored, validated by check digit, and unique per variant. Multiple GTINs may map to one variant (a supplier repack), but one GTIN never maps to two variants. |
| BR-SCN-02 | Every variant also carries an internal **numeric code**: a short, keyable identifier with a trailing check digit, generated by the platform. This is what a shop without a scanner types. |
| BR-SCN-03 | `[MONEY]` The numeric code is short enough to key quickly (8 digits including the check digit) and validated by check digit before any lookup, so a mistyped digit is rejected rather than resolving to a different product. This is the whole reason for the check digit: at a counter, a silent mis-scan sells the wrong item at the wrong price. |
| BR-SCN-04 | A **batch** may carry its own code — a QR encoding variant plus batch — printed on the goods-receipt label. Scanning it identifies the batch directly and skips the selection step in BR-SCN-20. |
| BR-SCN-05 | QR payloads are an opaque platform-issued token, never a URL with an internal identifier and never customer data. A QR code printed on a shelf is public. |
| BR-SCN-06 | `[SEC]` A scanned or typed code is untrusted input. It is validated for format and check digit, then resolved by exact lookup — never interpolated into a query, never used to construct a path (BR-SEC-02, DB-040). |
| BR-SCN-07 | Codes are immutable once a variant has been sold under them. A relabel issues a new code and keeps the old one resolving, so old stock on a shelf still scans. |
| BR-SCN-08 | Code lookup is a single indexed point read, `O(log n)` (DB-001). A counter queue is not the place for a table scan. |

### 1B.2 Resolution and batch selection

| ID | Rule |
|---|---|
| BR-SCN-20 | Resolution returns the variant plus its **sellable batches**, ordered by FEFO (BR-BAT-10). Then: **one** sellable batch → selected automatically; **more than one** → the operator is shown the list and must choose; **none** → the sale is blocked with the reason. |
| BR-SCN-21 | The batch chooser shows, per batch: batch number, expiry date, remaining shelf life, quantity available, and the **effective price including any markdown** (BR-BAT-30). Two batches of the same product can be different prices, and the operator must be able to see that before charging. |
| BR-SCN-22 | The FEFO head batch is pre-selected and visually marked as the recommended choice. Choosing a different batch is permitted, requires a reason code, and is recorded on the order line — a counter operator reaching past the near-expiry stock is exactly the behaviour that creates write-offs, and it should be visible. |
| BR-SCN-23 | `[MONEY]` The price charged is the selected batch's effective price. Selecting a batch and charging another batch's price is prohibited (BR-BAT-31). |
| BR-SCN-24 | `[MONEY]` Selection reserves against the chosen batch through the normal atomic path (BR-BAT-11). Two counters scanning the last unit simultaneously must not both sell it. |
| BR-SCN-25 | An expired, quarantined or recalled batch never appears in the chooser, under any code (BR-BAT-05, BR-BAT-21). |
| BR-SCN-26 | Scanning a **batch** QR skips selection but still validates that the batch is sellable, and refuses with the reason if it is not. |
| BR-SCN-27 | `[LEGAL]` Where a batch carries an allergen or provenance difference from its siblings, the chooser shows it. Two batches are not always the same product (BR-ATR-12, BR-ATR-24). |

### 1B.3 Input methods are interchangeable

| ID | Rule |
|---|---|
| BR-SCN-30 | Barcode scan, QR scan and typed numeric code resolve through **one** service method. Three entry points, one code path — a bug fixed for scanners is fixed for keypads (DRY). |
| BR-SCN-31 | A shop configures which input methods its counters offer. QR scanners are not universal — mid-size shops have them, small ones do not — so **the numeric keypad path is never optional and never a degraded experience**. It is a first-class input, not a fallback. |
| BR-SCN-32 | Hardware scanners present as keyboards. The counter UI accepts a fast keystroke burst terminated by Enter as a scan, and a slower typed entry as manual input, without the operator switching modes. |
| BR-SCN-33 | The counter screen is keyboard-operable end to end: scan or type, choose a batch by number key, confirm. A counter operator must never need a mouse. |
| BR-SCN-34 | An unresolvable code shows a search fallback by name and SKU rather than a dead end. |
| BR-SCN-35 | Scan resolution responds within **200 ms** at p95. Beyond that an operator starts double-scanning, which creates duplicate lines. |
| BR-SCN-36 | Every resolution emits an event with the input method used (`scan.resolved`, `scan.rejected`, `scan.batch_overridden`), so which shops actually use scanners is a measurement rather than an assumption (doc 06 §3). |

### 1B.4 Labels

| ID | Rule |
|---|---|
| BR-SCN-40 | The platform prints shelf-edge labels (name, price, unit price per BR-UOM-10, numeric code, barcode) and batch labels (batch number, expiry, batch QR). |
| BR-SCN-41 | `[LEGAL]` A price label must carry the GST-inclusive price and the unit price where legal metrology requires it. |
| BR-SCN-42 | Reprinting a label after a price change is an explicit action with an audit entry, so a shelf price that disagrees with the till is traceable to a moment. |

---

## 2. Inventory

### Features

- Per-variant stock: `on_hand`, `reserved`, derived `available`.
- Time-boxed reservations held during checkout.
- Backorder and pre-order flags per variant.
- Low-stock threshold with admin alert.
- Manual stock adjustment with mandatory reason code.
- Full stock movement ledger.

### Business rules

| ID | Rule |
|---|---|
| BR-INV-01 | `available = on_hand - reserved`. It is computed on read and never stored. |
| BR-INV-02 | `[MONEY]` Reservation is a single atomic conditional `UPDATE`. Read-then-write is prohibited. Zero rows affected means insufficient stock. |
| BR-INV-03 | Invariants `on_hand >= 0`, `reserved >= 0`, `reserved <= on_hand` are enforced by database `CHECK` constraints, not only by application code. |
| BR-INV-04 | A reservation is created at checkout initiation and expires after **15 minutes**. Expired reservations are released by the sweeper job. |
| BR-INV-05 | `[MONEY]` On successful payment, the reservation converts to a decrement of `on_hand` and `reserved` in one transaction. Stock is never decremented before payment confirmation. |
| BR-INV-06 | On payment failure, cancellation, or reservation expiry, `reserved` is decremented and `on_hand` is untouched. |
| BR-INV-07 | The sweeper is idempotent: releasing an already-released reservation is a no-op, not an error. |
| BR-INV-08 | A variant with `available <= 0` and `backorder = false` cannot be added to a cart and displays as out of stock. |
| BR-INV-09 | Backorder-enabled variants may go to `available < 0` up to `backorder_limit`. Orders containing backordered lines enter fulfilment as `awaiting_stock`. |
| BR-INV-10 | Every change to `on_hand` or `reserved` writes a stock movement row: variant, delta, reason (`sale`, `return`, `adjustment`, `damage`, `receipt`, `reservation`, `release`), actor, order reference, timestamp. |
| BR-INV-11 | Manual adjustments require an `inventory:write` role and a non-empty reason. Adjustments are always audited. |
| BR-INV-12 | Cart lines are not reservations. Stock is only guaranteed from checkout initiation onward; the cart must re-validate availability at checkout. |

---

## 2A. Suppliers, batches and expiry

Stock is not fungible. The same variant arrives from different suppliers, at different costs, with different expiry dates, and must be sold **earliest-expiry-first** so that value is not written off. This makes the **batch** — not the variant — the real unit of stock.

> **FEFO, not FIFO.** The requirement is "first out by expiry", which is First-Expired-First-Out. Received order is only the tiebreak when expiry dates match or are absent. Selling strictly by receipt order while a later-received, earlier-expiring batch sits on the shelf is exactly the write-off this model exists to prevent.

### Features

- Supplier master with GSTIN, terms, lead time and performance history.
- Purchase orders, goods receipts, and batch creation on receipt.
- Multiple concurrent batches per variant, each with its own batch/lot number, expiry, cost and supplier.
- FEFO allocation on reservation, with FIFO tiebreak.
- Minimum-shelf-life gate: short-dated stock is not sold as fresh.
- Near-expiry markdown — automatic or manual, per batch.
- Batch-level traceability: any order traces to its batches, any batch traces to its orders.
- Recall and quarantine by batch.
- Batch-costed COGS and margin reporting.

### 2B.1 Suppliers

| ID | Rule |
|---|---|
| BR-SUP-01 | A supplier record holds legal name, GSTIN, state (for place-of-supply on purchases), address, contacts, payment terms, lead time and an active flag. |
| BR-SUP-02 | `[LEGAL]` GSTIN MUST be format-validated and its embedded state code MUST agree with the supplier's state. Input tax credit depends on it. |
| BR-SUP-03 | A supplier is never deleted, only deactivated. Batches and receipts reference suppliers permanently. |
| BR-SUP-04 | `[SEC]` Supplier creation and edits require the `purchasing:write` role and are audited (BR-ADM-06). |
| BR-SUP-05 | A goods receipt MUST reference a supplier and a purchase order, or carry a recorded reason for receipt without one. |
| BR-SUP-06 | Supplier cost prices are `[MONEY]` integers in paise per base UoM, and are commercially sensitive: they MUST NOT be exposed on any storefront endpoint or in any customer-facing payload. |

### 2B.2 Batches

| ID | Rule |
|---|---|
| BR-BAT-01 | `[MONEY]` A **batch** is the unit of stock: variant, batch number, supplier, received date, manufacture date, expiry date, unit cost, and quantities in base UoM. `inventory` per variant is a **rollup** of its batches, maintained in the same transaction, never an independent number. |
| BR-BAT-02 | `(variant_id, supplier_id, batch_number)` is unique. Re-receiving the same batch number from the same supplier adds to the existing batch; it does not create a second one. |
| BR-BAT-03 | Every product declares `batch_tracked` and `expiry_tracked`. Non-batch-tracked products still use the batch model with a single implicit batch and a null expiry — **there is one allocation code path, not two** (DRY). |
| BR-BAT-04 | `[LEGAL]` For an `expiry_tracked` product, a receipt without an expiry date is rejected. There is no default and no null fallback. |
| BR-BAT-05 | Batch status is one of `active`, `quarantine`, `expired`, `recalled`, `written_off`. Only `active` batches are allocatable. |
| BR-BAT-06 | Batch quantities are integers in base UoM (BR-UOM-01). `on_hand_base >= 0`, `reserved_base >= 0`, `reserved_base <= on_hand_base`, enforced by database `CHECK`. |
| BR-BAT-07 | Unit cost is snapshotted on the batch at receipt and is immutable. Cost changes create a new batch, never an edit — COGS must be reproducible. |
| BR-BAT-08 | Every batch quantity change writes a stock movement citing the batch, the reason code, the actor and the source document (BR-INV-10). A batch's history MUST fully explain its current quantity. |

### 2B.3 FEFO allocation

| ID | Rule |
|---|---|
| BR-BAT-10 | `[MONEY]` Reservation allocates across batches in strict order: **`expiry_date` ascending (nulls last), then `received_at` ascending, then batch id**. The tiebreak chain makes the order total and deterministic — two concurrent reservations MUST allocate in the same sequence. |
| BR-BAT-11 | `[MONEY]` Allocation is a single atomic statement over locked candidate rows. Read-then-write across batches is prohibited (BR-INV-02, DB-030). |
| BR-BAT-12 | `[MONEY]` A reservation may span several batches. The allocation — which batch contributed how much — is persisted, not recomputed. |
| BR-BAT-13 | If the allocated total is less than requested, the whole transaction rolls back. Partial reservation is prohibited: a cart line is satisfied entirely or not at all. |
| BR-BAT-14 | Batches are locked in the allocation order above, so concurrent reservations acquire locks in the same sequence and cannot deadlock (DB-032). |
| BR-BAT-15 | `[MONEY]` On payment confirmation, each allocation converts to a decrement of that batch's `on_hand_base` and `reserved_base`. On expiry, failure or cancellation, `reserved_base` is released per allocation (BR-INV-05, BR-INV-06). |
| BR-BAT-16 | Order lines persist their batch allocations. This is what makes recall and traceability possible in both directions, and it MUST NOT be dropped as denormalisation. |
| BR-BAT-17 | Returned stock is restocked to its **original batch** when that batch is still `active` and within shelf life; otherwise it goes to `quarantine`, never to a different active batch (BR-RET-07). |

### 2B.4 Expiry and shelf life

| ID | Rule |
|---|---|
| BR-BAT-20 | A product may declare `min_shelf_life_days`. Batches with less remaining life are excluded from normal allocation — they are not sold as fresh stock. |
| BR-BAT-21 | A daily job transitions batches past expiry to `expired` and releases their unsold quantity from availability. Expired stock is never allocatable, under any discount. |
| BR-BAT-22 | `[MONEY]` Expiry write-off is a stock movement with reason `expired` and the batch's cost, so shrinkage is measurable rather than a silent disappearance. |
| BR-BAT-23 | Alerts fire at configurable thresholds before expiry (default 90/30/7 days) with the quantity and value at risk, so markdown is a decision, not a discovery. |
| BR-BAT-24 | `[LEGAL]` Expiry dates are dates, not timestamps, and are evaluated in `Asia/Kolkata`. A batch expires at the end of its expiry date. |
| BR-BAT-25 | `[SEC][LEGAL]` A recall marks affected batches `recalled`, removes them from availability immediately, and produces the list of affected orders and customers from the persisted allocations (BR-BAT-16). |

### 2B.5 Batch markdown — putting stock on sale

| ID | Rule |
|---|---|
| BR-BAT-30 | A batch may carry a **markdown**: a percentage or an absolute price override in paise per sale unit, with a reason (`near_expiry`, `damaged_packaging`, `overstock`, `clearance`) and an optional validity window. |
| BR-BAT-31 | `[MONEY]` The price a customer is quoted is the price of the batch that FEFO would allocate — the **head batch**. Price and allocation are decided together, in one transaction, and locked by the reservation (BR-PRC-09). A price computed from one batch and stock taken from another is prohibited. |
| BR-BAT-32 | `[MONEY]` If a cart line spans batches with different effective prices, each allocated portion is priced at its own batch price and the line shows the resulting blended total with a breakdown. Charging the cheaper price for the whole quantity, or the dearer, is prohibited. |
| BR-BAT-33 | `[LEGAL]` When a markdown is driven by shelf life, the storefront MUST disclose it — the remaining life or a "short-dated" label — before purchase. Selling near-expiry stock at a discount without disclosure is a consumer-protection exposure. |
| BR-BAT-34 | `[MONEY]` A markdown MUST NOT take the effective price below zero, and MUST NOT be applied to an `expired`, `recalled` or `quarantine` batch. |
| BR-BAT-35 | Automatic markdown rules (e.g. −25% at 45 days remaining, −50% at 20) are configured per category, evaluated by a daily job, and every application writes an event and an audit entry. Automatic markdown MUST NOT reprice an already-reserved line. |
| BR-BAT-36 | Batch markdown stacks with order-level coupons only when the coupon is flagged `stacks_with_markdown`. The default is **not stackable**, and the customer is shown which discount applied and why (BR-DSC-05). |
| BR-BAT-37 | `[SEC]` Manual markdown requires the `pricing:write` role, records actor and reason, and is audited. Markdown beyond a configured percentage requires second-actor approval (BR-ADM-04). |
| BR-BAT-38 | `[MONEY]` Margin reporting uses the batch's snapshotted cost against the actual sold price, so a markdown's true cost is visible per batch and per reason code. |
| BR-BAT-39 | Order lines snapshot the effective price **and** the markdown reason per allocated portion, so an invoice and a margin report can both be reproduced years later (BR-ORD-03). |

### 2B.6 Amendments to §2 Inventory

| ID | Rule |
|---|---|
| BR-INV-13 | BR-INV-01 is amended: `inventory.on_hand` and `inventory.reserved` are **derived rollups** of active batch quantities, written in the same transaction as any batch change. A reconciliation job asserts rollup equals the sum of batches and alerts on any drift. |
| BR-INV-14 | Customer-facing availability is `floor(sum(allocatable batch available) / conversion_factor)`, where allocatable excludes expired, quarantined, recalled batches and those below `min_shelf_life_days` (BR-UOM-08, BR-BAT-20). |

### FEFO allocation — the reference statement

```sql
-- One statement. Locks candidates in FEFO order, allocates a running total,
-- and returns what each batch contributed. BR-BAT-10/11/12/14.
with candidate as (
    select id, batch_number, expiry_date, received_at,
           on_hand_base - reserved_base as available,
           effective_price_paise
      from stock_batches
     where variant_id = $1
       and status = 'active'
       and on_hand_base > reserved_base
       and (expiry_date is null or expiry_date > current_date + $3::int)  -- min shelf life
     order by expiry_date nulls last, received_at, id                     -- FEFO, FIFO tiebreak
     for update
     limit 50
),
ranked as (
    select *, sum(available) over (order by expiry_date nulls last, received_at, id)
                 - available as taken_before
      from candidate
),
alloc as (
    select id, effective_price_paise,
           least(available, $2::bigint - taken_before) as take
      from ranked
     where taken_before < $2::bigint
)
update stock_batches b
   set reserved_base = b.reserved_base + a.take
  from alloc a
 where b.id = a.id
returning b.id as batch_id, a.take, a.effective_price_paise;
```

The caller sums `take` and rolls back unless it equals the requested quantity (BR-BAT-13). The returned per-batch prices are what the line is quoted at (BR-BAT-31/32) — allocation and pricing come out of the same statement, so they cannot disagree.

---

## 2B. Units of measure and conversion

Stock is not always held in the unit it is sold in: rice received and stocked in kilograms is sold in 500 g packs; fabric stocked in metres is sold per piece; beverages stocked as cases are sold as eaches. Three units therefore exist per product and MUST be modelled separately.

| Unit | Meaning | Used by |
|---|---|---|
| **Base UoM** | The canonical stock-keeping unit. All inventory arithmetic happens here. | `inventory`, `stock_movements`, reservations |
| **Sale UoM** | What the customer buys, per variant. | catalog, cart, order lines, invoice |
| **Purchase UoM** | What the supplier ships. | receiving, purchase orders |

### Features

- Per-product base UoM with a declared dimension (mass, volume, length, area, count).
- Per-variant sale UoM with an integer conversion factor to base.
- Per-supplier purchase UoM with its own conversion factor.
- Display in sale UoM everywhere customer-facing; arithmetic in base UoM everywhere internal.
- Unit pricing display (price per kg / per litre) derived, never stored.
- GST-compliant UQC (Unique Quantity Code) on every invoice line.

### Business rules

| ID | Rule |
|---|---|
| BR-UOM-01 | `[MONEY]` Every quantity in the system is an **integer in base UoM**. Floating-point quantities are prohibited everywhere, exactly as they are for money. |
| BR-UOM-02 | The base UoM is chosen fine enough that every sale and purchase conversion is an exact integer: milligram for mass, millilitre for volume, millimetre for length, square millimetre for area, `each` for count. |
| BR-UOM-03 | `[MONEY]` A conversion factor MUST be a positive integer expressing "how many base units are one sale (or purchase) unit". A conversion that would not be exact is a configuration error and is rejected at save time — the system never rounds a quantity. |
| BR-UOM-04 | Every UoM carries a **dimension**. Conversion or comparison across dimensions is prohibited: a mass is not a volume. Because a product's base unit is reference data read at runtime, this is enforced in two layers — **configuration time** (a variant's sale UoM dimension must equal its product's base UoM dimension, checked on save and by a database constraint) and **value time** (`uom.Quantity` carries its dimension and returns `ErrDimensionMismatch` on any cross-dimension operation). Compile-time enforcement is not achievable for values whose dimension is not known until the row is read. |
| BR-UOM-05 | The base UoM of a product is immutable once any variant has been ordered or any stock movement recorded. Changing it requires a migration with a recorded decision, not an edit. |
| BR-UOM-06 | The sale UoM and conversion factor of a variant are immutable once the variant appears on an order line. A new pack size is a **new variant with a new SKU**, never an edit to an existing one. |
| BR-UOM-07 | `[MONEY]` `inventory.on_hand` and `inventory.reserved` are always in base UoM. Reserving a cart line converts `qty × conversion_factor` to base **before** the atomic reservation `UPDATE` (BR-INV-02). |
| BR-UOM-08 | `[MONEY]` Availability shown to the customer is `floor(available_base / conversion_factor)` in sale units. The floor is deliberate: a partial pack is not sellable. |
| BR-UOM-09 | `[MONEY]` Prices are always **per sale unit**. Price per base unit is derived for display only and never stored, so the two can never disagree. |
| BR-UOM-10 | `[LEGAL]` Unit pricing (price per kg, per litre, per 100 g) MUST be displayed where legal metrology requires it for pre-packaged goods, computed from the sale price and the conversion factor. |
| BR-UOM-11 | `[LEGAL]` Every invoice line MUST carry the GST **UQC** (Unique Quantity Code — `KGS`, `GMS`, `LTR`, `MLT`, `MTR`, `NOS`, `PCS`, `BOX`, `SQM`) mapped from the sale UoM. The mapping is a fixed table, not free text. |
| BR-UOM-12 | `[LEGAL]` Order lines snapshot the sale UoM, its UQC, the conversion factor, the quantity in sale units **and** the resulting quantity in base units at placement. An invoice must be reproducible without reading current product configuration (BR-ORD-03). |
| BR-UOM-13 | Stock movements are recorded in base UoM with the originating document's sale or purchase UoM and factor retained, so a movement can be explained in the terms the operator used (BR-INV-10). |
| BR-UOM-14 | Returns convert the returned sale quantity back to base using the **snapshotted** factor from the order line, never the current variant configuration. |
| BR-UOM-15 | Receiving converts purchase UoM to base using the purchase conversion factor in force at receipt, snapshotted onto the receipt record. |
| BR-UOM-16 | Shipping weight is derived as `base_quantity × unit_weight_in_base`, so a mass-based product needs no separate weight field and the two can never drift (BR-PRC-07). |
| BR-UOM-17 | Cart line quantity limits (BR-CRT-05) and per-customer limits are expressed in **sale units**, since that is what the customer chose. |
| BR-UOM-18 | Variable-weight ("catch weight") items — where the delivered quantity differs from the ordered quantity — are **out of scope for launch**. A variant MUST NOT be configured to allow it until the pricing and refund rules for weight variance are specified. |
| BR-UOM-19 | The UoM catalogue (code, dimension, base unit, display symbol, UQC) is reference data seeded by migration, not user-editable at runtime. |
| BR-UOM-20 | Every conversion in the codebase goes through the single `uom` package (doc 03 §6.1). Inline multiplication by a factor is prohibited — it is how rounding and dimension errors enter. |

### Worked example

```
Product  : Basmati rice
  base UoM        : GRAM  (dimension: mass)
  on_hand         : 250_000 g   (250 kg)
  reserved        :   3_000 g

Variant  : "1 kg pack"    sale UoM = KG,  factor = 1_000   -> available = 250 units
Variant  : "500 g pack"   sale UoM = GM,  factor =   500   -> available = 494 units
Variant  : "5 kg sack"    sale UoM = BAG, factor = 5_000   -> available =  49 units

Customer buys 3 × "500 g pack"
  reserve base = 3 × 500 = 1_500 g   <- the atomic UPDATE takes 1_500, not 3
  order line snapshot: qty 3, sale UoM GM, UQC "GMS", factor 500, base_qty 1_500
  price is per pack; unit price display = pack price / 0.5 kg
```

Note that the three variants share one pool of grams. This is the whole point: without a base UoM they would each need their own stock number and would drift apart within a week.

---

## 2C. Demand forecasting and replenishment

Stock leaves automatically when an order is paid (BR-INV-05, BR-BAT-15) and every movement is ledgered (BR-INV-10). That ledger, together with the order history, is the input to forecasting: **the platform measures depletion rather than being told about it.**

The output is a *suggestion*, not an order. A human approves every purchase.

### Features

- Daily demand history per variant, corrected for stockouts and promotions.
- Forecast of expected demand over the supplier's lead time.
- Reorder point, safety stock and suggested order quantity per variant.
- Purchase suggestions grouped by supplier, ready to become a purchase order.
- Slow-moving and dead-stock reporting; overstock flagged before it expires.
- Forecast accuracy tracked and reported.

### 2C.1 Demand measurement

| ID | Rule |
|---|---|
| BR-FCT-01 | Demand is measured in **base UoM per variant per day**, derived from order lines reaching `paid` or `confirmed`, net of cancellations and returns (BR-UOM-01). |
| BR-FCT-02 | `[MONEY]` **Sales are censored demand.** A day on which a variant was unavailable records zero sales but not zero demand. Availability is tracked per variant per day, and unavailable days are excluded from the average rather than counted as zeros. Ignoring this is the single most common forecasting error, and it systematically under-orders exactly the products that keep selling out. |
| BR-FCT-03 | Days under an active promotion or campaign are flagged, so a discount-driven spike does not become the new baseline (BR-CMP-01). Promotional demand is forecast separately from baseline demand. |
| BR-FCT-04 | Demand history is aggregated by a scheduled worker into a materialised view. Forecast queries never scan live order tables (BR-RPT-01, DB-011). |
| BR-FCT-05 | A variant with fewer than 28 days of availability history is marked **insufficient history**. It gets a category-analogue estimate or manual entry, and the suggestion states which — never a confident number derived from two weeks of noise. |

### 2C.2 Forecast

| ID | Rule |
|---|---|
| BR-FCT-10 | The launch method is deliberately simple: a trailing moving average of corrected daily demand, multiplied by a day-of-week and month-of-year seasonality index computed per category. Exponential smoothing is a later refinement, and only if measurement shows the simple model is the limiting factor. |
| BR-FCT-11 | The forecast produces both an expected daily demand and its **variability** (standard deviation). A forecast without a variance cannot produce a defensible safety stock. |
| BR-FCT-12 | Indian retail seasonality is real and large. Festival periods are held as reference data with their dates per year, and are treated as their own seasonality factor rather than being smoothed away. |
| BR-FCT-13 | Every forecast run **snapshots its inputs** — the demand window, the corrections applied, the seasonality indices — with the suggestion, so a purchasing decision can be re-examined months later (BR-VER-03). |
| BR-FCT-14 | Forecast accuracy (MAPE against realised demand) is computed weekly per category and reported. A forecast nobody measures is a guess with a chart. |

### 2C.3 Replenishment suggestions

| ID | Rule |
|---|---|
| BR-FCT-20 | Safety stock = `z × σ_demand × √(lead time days)`, where `z` is the service-level factor configured per category (default 95%). |
| BR-FCT-21 | Reorder point = `expected daily demand × lead time days + safety stock`. Lead time comes from the supplier record (BR-SUP-01) and, where enough history exists, from that supplier's **actual** delivery performance rather than its promised lead time. |
| BR-FCT-22 | A suggestion is raised when projected available stock — on hand, less reserved, plus quantities already on open purchase orders — falls below the reorder point. Ignoring stock already on order is how a business double-orders. |
| BR-FCT-23 | Suggested quantity covers demand to a configured target horizon, then is rounded **up** to the supplier's purchase UoM and minimum order quantity (BR-UOM-15). |
| BR-FCT-24 | `[MONEY]` **The suggestion is capped by shelf life.** For an `expiry_tracked` product, the quantity must not exceed what the forecast says can be sold before the incoming batch's expected expiry, less the minimum shelf life required at sale (BR-BAT-20). Ordering stock that will expire on the shelf is worse than a stockout, because it costs money rather than only losing a sale. |
| BR-FCT-25 | Suggestions are grouped by supplier and show the working: current stock, on order, corrected daily demand, lead time, safety stock, reorder point, resulting quantity, and the cost at the supplier's last price. A suggestion a buyer cannot audit will not be trusted, and should not be. |
| BR-FCT-26 | `[SEC]` A suggestion **never becomes a purchase order automatically**. A human holding `purchasing:write` reviews, adjusts and approves it, and the approval is audited (BR-SUP-04). |
| BR-FCT-27 | An adjusted suggestion records what the buyer changed and why. Those adjustments are the feedback that shows where the model is systematically wrong. |
| BR-FCT-28 | Suggestions are suppressed for archived, discontinued and `backorder`-only variants, and for variants with an open suggestion already awaiting approval. |

### 2C.4 The other direction — overstock and dead stock

| ID | Rule |
|---|---|
| BR-FCT-30 | Days-of-cover is computed per variant and per batch: current stock divided by corrected daily demand. It is the number that makes both stockouts and overstock visible on one screen. |
| BR-FCT-31 | `[MONEY]` A batch whose remaining shelf life is shorter than its days-of-cover **will not sell in time**. That is the trigger for a markdown suggestion, and it is raised early enough for a discount to work rather than at the point of write-off (BR-BAT-23, BR-BAT-35). |
| BR-FCT-32 | Slow-moving stock (no sale in a configured window) and dead stock (no sale since receipt) are reported with their held cost, so capital tied up in stock is visible to finance. |
| BR-FCT-33 | Forecast, suggestion and markdown-trigger events are emitted per doc 06 §3 (`forecast.generated`, `replenishment.suggested`, `replenishment.approved`, `stock.cover_breach`, `markdown.suggested`). |

---

## 3. Pricing and tax

### Features

- Per-variant base price and MRP.
- Automatic GST computation by place of supply.
- Tax-inclusive display pricing with a tax breakdown at checkout.
- Shipping charge calculation by weight slab and destination zone.
- Full price breakdown returned by the server on every cart and checkout response.

### Business rules

| ID | Rule |
|---|---|
| BR-PRC-01 | `[MONEY]` The server recomputes the entire price breakdown from the database on every cart read, checkout initiation, and payment confirmation. A total supplied by the client is discarded. |
| BR-PRC-02 | `[MONEY]` Order of computation is fixed: line subtotal → line discount → line taxable value → line GST → order shipping → shipping GST → order total. Changing this order changes the tax and is a breaking change. |
| BR-PRC-03 | `[MONEY]` Rounding is round-half-up to the nearest paise, applied per line, then summed. The order total is never rounded again after summation. |
| BR-PRC-04 | `[LEGAL]` GST split is determined by place of supply: seller state == shipping state → CGST + SGST, each at half the rate; otherwise → IGST at the full rate. |
| BR-PRC-05 | `[LEGAL]` `place_of_supply` is captured on the order at placement from the shipping address state and never recomputed afterwards, even if the address is later corrected. |
| BR-PRC-06 | `[LEGAL]` Displayed storefront prices are GST-inclusive. The checkout must show the tax component explicitly, split into CGST/SGST or IGST. |
| BR-PRC-07 | Shipping charge is computed from billable weight (`max(actual, volumetric)`) against the destination zone slab. Free-shipping thresholds are evaluated on the post-discount order subtotal. |
| BR-PRC-08 | `[LEGAL]` Shipping is taxed at its own GST rate and appears as a separate invoice line. |
| BR-PRC-09 | A price quoted at checkout initiation is held for the reservation window (15 minutes). If payment completes within the window, that price stands even if the catalog price changed. |
| BR-PRC-10 | If a reservation expires and the customer retries, prices are re-quoted from live catalog data and the customer is shown what changed before paying. |
| BR-PRC-11 | `[MONEY]` The order total sent to Razorpay must equal the stored order total in paise, exactly. Any mismatch aborts the payment and raises a critical alert. |

---

## 3A. Tax rate versioning and effective-dated reference data

GST rates change by government notification, sometimes with a future effective date, sometimes with a slab boundary tied to transaction value. A rate is therefore never a single number on a product — it is a **row in a versioned, effective-dated table**, and the correct rate is a function of `(HSN code, date, transaction value)`.

### 3A.1 GST rate history

| ID | Rule |
|---|---|
| BR-TAX-01 | `[LEGAL][MONEY]` GST rates live in an effective-dated `gst_rates` table keyed by HSN code. A rate is **never updated in place**. A change inserts a new row with a new effective range; the previous row is closed, not overwritten. |
| BR-TAX-02 | `[LEGAL][MONEY]` The rate applied to a transaction is the rate in force at the **taxable event date**, not the current date. Historical recomputation must therefore be possible for any past date. |
| BR-TAX-03 | `[LEGAL]` The taxable event date defaults to order placement. The precise time-of-supply determination MUST be confirmed with the tax advisor and recorded in `docs/decisions/` before launch — this document does not assert it. |
| BR-TAX-04 | `[LEGAL][MONEY]` Effective ranges for one HSN code MUST NOT overlap and MUST NOT leave a gap. This is enforced by a database exclusion constraint, not by application code. |
| BR-TAX-05 | `[LEGAL]` Every rate row records the government notification reference that authorises it, the actor who entered it, and the timestamp. A rate without a citation MUST NOT be activated. |
| BR-TAX-06 | `[LEGAL][MONEY]` Value-slab rates are supported: a rate row may carry a transaction-value range (India applies different rates to some categories above and below a price threshold). Slab boundaries are evaluated on the per-unit taxable value, and the applicable rule MUST be recorded on the order line. |
| BR-TAX-07 | `[LEGAL][MONEY]` Compensation cess, where applicable, is a separate rate on the same row and appears as a separate invoice component. It MUST NOT be folded into the GST rate. |
| BR-TAX-08 | `[MONEY]` A future-dated rate is entered ahead of time and takes effect by date lookup alone. No job "applies" a rate change, because a job can fail to run; a date comparison cannot. |
| BR-TAX-09 | `[SEC]` Entering or amending a rate requires the `tax:write` role and second-actor approval, and is audited (BR-ADM-04). |
| BR-TAX-10 | `[LEGAL][MONEY]` Order lines snapshot the applied rate, the cess, the rate row's ID and the notification reference (BR-ORD-03). An invoice reprinted in five years shows what was charged and cites why — without depending on the rate table still containing that row. |
| BR-TAX-11 | `[MONEY]` A rate change MUST NOT reprice an order already placed, nor an active reservation whose price is locked (BR-PRC-09). Carts are re-quoted on the next read, and the change is shown to the customer before payment (BR-CHK-07). |
| BR-TAX-12 | A rate-change rehearsal is required: before activation, a report shows every affected product, the old and new rate, and the resulting price change at current prices. Activating without that report is prohibited. |
| BR-TAX-13 | `rate_changed` and `rate_scheduled` events are emitted with the notification reference (doc 06 §3). |

```sql
create extension if not exists btree_gist;

create table gst_rates (
  id               uuid primary key default gen_random_uuid(),
  hsn_code         text   not null,
  rate_bps         int    not null check (rate_bps >= 0),
  cess_bps         int    not null default 0 check (cess_bps >= 0),
  min_value_paise  bigint,                    -- value-slab lower bound, null = unbounded
  max_value_paise  bigint,                    -- value-slab upper bound, null = unbounded
  effective        daterange not null,        -- [from, to) — to is null-unbounded for current
  notification_ref text   not null,           -- the authorising government notification
  created_by       text   not null,
  approved_by      text   not null,           -- BR-TAX-09, second actor
  created_at       timestamptz not null default now(),

  -- No two rate rows for the same HSN and value slab may overlap in time. Ever.
  exclude using gist (
    hsn_code with =,
    int8range(coalesce(min_value_paise, 0), coalesce(max_value_paise, 9223372036854775807)) with &&,
    effective with &&
  )
);
create index on gst_rates (hsn_code, effective);
```

The exclusion constraint is the point: an overlapping or duplicated rate becomes impossible at the storage layer, so no amount of application-level carelessness can produce two answers to "what was the rate on 14 March".

### 3A.2 Everything else that must be versioned

The same discipline applies to every input that affects money, a legal document, or a customer's expectations. **Update-in-place is prohibited for all of them.**

| ID | Rule |
|---|---|
| BR-VER-01 | Reference and configuration data affecting money or legal output MUST be effective-dated and append-only: GST rates, price lists, shipping rate slabs, COD limits, loyalty earn and redemption rates, return windows, markdown rules, UoM conversion factors, coupon definitions, and campaign offers. |
| BR-VER-02 | The value in force is resolved by date lookup. Code MUST NOT read "the current row" and assume it was always current. |
| BR-VER-03 | Every transactional record snapshots the version it used — the row ID, not just the value — so the value and its justification are both recoverable (BR-ORD-03, BR-TAX-10). |
| BR-VER-04 | `[LEGAL]` Customer-facing policy documents — terms, privacy policy, return policy, loyalty terms — are versioned, and the version the customer accepted is stored on the order or the consent record. "Which terms did they agree to" MUST be answerable. |
| BR-VER-05 | Event schemas are versioned; a shape change increments the version and the old shape remains readable (EVT-004). |
| BR-VER-06 | The API is versioned at the path (`/api/v1`). A breaking change is a new version, never a redefinition of the old one. |
| BR-VER-07 | Migrations are forward-only, sequential and never edited after merge. The schema version is part of the startup log (HLT-004). |
| BR-VER-08 | Deployed artefacts carry version and git SHA, reported in the startup log and in `/readyz`, so a production behaviour can be tied to a commit. |
| BR-VER-09 | A version is never reused or silently corrected. A mistake is fixed by a new version plus a compensating record — never by editing history. |
| BR-VER-10 | `[SEC]` All version changes to money-affecting or legal data are audited with actor, approver, before, after and reason (BR-ADM-06). |

---

## 4. Cart

### Features

- Guest and authenticated carts.
- Persistent across sessions and devices (for logged-in customers).
- Line add / update quantity / remove / clear.
- Live re-pricing and availability check on every read.
- Save for later / move to wishlist.
- Merge of guest cart into customer cart at login.

### Business rules

| ID | Rule |
|---|---|
| BR-CRT-01 | A cart is server-owned. The client sends `variant_id` and `qty` only. |
| BR-CRT-02 | Guest carts are keyed by an opaque cart ID in an `HttpOnly` cookie and expire after 30 days of inactivity. |
| BR-CRT-03 | `[SEC]` A cart may only be read or modified by its owning session. Cart IDs are unguessable (UUIDv7) and ownership is checked on every operation. |
| BR-CRT-04 | Adding a variant already in the cart increments the existing line rather than creating a second line. |
| BR-CRT-05 | Line quantity is `1 <= qty <= min(50, available, per_customer_limit)`. Requests above the cap are clamped to the cap and the response states the clamp. |
| BR-CRT-06 | On every cart read the server re-validates: variant still active, still in stock, current price. Changes are returned as explicit `line_notices` — silent substitution is prohibited. |
| BR-CRT-07 | A line whose variant became inactive or archived is marked unavailable and blocks checkout until removed. |
| BR-CRT-08 | On login, the guest cart merges into the customer cart: quantities for the same variant are summed, then clamped per BR-CRT-05. The guest cart is deleted after a successful merge. |
| BR-CRT-09 | A cart with zero purchasable lines cannot proceed to checkout. |
| BR-CRT-10 | Cart contents are held in Redis for speed and in PostgreSQL for durability. Redis loss must not lose a logged-in customer's cart. |

---

## 5. Discounts and coupons

### Features

- Coupon codes: percentage, fixed amount, free shipping, buy-X-get-Y.
- Automatic (codeless) promotions.
- Conditions: minimum order value, specific products/categories/variants, first-order-only, customer segment.
- Usage limits: total, per customer, per day.
- Validity window with start and end.
- Stacking policy.

### Business rules

| ID | Rule |
|---|---|
| BR-DSC-01 | `[MONEY]` Discount eligibility and amount are computed server-side at every cart read and re-verified at payment confirmation. A coupon valid at cart time but expired at payment time is rejected. |
| BR-DSC-02 | `[MONEY]` A discount can never reduce the order total below zero. Fixed-amount discounts are capped at the discountable subtotal. |
| BR-DSC-03 | Discounts apply to the pre-tax line value. GST is computed on the discounted taxable value, never on the pre-discount value. |
| BR-DSC-04 | Percentage discounts respect an optional `max_discount_paise` cap. |
| BR-DSC-05 | Default stacking policy: **one coupon code per order**, which may combine with automatic promotions. Coupon-on-coupon requires an explicit `stackable` flag on both. |
| BR-DSC-06 | Free-shipping coupons zero the shipping charge but not its tax treatment; the invoice shows shipping at zero. |
| BR-DSC-07 | Usage counters increment only on transition to `paid` (or `confirmed` for COD), never at cart or checkout time. |
| BR-DSC-08 | `[MONEY]` Usage counters decrement on full cancellation or full refund, restoring the customer's entitlement. Partial refunds do not restore usage. |
| BR-DSC-09 | `[SEC]` Coupon validation is rate-limited per session and per IP. Invalid and non-existent codes return the identical error — the response must not reveal whether a code exists. |
| BR-DSC-10 | Code matching is case-insensitive and whitespace-trimmed. Codes are stored uppercase. |
| BR-DSC-11 | First-order-only coupons check for any prior order in `paid` or beyond, matched on customer ID **and** verified phone number. |
| BR-DSC-12 | A coupon is invalid outside `[starts_at, ends_at)`. Boundary behaviour: `starts_at` inclusive, `ends_at` exclusive. |
| BR-DSC-13 | Discount allocation across lines is proportional to line taxable value, with the rounding remainder assigned to the highest-value line so allocations sum exactly to the discount. |

---

## 5A. Campaigns

A campaign is the **orchestration** of a promotion: who, what offer, which channel, when, and how it is measured. It is not a second discount engine.

| ID | Rule |
|---|---|
| BR-CMP-01 | A campaign composes existing mechanisms — coupons (§5), automatic promotions, batch markdown (§2A.5), loyalty multipliers (§5B), merchandising slots. It MUST NOT implement its own price arithmetic. All money still flows through `pricing` (BR-PRC-01, DRY). |
| BR-CMP-02 | A campaign declares: name, objective, audience segment, offer, channels, schedule, budget caps, holdout percentage, and owner. A campaign missing an objective or a measurement plan MUST NOT be launched. |
| BR-CMP-03 | Campaign states are `draft → scheduled → running → paused → ended`, with `cancelled` from any pre-`ended` state. Transitions are audited (BR-ORD-01 pattern). |
| BR-CMP-04 | `[SEC]` Creating or editing a campaign requires the `marketing:write` role. A campaign whose maximum discount exposure exceeds a configured threshold requires second-actor approval before it can leave `draft` (BR-ADM-04). |
| BR-CMP-05 | `[MONEY]` Every campaign carries hard caps: maximum total discount value, maximum redemptions, and maximum redemptions per customer. On reaching a cap the campaign auto-pauses and alerts. Caps are enforced server-side at redemption, never by the UI. |
| BR-CMP-06 | `[MONEY]` A campaign MUST NOT be able to produce a negative margin line without explicit `loss_leader` approval recorded on the campaign. Margin is computed against batch cost (BR-BAT-38). |
| BR-CMP-07 | Campaign eligibility is evaluated server-side at every cart read and re-verified at payment (BR-DSC-01). A campaign that ended between cart and payment does not apply. |
| BR-CMP-08 | Flash sales and limited-stock campaigns rely on the normal reservation path (BR-INV-02, BR-BAT-11). A campaign MUST NOT introduce a separate stock path, and MUST NOT permit overselling as an "acceptable" trade-off for the event. |

### Audience and consent

| ID | Rule |
|---|---|
| BR-CMP-10 | Segments are saved, named, versioned rule sets over customer attributes and behaviour (RFM, category affinity, last order date, lifetime value, loyalty tier). A segment's definition at send time is snapshotted onto the send record so a campaign's audience is reproducible. |
| BR-CMP-11 | `[LEGAL]` Marketing sends require explicit, recorded opt-in per channel (BR-NOT-04). Consent for email does not imply consent for SMS or WhatsApp. Consent records store timestamp, source, and IP. |
| BR-CMP-12 | `[LEGAL]` Indian SMS marketing MUST use DLT-registered headers and approved templates, and MUST respect DND registration and permitted send windows. A send that cannot prove template registration MUST NOT dispatch. |
| BR-CMP-13 | `[LEGAL]` WhatsApp sends use approved message templates within the platform's policy window. Transactional templates MUST NOT be used to carry marketing content. |
| BR-CMP-14 | `[LEGAL]` Every marketing message carries a working opt-out. Opt-out takes effect immediately across all campaigns, and is honoured even mid-send. |
| BR-CMP-15 | Fatigue caps: a customer receives at most N marketing messages per channel per rolling 7 days (default 2 email, 1 SMS). The cap is enforced at dispatch, across campaigns, not per campaign. |
| BR-CMP-16 | Suppression lists (opted out, bounced, complained, refunded-and-churned, employees, test accounts) are applied at dispatch time as the final filter. |
| BR-CMP-17 | `[SEC]` Audience exports containing contact details require `admin` or `marketing:export` and are audited with row count and segment definition (BR-RPT-05). |

### Delivery and measurement

| ID | Rule |
|---|---|
| BR-CMP-20 | Sends are queued and throttled (QUE-002), never dispatched inline. A large campaign MUST NOT starve transactional messages — marketing runs on the `low` queue, transactional on `default` or `critical` (BR-NOT-01). |
| BR-CMP-21 | Every send is recorded per recipient with status, provider message ID, and correlation ID, so a customer's "I got three of these" is answerable (doc 06 §7). |
| BR-CMP-22 | A **holdout** group (default 5%) is excluded from every measured campaign, so incremental effect is measurable rather than assumed. Campaigns without a holdout MUST record why. |
| BR-CMP-23 | Attribution captures UTM parameters on landing, persists them to the session and onto the order, and additionally attributes by coupon code. Both are reported; they MUST NOT be silently merged. |
| BR-CMP-24 | `[MONEY]` Campaign reporting shows revenue, discount cost, **margin after batch cost**, redemption rate, and incremental lift against the holdout. Gross revenue alone is not an outcome. |
| BR-CMP-25 | Campaign events (`campaign.launched`, `campaign.paused`, `campaign.capped`, `campaign.send_queued`, `campaign.send_delivered`, `campaign.send_failed`, `campaign.redeemed`, `campaign.opted_out`) are emitted per doc 06 §3. |

---

## 5B. Loyalty points

Points serve two purposes, and the second is the more valuable: they give repeat customers a reason to return, and they give the business a legitimate reason to hold a verified contact with recorded consent.

| ID | Rule |
|---|---|
| BR-LOY-01 | `[MONEY]` Points are an **append-only ledger** of integer entries (earn, redeem, expire, reverse, adjust), each referencing its source order or actor. The balance is derived from the ledger; a stored balance is a maintained rollup reconciled against the ledger, never an independent number (same discipline as BR-BAT-01). |
| BR-LOY-02 | `[MONEY]` Points are integers. Earn rates are basis points of the eligible amount, and the result is floored. Fractional points do not exist. |
| BR-LOY-03 | `[MONEY]` Points are earned on the **net paid amount excluding GST, shipping, and any amount settled by points**. Earning points on points is prohibited. |
| BR-LOY-04 | `[MONEY]` Points are credited only after the return window closes on a delivered order (BR-RET-01), not at payment. Crediting earlier makes buy-redeem-return a free-money loop. Pending points are visible to the customer as pending, with their release date. |
| BR-LOY-05 | `[MONEY]` Redemption converts points to a discount at a fixed, configured rate (paise per point). The rate is versioned; a change never revalues points already earned without a recorded decision. |
| BR-LOY-06 | Redemption is a **discount on the order**, not a payment method. It reduces the taxable value like any other discount and flows through `pricing` (BR-DSC-03, BR-CMP-01). |
| BR-LOY-07 | `[LEGAL]` The GST treatment of points redemption MUST be confirmed with the tax advisor before launch and recorded in `docs/decisions/`. This document does not assert it. |
| BR-LOY-08 | **Selective eligibility.** Points are redeemable only against eligible lines, determined by an allowlist of categories, products or variants on the loyalty programme. Ineligible by default: gift cards, already-marked-down batches (BR-BAT-30), and items flagged `loyalty_excluded`. |
| BR-LOY-09 | `[MONEY]` Redemption is capped at a configured percentage of the eligible subtotal per order, and at a maximum points-per-order value. Both are enforced server-side. |
| BR-LOY-10 | `[MONEY]` Points are consumed **oldest-earned-first (FIFO)** so that the balance nearest expiry is used first — the same reasoning as FEFO on stock. |
| BR-LOY-11 | Points expire a configured period after earning (default 12 months). Expiry is a ledger entry, never a deletion. Customers are notified before expiry — which is itself a legitimate re-contact. |
| BR-LOY-12 | `[MONEY]` The ledger balance can never go negative. Redemption is an atomic conditional write against the ledger rollup; read-then-write is prohibited (DB-030). |
| BR-LOY-13 | `[MONEY]` On cancellation or refund: points earned on that order are reversed, and points redeemed on it are restored — both as new ledger entries, never by editing history. A partial refund reverses proportionally (BR-DSC-08 pattern). |
| BR-LOY-14 | `[MONEY]` If reversal would push the balance negative because the points were already spent, the balance floors at zero and the shortfall is recorded as `clawback_shortfall` for review. Silent negative balances are prohibited. |
| BR-LOY-15 | Enrolment requires a **verified phone number** (BR-IDN-04). This is what makes the contact worth holding. |
| BR-LOY-16 | `[LEGAL]` Enrolment and marketing consent are **separate, separately-recorded actions**. Enrolling in loyalty MUST NOT be treated as consent to marketing, and consent MUST NOT be a precondition of earning points. Bundling them is a compliance failure and a dark pattern. |
| BR-LOY-17 | `[SEC]` Manual point adjustments require the `loyalty:write` role, a reason, and an audit entry. Adjustments above a threshold require second-actor approval (BR-ADM-04). |
| BR-LOY-18 | `[SEC]` Anti-abuse: enrolment is rate-limited per phone and per device; one programme membership per verified phone; referral self-matching on phone, device and payment instrument is blocked; guest orders claimed retroactively earn points only once. |
| BR-LOY-19 | `[MONEY]` The outstanding points balance is a financial liability. Total outstanding points, their paise value, breakage rate and expiry schedule are reported monthly to finance. |
| BR-LOY-20 | The customer can see a full statement: every earn, redeem, expiry and reversal with its order reference and date. An unexplainable balance is a support incident. |
| BR-LOY-21 | Loyalty events (`loyalty.enrolled`, `points.pending`, `points.credited`, `points.redeemed`, `points.expired`, `points.reversed`, `points.adjusted`) are emitted per doc 06 §3. |

---

## 6. Customer identity

### Features

- Registration by email + password or phone + OTP.
- Login by either method; phone OTP is the primary path.
- Guest checkout with no account.
- Password reset by email link.
- Profile, saved addresses, order history.
- Session management with "sign out everywhere".

### Business rules

| ID | Rule |
|---|---|
| BR-IDN-01 | `[SEC]` Passwords are hashed with Argon2id. Plaintext or reversibly-encrypted passwords are prohibited. Minimum length 10; no composition rules; the top-10k common password list is rejected. |
| BR-IDN-02 | `[SEC]` Sessions are opaque IDs in `HttpOnly; Secure; SameSite=Lax` cookies, with the session body in Redis. Tokens in `localStorage` are prohibited. |
| BR-IDN-03 | `[SEC]` Session TTL is 30 days sliding. Sessions are invalidated on password change, on explicit sign-out-everywhere, and on role change. |
| BR-IDN-04 | `[SEC]` OTP is 6 digits, valid for 5 minutes, single-use, with a maximum of 5 verification attempts before the OTP is burned. |
| BR-IDN-05 | `[SEC]` OTP send is rate-limited: 3 per phone number per 15 minutes, 10 per IP per hour, with exponential backoff on repeat requests. |
| BR-IDN-06 | `[SEC]` Login, registration and password-reset responses must not reveal whether an account exists. Reset always responds "if an account exists, we've sent a link". |
| BR-IDN-07 | `[SEC]` Password reset tokens are single-use, expire in 30 minutes, and are stored hashed. Using one invalidates all existing sessions for that customer. |
| BR-IDN-08 | Email is unique per account, case-insensitively. Phone number is unique per account, stored in E.164. |
| BR-IDN-09 | Guest checkout must remain available. Account creation is never a precondition for placing an order. |
| BR-IDN-10 | After a guest order, an account may be claimed via the same email or phone; matching past guest orders are linked on first verified login. |
| BR-IDN-11 | `[SEC]` Failed login attempts are rate-limited per account and per IP independently. Account lockout is temporary (15 minutes), never permanent, to avoid a lockout denial-of-service. |
| BR-IDN-12 | `[SEC]` Authentication events (login success/failure, OTP send/verify, password change, reset) are written to the audit log with IP and user agent. Credentials and OTP values are never logged. |

---

## 7. Addresses and serviceability

### Features

- Multiple saved addresses per customer, with a default.
- Separate billing and shipping addresses.
- Pincode serviceability check with estimated delivery date.
- COD availability by pincode.

### Business rules

| ID | Rule |
|---|---|
| BR-ADR-01 | Required fields: name, phone (E.164), line 1, city, state, pincode, country. Line 2 and landmark are optional. |
| BR-ADR-02 | Indian pincode must be exactly 6 digits and must resolve to a known state. A pincode whose state contradicts the entered state is rejected. |
| BR-ADR-03 | `[SEC]` An address may only be read, edited or deleted by its owning customer. Checked on every operation. |
| BR-ADR-04 | Deleting an address is a soft delete. Addresses referenced by an order are retained. |
| BR-ADR-05 | `[LEGAL]` Order addresses are snapshotted onto the order at placement. Editing a saved address never alters a past order or invoice. |
| BR-ADR-06 | Checkout is blocked for a non-serviceable pincode, with the reason stated and an alternative requested. |
| BR-ADR-07 | If no billing address is supplied, the shipping address is copied as the billing address at placement. |

---

## 8. Checkout

### Features

- Single-page checkout: address → shipping method → payment.
- Guest and authenticated flows.
- Order summary with the full tax and discount breakdown.
- Coupon entry.
- Payment method selection (Razorpay online, or COD).

### Business rules

| ID | Rule |
|---|---|
| BR-CHK-01 | `[MONEY]` Checkout initiation performs, in one transaction: re-price the cart, validate every line, reserve stock, and create the order in `pending_payment`. If any step fails, nothing is persisted. |
| BR-CHK-02 | `[MONEY]` Checkout initiation requires an `Idempotency-Key` header. A repeat with the same key returns the original order and the original Razorpay order ID; it never creates a second order. |
| BR-CHK-03 | Idempotency keys are stored for 24 hours with the original response, scoped to the session. |
| BR-CHK-04 | `[SEC]` All state-changing checkout requests require a valid CSRF token. |
| BR-CHK-05 | Checkout is rate-limited per session to prevent reservation squatting on limited stock. |
| BR-CHK-06 | If any line became unavailable between cart and checkout, checkout fails with a per-line explanation. Automatic removal or substitution is prohibited. |
| BR-CHK-07 | If the recomputed total differs from what the customer was last shown, the customer is shown the difference and must explicitly confirm before payment. |
| BR-CHK-08 | Email is mandatory at checkout (order confirmation and invoice delivery); phone is mandatory (delivery contact). |
| BR-CHK-09 | An order in `pending_payment` older than the reservation window is auto-cancelled and its stock released. |
| BR-CHK-10 | A customer may have at most 3 concurrent `pending_payment` orders. |

---

## 8A. Interruption, recovery and resumption

Connections drop. A customer loses signal mid-payment, closes the tab, or their phone dies between the bank's OTP screen and the return redirect. UPI in particular settles a beat *after* the customer has given up and navigated away.

> **The governing principle: never tell a customer their payment failed when it may have succeeded, and never let a captured payment exist without an order.** Of the two ways to be wrong, silently taking money is far worse than showing a "we're confirming this" state for thirty seconds.

### The two invariants

Everything in this section exists to hold these two, and every rule below cites which one it defends. If a proposed change breaks either, the change is wrong.

| | Invariant | How it is held |
|---|---|---|
| **I1** | **A customer is never charged twice for one order.** | One idempotency key per attempt, persisted client-side (BR-RCV-01) · one provider order reused on retry (BR-RCV-02) · provider idempotency keys on every outbound retry (BR-RCV-31) · an unknown outcome is reconciled, never assumed failed and retried (BR-RCV-32) · duplicate capture detected and alerted (BR-RCV-22) |
| **I2** | **Nothing is lost — no cart, no order, no captured payment, no event.** | Server-owned durable cart (BR-RCV-07) · atomic rollback on cancellation, so there is no partial state (BR-RCV-05) · the webhook confirms the order whether or not the browser returns (BR-RCV-04) · orphan payments detected within 15 minutes (BR-RCV-20) · events written in the same transaction as the change (EVT-001) |

The two pull in opposite directions under uncertainty: retrying protects against loss and risks a double charge; refusing to retry protects against a double charge and risks loss. **The resolution is always the same — do not guess, reconcile.** An unknown outcome is recorded as unknown and resolved against the provider's own record (BR-RCV-21/32), which is the only source that can answer whether money actually moved.

### 8A.1 Resuming an interrupted checkout

| ID | Rule |
|---|---|
| BR-RCV-01 | `[MONEY]` The idempotency key for a checkout is generated **once per attempt and persisted client-side with the cart**, so it survives a reload, a crash and a device restart. A retry after any interruption presents the same key and therefore returns the original order rather than creating a second one (BR-CHK-02). |
| BR-RCV-02 | `[MONEY]` If a customer retries while an unexpired `pending_payment` order exists for their cart, the server **reuses that order and its existing provider order id**. It does not create a second provider order, because two live provider orders for one cart is how a customer gets charged twice. |
| BR-RCV-03 | On returning to the site, a customer with a `pending_payment` order is shown it, with two clear choices: resume payment, or abandon and release the stock. They are never silently dropped back into an empty cart while their reservation is still held. |
| BR-RCV-04 | `[MONEY]` A customer who never returns still gets their order. The webhook is the source of truth (BR-PAY-02), so a charge that succeeded is confirmed, stock is committed, and the confirmation email is sent, regardless of whether the browser ever came back. |
| BR-RCV-05 | `[MONEY]` A request whose context is cancelled mid-transaction rolls back completely. Partial state is impossible: rollback runs on a context detached from the cancelled one, so the failure that caused the cancellation cannot also prevent the cleanup. |
| BR-RCV-06 | An abandoned `pending_payment` order expires with its reservation and releases the stock (BR-CHK-09, BR-INV-04). Expired orders are retained for funnel analysis (BR-ORD-12). |
| BR-RCV-07 | A cart survives the interruption. It is server-owned and durable in PostgreSQL, not held in browser state (BR-CRT-01, BR-CRT-10). |

### 8A.2 What the customer sees

| ID | Rule |
|---|---|
| BR-RCV-10 | `[MONEY]` When the outcome is not yet known, the storefront shows an explicit **"confirming your payment"** state and polls order status. It MUST NOT report failure on a timeout, a dropped connection, or a missing callback — none of those mean the payment failed. |
| BR-RCV-11 | The confirming state has a bounded, stated duration and then hands off to "we'll email you as soon as this settles", with the order number visible. A spinner with no end is worse than an honest wait. |
| BR-RCV-12 | Payment failure is reported **only** on a verified failure signal: a `payment.failed` webhook, or a provider status of failed from the reconciliation poll (BR-RCV-21). |
| BR-RCV-13 | The client retries safe methods automatically with backoff. It MUST NOT automatically retry a state-changing request without its idempotency key — an automatic retry without one is a duplicate-order generator. |
| BR-RCV-14 | The order number and the `X-Request-Id` are shown on any error screen, so a customer contacting support gives them everything needed to find what happened (OBS-012, PRB-001). |

### 8A.3 Money without an order — the case that must never be silent

| ID | Rule |
|---|---|
| BR-RCV-20 | `[MONEY]` **Orphan payment detection.** A captured payment whose order has not reached `paid` within a configured window (default 15 minutes) raises a **critical alert** and lands on a reconciliation queue. This is the one failure that takes a customer's money and gives them nothing, so it is never left to be discovered by the customer. |
| BR-RCV-21 | `[MONEY]` A reconciliation job polls the provider for the status of every `pending_payment` order older than the reservation window. Webhooks are the primary path; this is the belt-and-braces path for a webhook that was never delivered. It is idempotent and resolves through the same ledger (BR-PAY-07). |
| BR-RCV-22 | `[MONEY]` **Duplicate capture.** Two captured payments against one order raise a critical alert. The excess is refunded, but only after human approval — an automatic refund on a detection rule is itself a way to lose money to a false positive. |
| BR-RCV-23 | `[MONEY]` An amount mismatch between the captured payment and the stored order total never marks the order paid; it alerts for manual review (BR-PAY-09). |
| BR-RCV-24 | `[MONEY]` Orphan payments, duplicate captures, amount mismatches and unresolved `pending_payment` orders are reported daily to finance with their value, so the size of the problem is a number rather than an impression. |
| BR-RCV-25 | Recovery events are emitted per doc 06 §3: `checkout.resumed`, `checkout.abandoned`, `payment.orphan_detected`, `payment.duplicate_capture`, `payment.reconciled`, `order.expired`. |

### 8A.4 Server-side resilience

| ID | Rule |
|---|---|
| BR-RCV-30 | `[MONEY]` No external call happens inside a database transaction (MOD-09). A provider timeout must never hold locks, and a rollback must never need to un-do a charge. |
| BR-RCV-31 | Every outbound provider call carries a deadline and a bounded retry with backoff and jitter (GO-033). A retried create-order call carries the provider's idempotency key so the retry cannot create a second provider order. |
| BR-RCV-32 | `[MONEY]` A provider call whose outcome is **unknown** — a timeout, a connection reset — is recorded as unknown and resolved by reconciliation. It is never assumed to have failed, because assuming failure is what produces the second charge. |
| BR-RCV-33 | The server drains in-flight requests on shutdown within the grace period, so a deploy during a customer's checkout completes their request rather than resetting it. |
| BR-RCV-34 | Webhook delivery is retried by the provider on any non-2xx, and the handler is idempotent, so a webhook arriving during a restart is simply redelivered (BR-PAY-07, BR-PAY-08). |

### 8A.5 The counter is online — withdrawn: offline selling

> **Withdrawn by [ADR 0006](decisions/0006-fully-online.md).** Steleios is fully online. Nothing is installed at the shop, the counter runs in a browser, and there is no offline selling.
>
> **Every `BR-OFF-*` rule below is withdrawn and MUST NOT be implemented.** They are retained, struck through in intent rather than deleted, because the lease design they describe is the correct answer *if* offline selling is ever genuinely required — and reintroducing it from scratch would risk arriving at the naive version that oversells stock and charges stale prices. Treat this section as a design already done, not as a backlog.

**What happens during an outage:** the counter stops. The shop falls back to a paper bill book and enters the sales afterwards, which is what most small retailers already do. Mitigation is operational — a 4G hotspot as automatic failover costs a few hundred rupees a month and fixes connectivity for the storefront and the card terminal too.

| ID | Rule |
|---|---|
| BR-ONL-01 | `[MONEY]` The counter requires connectivity. Batch allocation, effective price and stock availability are server decisions (BR-SCN-20, BR-BAT-31), and there is no local authority to fall back on. |
| BR-ONL-02 | An offline till states plainly that it is offline, shows what to do, and retries in the background. A partly entered basket is preserved in the browser so the operator does not re-scan it — **preserving input is not completing a sale**, and only the former happens offline. |
| BR-ONL-03 | `[MONEY]` A sale MUST NOT be completed, queued or recorded locally while offline. There is no lease, so there is no stock the till may claim. |
| BR-ONL-04 | Till connectivity is monitored: an offline till emits an event and, past a threshold, alerts. A shop that cannot sell must be visible to someone who can act on it, not only to the operator standing at it (doc 06 §5). |
| BR-ONL-05 | The scan-to-confirm budget of 200 ms p95 (BR-SCN-35) is now load-bearing: an online-only till turns latency into a queue of customers (doc 05 §7). |

---

#### Withdrawn — the lease design, retained for reference

<details>
<summary>Offline selling via stock leases (ADR 0003, superseded by ADR 0006)</summary>

The till does not sell from a guess. It sells from a **lease**: specific batches, in specific quantities, at specific prices, reserved to that till in advance by the server. Leased units are already `reserved` in the shared pool, so nobody else can sell them — which is what turns offline selling from an overselling risk into a bounded availability limit.

> **The lease is the sole source of offline sellable stock, in every mode.** Any change that lets a till sell outside its lease reintroduces every failure ADR 0002 documented.

#### Connectivity modes — configured per shop

| ID | Rule |
|---|---|
| BR-OFF-01 | A shop configures its connectivity mode. All three use the same lease mechanism; they differ only in lease size, expiry and how sync is triggered. |
| BR-OFF-02 | `[MONEY]` The mode changes **availability and limits, never correctness**. No mode permits selling outside the lease, charging an unleased price, or guessing a batch. |
| BR-OFF-03 | `[SEC][MONEY]` **`online_only` is the default.** Offline selling is opt-in: it is never enabled by a default, an upgrade, or a support action. Enabling it requires the `owner` role, records who enabled it and why, and is audited (BR-ADM-06). It hands a device real stock authority and accepts a bounded price-staleness and recall window, so it is a decision an owner takes deliberately rather than one they inherit. |
| BR-OFF-04 | Turning an offline mode **off** revokes outstanding leases and requires every till to sync before the change completes, so unsynced sales cannot be stranded by a configuration change (BR-OFF-33). |

| Mode | Behaviour | For |
|---|---|---|
| `online_only` *(default)* | The till refuses to complete a sale while offline. No lease is granted. | Every shop, unless the owner deliberately chooses otherwise |
| `offline_capable` | Sells from its lease when offline, syncs **opportunistically the moment** connectivity returns. | Shops that are online normally but need resilience to drops |
| `offline_first` | Deliberately runs disconnected and syncs on a **schedule** (configurable interval, default hourly). | Poor or metered connectivity, or a deliberate low-bandwidth operation |

#### The lease

| ID | Rule |
|---|---|
| BR-OFF-10 | `[MONEY]` A lease grants `(batch, quantity, effective price, price_valid_until)` entries to a named till, and **reserves those quantities in the shared pool** through the normal atomic path (BR-BAT-11). Leased stock is invisible to the storefront and to other tills. |
| BR-OFF-11 | `[MONEY]` A lease is granted per batch, so an offline receipt records the real batch number and expiry. Traceability, recall and allergen provenance survive the outage (BR-BAT-16, BR-BAT-25, BR-ATR-24). |
| BR-OFF-12 | `[MONEY]` The price charged offline is the leased price. It is stale by at most the lease expiry, which is a **known, bounded** window rather than an open-ended one. A markdown applied during an outage reaches the till at the next lease refresh. |
| BR-OFF-13 | Leases are refreshed continuously while online, so a till that loses connectivity is already carrying a current lease rather than scrambling for one. |
| BR-OFF-14 | `[MONEY]` A lease expires (default 12 hours). Unsold leased quantity is released back to the pool by the sweeper when the lease expires or the till syncs — the same release path as an abandoned checkout reservation (BR-INV-06). |
| BR-OFF-15 | `[SEC]` A lease is **revocable server-side**. A lost or stolen till holds real stock authority, so revocation is part of the design: revoking a lease releases its stock and invalidates the till's credentials. |
| BR-OFF-16 | Leases are sized per shop by value and by units, capping the money and stock at risk in any one device. |
| BR-OFF-17 | Every grant, refresh, expiry, revocation and reclaim is audited and emits an event (`lease.granted`, `lease.refreshed`, `lease.expired`, `lease.revoked`, `lease.reclaimed`). |

#### Selling offline

| ID | Rule |
|---|---|
| BR-OFF-20 | `[MONEY]` A till sells offline **only within its lease**. Beyond it, the sale is refused with the reason stated — a refusal, never an oversell. |
| BR-OFF-21 | `[MONEY]` Offline payment is **cash**, or an externally-settled UPI or card payment whose reference the operator records (§8A.6). A till MUST NOT claim a payment succeeded without an approval it cannot have obtained. |
| BR-OFF-22 | `[LEGAL]` Each till has its **own invoice series** with a pre-allocated block, replenished with the lease. GST requires consecutive numbering within a series, not one global sequence, so per-till series stay compliant without a round trip (BR-ORD-10). |
| BR-OFF-23 | Each offline sale carries a till-generated UUIDv7 identifier, recorded with `sold_offline_at`. That identifier is the idempotency key for sync (BR-OFF-30). |
| BR-OFF-24 | `[SEC]` Local till storage is encrypted at rest and holds no card data and no customer PII beyond what the receipt requires (BR-DAT-06, BR-PAY-11). |
| BR-OFF-25 | The till shows its offline state, its remaining lease per line, and the time since last sync. An operator must never be surprised by a refusal. |
| BR-OFF-26 | `[MONEY]` A batch recalled during an outage may still be sold — the till cannot know. Such sales are **flagged on sync for customer contact**. This exposure is the main reason lease expiry is short (BR-BAT-25). |

#### Sync

| ID | Rule |
|---|---|
| BR-OFF-30 | `[MONEY]` Sync uploads offline sales keyed by their till-generated identifier, applied `ON CONFLICT DO NOTHING`. Re-uploading is a no-op, so an interrupted sync is simply retried — the same idempotency discipline as the webhook ledger (BR-PAY-07). |
| BR-OFF-31 | `[MONEY]` Sync converts leased reservations to decrements and releases the unsold remainder, in one transaction per sale. It is arithmetic against the lease, not a judgement call. |
| BR-OFF-32 | `offline_capable` syncs the moment connectivity returns. `offline_first` syncs on its configured interval, and additionally whenever the lease is nearly exhausted or nearly expired. |
| BR-OFF-33 | A sale that cannot be applied — expired lease, revoked lease, recalled batch — is **never silently dropped and never silently accepted**. It lands on a reconciliation queue with the reason, and is resolved by a human. |
| BR-OFF-34 | `[MONEY]` A till MUST resync within a configured deadline (default 24 hours). Past it, offline selling stops until the till syncs: an unbounded backlog of unsynced sales is unbounded unrecorded revenue. |
| BR-OFF-35 | Sync is resumable and incremental. A large backlog uploads in bounded chunks, so a poor connection makes progress rather than restarting (DB-027). |
| BR-OFF-36 | `[SEC]` A till authenticates as a registered device with its own credentials, and its uploads are authorised against the shop and lease it holds. A till is a staff actor, not an anonymous client (SEC-09). |

#### Monitoring

| ID | Rule |
|---|---|
| BR-OFF-40 | Till connectivity, lease utilisation, time since last sync and unsynced sale count and value are exported as metrics, with alerts on a till past its sync deadline or exhausting its lease (doc 06 §5). |
| BR-OFF-41 | Unsynced offline revenue is reported daily to finance with its value, so the amount of money recorded only on a device is a number rather than an impression (BR-RCV-24). |
| BR-OFF-42 | Offline-sale events (`sale.completed_offline`, `sale.synced`, `sale.sync_rejected`, `till.went_offline`, `till.resynced`) are emitted per doc 06 §3, so how much selling actually happens offline is a measurement rather than an assumption. |
| BR-OFF-43 | The scan-to-confirm budget of 200 ms p95 (BR-SCN-35) still applies online, because latency at a till is a queue of customers (doc 05 §7). |

</details>

*End of withdrawn section. Live rules resume below.*

### 8A.6 Counter payment methods

> **The platform does not process counter payments. It records them.**
>
> Money at the counter moves through the shop's own channels — the cash drawer, the shop's UPI QR, the shop's card terminal — none of which the platform touches. Razorpay processes **storefront** payments only. At the counter, every payment is a *record of something that happened elsewhere*.

This is a deliberate and reasonable division: the shop already has a QR and a card machine, and Steleios is its till and inventory system, not its payment processor. But it has one large consequence that shapes every rule below:

**No counter payment is verified at the moment of sale.** Reconciliation is therefore not a safety net for an unusual case — it is the primary financial control for the entire counter channel.

| Method | What the platform records | Counter | Storefront |
|---|---|---|---|
| **Cash** | Amount tendered and change given; reconciled against the drawer count at shift close | ✅ | ❌ |
| **UPI** — the shop's own QR | Transaction reference (UTR/RRN) read off the customer's confirmation, and the amount | ✅ | ✅ |
| **Card** — the shop's own terminal | Terminal id, approval/RRN reference, card network, last four digits | ✅ | ❌ |

**The counter takes all three; the storefront is UPI-only.** A counter customer is standing in front of an operator with a drawer and a terminal. A storefront order is fulfilled by a delivery person who handles goods and never money, so cash and card have nowhere to happen (§9A, BR-STO-22a).

In every case the platform records **what was paid, how, and its reference** — and never processes it (ADR 0008).

| ID | Rule |
|---|---|
| BR-CPM-01 | `[MONEY]` Every payment records its **confirmation source**: `gateway` (storefront, provider-verified) or `recorded` (counter, operator-attested). It is stored on the payment, shown in admin, printed on the receipt as the tender type, and carried into every financial report. A recorded payment is never displayed or counted as a verified one. |
| BR-CPM-02 | `[MONEY]` A counter payment puts the order into **`paid_unverified`**, not `paid`. Fulfilment proceeds immediately — the customer is standing there with their goods — but the money is not treated as settled until reconciliation matches it (BR-CPM-20). Revenue reports separate verified from unverified takings; totalling them as one number is prohibited. |
| BR-CPM-02a | `[MONEY]` Razorpay is **not used at the counter**. A counter sale MUST NOT create a provider order, and the counter UI MUST NOT offer a gateway path — offering one that cannot be completed is worse than not offering it. |
| BR-CPM-03 | `[SEC][LEGAL]` **No card data ever enters the platform.** A card attestation records the terminal id, the approval or RRN reference, the card network, and the last four digits only. Full card number, expiry and CVV MUST NOT be captured, stored, logged or displayed — not in a note field, not in a photo, not anywhere (BR-PAY-11). |
| BR-CPM-04 | `[MONEY]` Required fields per method are mandatory and format-validated before the sale completes: **UPI** — transaction reference (UTR/RRN) and the amount; **card** — terminal id, approval/RRN reference, network, last four. A sale MUST NOT complete with a blank reference and a promise to fill it in later. |
| BR-CPM-05 | `[MONEY]` The recorded amount must equal the order total exactly. Split tender — part cash, part UPI — is supported as **multiple payment records that sum to the total**, never as one adjusted amount. |
| BR-CPM-06 | `[SEC][MONEY]` A transaction reference is **unique across all payments**. Re-using a UTR or approval code is either an operator mistake or an attempt to mark two sales paid with one transfer, and is refused with an explicit message. |
| BR-CPM-07 | `[SEC]` The operator confirms, as an explicit action, that they saw the payment confirmation on the customer's device or the terminal slip. That confirmation is recorded with the operator's identity and is what makes the attestation attributable (BR-ADM-06). |
| BR-CPM-08 | `[MONEY]` A recorded non-cash payment is capped per sale by a configured value, because nothing verified it. Above the cap the sale requires manager approval, recorded with the approver's identity. |
| BR-CPM-09 | UPI uses the shop's **static** QR. The platform does not generate a dynamic amount-bearing QR, because it is not in the payment path and a QR it generated would imply a verification it cannot perform. |
| BR-CPM-10 | `[MONEY]` The till shows the amount due prominently and the operator enters the reference **after** the customer's confirmation appears. The sequence matters: entering a reference before payment completes is how a sale gets recorded for money that never arrived. |

#### Cash handling

| ID | Rule |
|---|---|
| BR-CPM-15 | `[MONEY]` A cash payment records the amount tendered and the change given, so the drawer can be reconciled against recorded sales rather than against a total alone. |
| BR-CPM-16 | `[MONEY]` A shift close counts the drawer and records the variance against expected cash. Variance is reported per till and per operator — a persistent variance is a finding, not noise. |
| BR-CPM-17 | `[SEC]` Cash refunds at the counter follow the refund rules and are not available to `counter_sales` (docs/02 §15); a refund goes to a manager. |

#### Reconciliation — the counter's primary financial control

Because the platform never sees counter money move, reconciliation is not an audit afterthought. It is the **only** mechanism that turns a counter sale's recorded payment into a known fact, and it is where every error and every fraud in the channel surfaces.

| ID | Rule |
|---|---|
| BR-CPM-20 | `[MONEY]` Recorded payments are reconciled against the **bank/UPI settlement statement** and the **card terminal's batch settlement**, matched on reference and amount. A match moves the order from `paid_unverified` to `paid`. Cash is reconciled against the drawer count at shift close (BR-CPM-16). |
| BR-CPM-21 | `[MONEY]` An unmatched record past a configured window (default 3 working days) is an **exception**, raised to finance with its value and its recording operator. This is the control that catches a mistyped reference, a payment that never arrived, and an operator pocketing cash while recording a UPI reference that does not exist. |
| BR-CPM-22 | `[MONEY]` A settlement entry with no matching record is equally an exception: money arrived that no sale accounts for. |
| BR-CPM-23 | `[MONEY]` Unreconciled counter revenue is reported daily to finance with its value and age, alongside unsynced offline revenue (BR-OFF-41). Both answer the same question: **how much of today's takings exists only as somebody's word.** |
| BR-CPM-24 | Reconciliation is idempotent and re-runnable — a restated settlement file must not double-match (BR-PAY-07 discipline). |
| BR-CPM-26 | `[MONEY]` Reconciliation is a **launch requirement for the counter channel, not a later phase.** Shipping counter sales without it would mean the business has no way to know whether the money it recorded actually arrived. |
| BR-CPM-27 | Exception rate and unmatched value are tracked per operator and per till over time. One mistyped reference is an accident; a pattern is a finding. |
| BR-CPM-25 | Counter payment events (`counter.payment_recorded`, `counter.payment_attested`, `counter.payment_reconciled`, `counter.payment_unmatched`, `counter.drawer_variance`) are emitted per doc 06 §3. |

---

## 9. Documents and accounting records

> **Steleios records payments; it never processes them** ([ADR 0008](decisions/0008-no-payment-processing.md)). Every `BR-PAY-*` and `BR-COD-*` rule in the withdrawn section at the end of this chapter is **withdrawn and MUST NOT be implemented**.

This is now the accounting core of the product: a complete, GST-compliant document trail for **everything that moves goods or money**, in both directions.

| Direction | Event | Document |
|---|---|---|
| Outward | Sale | **Tax invoice** |
| Outward | Sale of exempt or composition goods | **Bill of supply** |
| Outward | Customer return, or a reduction in value | **Credit note** |
| Outward | An increase in value after invoicing | **Debit note** |
| Inward | Goods received from a supplier | **Purchase invoice** recorded against the receipt |
| Inward | Return to supplier | **Debit note**, matched to the supplier's credit note |
| Either | Goods moving without a sale | **Delivery challan** |
| Outward | Money received before the supply | **Receipt voucher** |
| Outward | An advance refunded without a supply | **Refund voucher** |
| Inward | A supply attracting reverse charge | **Payment voucher** |

### 9.1 Numbering and immutability

| ID | Rule |
|---|---|
| BR-DOC-01 | `[LEGAL]` Every document type has its own **series**, and each series is consecutive with no gaps within a financial year. Numbers are allocated by the database, serialized, so two concurrent sales cannot take the same number. |
| BR-DOC-02 | `[LEGAL]` A number is allocated **only when the document is issued**, never reserved in advance and never released. A cancelled invoice keeps its number and is marked cancelled; deleting it would leave a gap, and a gap in an invoice series is what an auditor asks about first. |
| BR-DOC-03 | `[LEGAL]` Issued documents are **immutable**. A correction is a credit or debit note referencing the original, never an edit. This is enforced the same way the audit log is: no `UPDATE`, no `DELETE` (BR-ADM-05). |
| BR-DOC-04 | `[LEGAL]` Every document snapshots everything it asserts — party details, addresses, GSTIN, line items, quantities with UoM and UQC, HSN codes, rates, tax split, totals in words — so it reprints identically years later regardless of what has changed since (BR-ORD-03, BR-VER-03). |
| BR-DOC-05 | Documents are rendered to PDF on issue and stored. The stored file is the record; regenerating it must produce the same document, and a template change MUST NOT alter an already-issued one. |
| BR-DOC-06 | `[LEGAL]` Series are per shop (tenant). Multiple series are permitted provided each is itself consecutive, which is what lets each shop number independently. |

### 9.2 Sales invoice

| ID | Rule |
|---|---|
| BR-DOC-10 | `[LEGAL]` A tax invoice carries: supplier name, address and GSTIN; customer name and address, with GSTIN for a B2B sale; invoice number and date; place of supply; per line the description, HSN, quantity with UQC, unit price, taxable value, and the GST split; the total, and the total in words. |
| BR-DOC-11 | `[LEGAL]` The GST split follows place of supply — CGST + SGST intra-state, IGST inter-state — using the rate in force on the taxable event date (BR-PRC-04, BR-TAX-02). |
| BR-DOC-12 | `[LEGAL]` A **bill of supply** is issued instead of a tax invoice for exempt goods or under composition. The two are different documents with different series and MUST NOT be conflated. |
| BR-DOC-13 | `[LEGAL]` Invoice timing differs by channel, because the moment the supply is settled differs: **at the counter** the invoice is issued at the sale, since goods and customer are together; **on delivery** the goods travel on a delivery challan and the invoice is issued at the door once the customer has accepted what they are keeping (§9A.3). |
| BR-DOC-14 | `[LEGAL]` A counter sale's invoice is available as a printed receipt and, where the customer gives contact details, by email or link. |

### 9.3 Purchase invoices — the inward side

Recording what the shop **buys** is half of accounting and is what makes input tax credit claimable. It is not a lesser feature than the sales side.

| ID | Rule |
|---|---|
| BR-DOC-20 | `[LEGAL]` Every goods receipt (§2A) records the supplier's invoice: supplier GSTIN, their invoice number and date, line items with HSN and quantity, taxable value, tax split, and total. |
| BR-DOC-21 | `[MONEY]` The purchase invoice is **matched against what was actually received**. A quantity or price difference is an exception raised to purchasing, not silently accepted — this is what catches short deliveries and wrong prices. |
| BR-DOC-22 | `[LEGAL]` `(supplier_id, supplier_invoice_number, financial_year)` is unique. The same supplier invoice cannot be recorded twice, which would claim the input tax credit twice. |
| BR-DOC-23 | `[MONEY]` The invoice links to the **batches** it created (BR-BAT-07), so batch cost traces to the document that set it and margin is auditable to source. |
| BR-DOC-24 | `[LEGAL]` Input tax credit eligibility is recorded per invoice, including ineligible and blocked credits, because claiming credit that is not available is the most common GST error and the most expensive to unwind. |
| BR-DOC-25 | `[LEGAL]` Purchase invoices are reconciled against **GSTR-2B** — the credit auto-drafted from suppliers' own filings. An invoice the shop holds but the supplier never filed is an exception: the credit is not available until the supplier files, and the shop needs to know before it claims. |

### 9.4 Returns, both directions

| ID | Rule |
|---|---|
| BR-DOC-30 | `[LEGAL]` A **customer return** issues a credit note referencing the original invoice number and date, with the returned lines, their taxable value and their tax reversal (BR-RET-08). |
| BR-DOC-31 | `[MONEY]` Credit note value is computed from the original invoice's snapshot — including the proportional share of any discount — never from current prices (BR-RET-03). |
| BR-DOC-32 | `[LEGAL]` A **return to supplier** (§12A) issues a debit note referencing the purchase invoice, and is matched to the supplier's own credit note when it arrives. Unmatched debit notes age on a report; an unrecovered RTV is a real loss (BR-RTV-04). |
| BR-DOC-33 | `[LEGAL]` A return reverses input tax credit or output tax in the period the note is issued, and the reversal appears in the returns for that period. |
| BR-DOC-34 | `[MONEY]` Cumulative credit notes against an invoice can never exceed it (BR-RET-04). |

### 9.5 Payment records

Payment recording is specified in §8A.6 and applies to **every channel**, not only the counter.

| ID | Rule |
|---|---|
| BR-DOC-40 | `[MONEY]` A payment record is linked to the documents it settles. One payment may settle several invoices, and one invoice may be settled by several payments; the allocation is explicit and MUST sum correctly. |
| BR-DOC-41 | `[MONEY]` Outstanding balance per customer and per supplier is derived from documents minus allocated payments. It is never a stored figure someone can adjust. |
| BR-DOC-42 | Ageing reports — receivables and payables by bucket — are produced from the same data. A shop's most common question is who owes it money and who it owes. |
| BR-DOC-43 | `[MONEY]` Advances received before an invoice are recorded as such and adjusted against the invoice when it is issued. |

#### Receipts and vouchers

A **receipt** acknowledges money; an **invoice** records a supply. They are usually printed together at the counter, and they are still two different documents with different rules.

| ID | Rule |
|---|---|
| BR-DOC-44 | `[LEGAL]` Money received **before** the supply requires a **receipt voucher** with its own series: supplier and customer details, amount, the rate and tax on the advance, and the place of supply. Recording it as an invoice would report a supply that has not happened. |
| BR-DOC-45 | `[LEGAL]` When the supply follows, the invoice **adjusts the advance** and references the receipt voucher, so the tax is not accounted for twice. |
| BR-DOC-46 | `[LEGAL]` An advance refunded without a supply requires a **refund voucher** referencing the receipt voucher. The advance's tax is reversed in the period the voucher is issued. |
| BR-DOC-47 | `[LEGAL]` A **payment voucher** is issued for a supply attracting reverse charge, where the shop rather than the supplier accounts for the tax. |
| BR-DOC-48 | `[MONEY]` A counter sale's printed receipt **is** the tax invoice — goods and money change hands together, so there is no advance and no separate voucher (BR-DOC-14). |
| BR-DOC-49 | `[LEGAL]` Every voucher type follows §9.1: its own gapless series per shop, numbered only on issue, immutable once issued, and reprintable identically years later. |

### 9.6 GST returns

| ID | Rule |
|---|---|
| BR-DOC-50 | `[LEGAL]` The system produces the data for **GSTR-1** (outward supplies) and the summary figures for **GSTR-3B**, from documents rather than from re-keyed totals. |
| BR-DOC-51 | `[LEGAL]` Return data is produced **per period and frozen once filed**. A document issued after filing belongs to the next period, and an amendment to a filed period is an amendment, not a re-computation. |
| BR-DOC-52 | `[LEGAL]` Filing itself is **out of scope**. Steleios produces the figures and the export; the shop or its accountant files them. This keeps the system out of a regulated filing role, in the same spirit as staying out of payments. |
| BR-DOC-53 | `[MONEY]` Any figure on a return traces to the documents that produced it, in one click. An accountant who cannot see what a number is made of will not trust it, and will re-key it into a spreadsheet. |

### 9.7 Accounting export

| ID | Rule |
|---|---|
| BR-DOC-60 | Documents export in a form an accountant can use — Tally-compatible XML and CSV at minimum, with an option to export a period in full. |
| BR-DOC-61 | `[LEGAL]` Export works in every subscription state, including dormant (BR-LIC-31). A shop's books are its own. |
| BR-DOC-62 | Exports are idempotent and re-runnable, and record what was exported and when, so a period is not double-posted into the accounting system. |
| BR-DOC-63 | `[SEC]` Exports containing customer or supplier details require an appropriate role and are audited with the row count and the period (BR-RPT-05). |

---

### Withdrawn — payment gateway integration

<details>
<summary>Razorpay processing, webhooks and COD as a payment path (superseded by ADR 0008)</summary>

**Withdrawn.** Steleios does not process payments. These rules are retained rather than deleted because they are a correct design for gateway integration should online payment ever be reintroduced (ADR 0008, "Revisiting"), and because the webhook idempotency discipline they describe still governs any provider callback — courier tracking, for instance.

### Features

- Razorpay Standard Checkout: UPI, cards, netbanking, wallets.
- Cash on delivery with a risk gate.
- Webhook-driven payment confirmation.
- Full and partial refunds.
- Settlement reconciliation import.

### Business rules

| ID | Rule |
|---|---|
| BR-PAY-01 | `[MONEY]` A Razorpay order is created server-side. The amount is sent in paise from the stored order total. The client never supplies or influences the amount. |
| BR-PAY-02 | `[SEC][MONEY]` **The browser callback is not proof of payment.** The signature returned to the browser is verified only to unlock the confirmation page. Order state advances to `paid` exclusively on a verified webhook. |
| BR-PAY-03 | `[SEC]` The browser callback signature is `HMAC_SHA256(razorpay_order_id + "\|" + razorpay_payment_id, KEY_SECRET)`, compared with `hmac.Equal`. Non-constant-time comparison is prohibited. |
| BR-PAY-04 | `[SEC]` The webhook signature is `HMAC_SHA256(raw request body, WEBHOOK_SECRET)` from the `X-Razorpay-Signature` header. `WEBHOOK_SECRET` is a distinct secret from `KEY_SECRET`; the two must not be interchangeable in configuration. |
| BR-PAY-05 | `[SEC]` The raw request body must be read and verified **before** any JSON decoding or middleware transformation. A webhook failing verification returns 400 and is logged as a security event. |
| BR-PAY-06 | `[SEC]` The webhook route carries no session authentication and is exempt from CSRF. This exemption is declared explicitly in the router with a comment stating why. |
| BR-PAY-07 | `[MONEY]` Webhook processing is idempotent, keyed on Razorpay's event ID in a `webhook_events` ledger via `INSERT ... ON CONFLICT DO NOTHING`. If no row is inserted, the event has already been processed: return 200 and stop. |
| BR-PAY-08 | Webhook handlers return 200 within 5 seconds. All downstream work (invoice, email, SMS) is enqueued on asynq, never performed inline. |
| BR-PAY-09 | `[MONEY]` On `payment.captured` the handler verifies that the captured amount equals the stored order total. A mismatch does not mark the order paid; it raises a critical alert for manual review. |
| BR-PAY-10 | Handled events: `payment.captured`, `payment.failed`, `order.paid`, `refund.created`, `refund.processed`. Unrecognised events are acknowledged with 200 and recorded, never rejected. |
| BR-PAY-11 | `[SEC][LEGAL]` Card data is never received, stored or logged by Steleios. Saved cards use Razorpay tokenization exclusively. |
| BR-PAY-12 | Payment attempts are recorded individually. An order may have several failed attempts and at most one captured payment. |
| BR-PAY-13 | `[MONEY]` Refunds are separate records against the payment. Total refunded can never exceed the captured amount. Refunds are never in-place mutations of the payment row. |
| BR-PAY-14 | Gateway fee and gateway tax are stored per payment when the settlement report is imported, so margin reporting is possible. |
| BR-PAY-15 | `[SEC]` Test and live Razorpay keys are selected by deployment environment only. No request field, header or query parameter may influence key selection. |
| BR-PAY-16 | `[SEC]` Razorpay webhook payloads are stored, but PII within them is redacted from application logs. |

### COD

| ID | Rule |
|---|---|
| BR-COD-01 | COD is offered only when the destination pincode is COD-serviceable **and** the order total is within `[cod_min, cod_max]`. |
| BR-COD-02 | `[SEC]` A COD order requires a verified phone number via OTP before it is confirmed. |
| BR-COD-03 | A COD order transitions `pending_payment → confirmed` on OTP verification, bypassing `paid`. Stock decrements at `confirmed`. |
| BR-COD-04 | COD is unavailable to customers with 2 or more undelivered-refused COD orders in the previous 180 days. |
| BR-COD-05 | A COD order records payment as collected at delivery, moving to `paid` at that point for accounting purposes. |
| BR-COD-06 | Discounts flagged `online_payment_only` are unavailable when COD is selected; selecting COD removes them and re-prices, with the change shown to the customer. |

</details>

---

## 9A. Storefront orders — payment on delivery or by UPI QR

The storefront takes **orders**, not payments (ADR 0008). A customer chooses when they will pay, and the shop records the payment when it arrives.

**Both routes are UPI. No cash is collected on a storefront order.**

| Route | When the customer pays | Where the money goes |
|---|---|---|
| **On delivery** | At handover, by scanning the shop's QR | Straight into the shop's account |
| **Before delivery** | On or after ordering, using the shop's QR or UPI number | Straight into the shop's account |

### What UPI-only removes, and what it does not

**Removed entirely — cash.** No drawer, no shift counts, no variance, no delivery person carrying money. Cash skimming is the classic retail fraud and it is simply gone, because **the money never passes through anyone's hands**: it goes from the customer's app to the shop's account directly. A delivery person handles goods, never takings.

**Also removed — partial reconciliation.** Every payment now leaves a trace in the UPI statement, so **100% of takings are matchable** rather than only the non-cash portion. Reconciliation stops being a sampling exercise and becomes complete, which is what makes `BR-CPM-21` a real control instead of a best effort.

**Not removed — payee substitution.** UPI is a payee-addressed system, so a delivery person can still present *their own* QR instead of the shop's. That risk is unchanged by the choice of UPI.

But it changes character in the shop's favour: with cash, skimming was **undetectable**; with UPI-only, every delivered order must have a matching credit in one known account, so a substituted payment shows up as a delivered order with no payment at the next reconciliation run. **It goes from invisible to caught within a day**, which is why the controls in §9A.1 are worth keeping rather than relaxing.

### Business rules

| ID | Rule |
|---|---|
| BR-STO-01 | `[MONEY]` Placing an order **never** asserts payment. An order enters `awaiting_payment` (UPI) or `confirmed_cod` (payment on delivery), and reaches `paid_unverified` only when a payment is recorded (BR-CPM-02). |
| BR-STO-02 | `[MONEY]` Stock is reserved at order placement exactly as at checkout (BR-CHK-01, BR-BAT-11). The reservation is what stops the shop selling the same unit twice while a customer arranges payment. |
| BR-STO-03 | `[MONEY]` A UPI order's reservation window is longer than a card checkout's — the customer may pay minutes later — but it is bounded and stated to the customer. On expiry the order is cancelled and the stock released (BR-CHK-09). |
| BR-STO-04 | `[LEGAL]` The customer is shown, before confirming: the amount, how to pay, and what happens if they do not. No ambiguity about whether an order is paid. |
| BR-STO-05 | Payment details reach the customer by one of two routes, both of which the shop controls: **the delivery person presents the shop's QR at handover**, or **the shop sends its UPI number** with the order confirmation. The storefront may also display the shop's static QR. Steleios never generates a dynamic payment QR, because generating one would imply a verification it cannot perform (BR-CPM-09). |
| BR-STO-06 | `[MONEY]` The customer submits their **transaction reference** after paying, and a member of staff confirms receipt against the shop's own bank or UPI app before the order advances. A customer-entered reference alone is a claim, never a confirmation. |
| BR-STO-07 | `[SEC]` A customer-supplied reference is untrusted input: format-validated, checked for reuse across all payments (BR-CPM-06), and never trusted to release goods on its own. |
| BR-STO-08 | `[MONEY]` **Payment-on-delivery orders carry delivery risk.** A refused delivery costs the shop the trip and returns the stock. Risk controls apply as they did for COD: pincode serviceability, an order-value cap, phone verification, and a refusal history check (BR-COD-01, BR-COD-02, BR-COD-04). |
| BR-STO-09 | `[MONEY]` Payment taken at delivery is recorded against the order with its tender type and reference, and reconciles like any other payment (BR-CPM-20). Cash collected by a delivery person is reconciled against their round, not only against the order. |
| BR-STO-10 | `[LEGAL]` The invoice is issued when the goods go out, not when payment clears, because the supply has happened (BR-DOC-13). An unpaid invoice is a receivable, which is what the ageing report is for (BR-DOC-42). |
| BR-STO-11 | Order status is honest to the customer throughout: placed, awaiting payment, payment received, packed, dispatched, delivered. "Awaiting payment" is never dressed up as "processing". |
| BR-STO-12 | `[MONEY]` An order abandoned without payment releases its stock and is recorded as abandoned. Abandonment rate by payment method is reported — if UPI orders abandon far more than delivery ones, that is a finding worth acting on. |

### 9A.1 Where the payment details come from

Money is collected outside the system, so **the one thing the system must get right is that the customer pays the shop and not somebody else.** Both routes are attack surfaces, and the controls differ.

| ID | Rule |
|---|---|
| BR-STO-20 | `[SEC][MONEY]` The shop's UPI identifier is **configuration held on the shop record**, verified once at onboarding. It is never entered per order, never typed by a delivery person, and never taken from a form. A per-order payee field would be a way to redirect a shop's takings one order at a time. |
| BR-STO-21 | `[SEC][MONEY]` **A delivery person presents the shop's QR, never their own.** With cash gone, payee substitution is the only remaining way to divert money, so it is where any fraud will concentrate. The control is that the QR is rendered by the app from the shop record — it is not an image on the person's phone, and there is no field for them to type a payee into. |
| BR-STO-22 | `[MONEY]` A round is reconciled as **every delivered order having a matching credit** in the shop's account. The delivery person never holds money, so there is nothing to count in or out — but a delivered order with no corresponding payment is an exception naming that person, and it surfaces at the next reconciliation run rather than at a month-end stock-take (BR-CPM-21). |
| BR-STO-22a | `[MONEY]` No cash is accepted on a storefront order. A customer who cannot pay by UPI is told before the order is confirmed, not at the door. |
| BR-STO-23 | `[SEC]` Changing the shop's UPI identifier requires the `owner` role, re-authentication, and an audit entry, and notifies the owner out-of-band. It is the single highest-value configuration field in the system: whoever controls it controls where the money goes. |
| BR-STO-24 | `[SEC][LEGAL]` Payment details sent to a customer go out on the registered channel and template (BR-CMP-12), drawn from the shop record. Steleios MUST NOT send a payment identifier supplied in a request. |
| BR-STO-25 | `[SEC]` The shop's UPI identifier is shown to the customer **consistently across every touchpoint** — order confirmation, message, delivery. A payment identifier that varies between messages is the signature of a phishing attempt, and consistency is what lets a customer notice one. |
| BR-STO-26 | `[SEC]` The customer is told, in the confirmation, to pay only the identifier shown by the shop and never one sent by anyone else. Customers are the ones targeted by this fraud, and a plain warning costs nothing. |
| BR-STO-27 | `[MONEY]` A reference the customer submits is matched to a payment actually seen in the shop's account before goods are released (BR-STO-06). Where goods are already handed over at delivery, the assertion is confirmed by a second person (§9A.2). |

### 9A.2 Delivered and paid — a two-step confirmation

The delivery person asserts. **A different person confirms.**

> **Whoever takes a payment can never be the person who confirms it arrived.** With cash gone, presenting one's own QR instead of the shop's is the only remaining way to divert money (BR-STO-21), and a single-actor confirmation would make that undetectable again. Separating the two is what keeps it caught within a day.

| Step | Who | What happens |
|---|---|---|
| 1 | **Delivery person** (`delivery` role) | Marks the order delivered and asserts the customer paid, with the reference and time. Order becomes `delivered_pending_verification`; payment is `paid_unverified`. |
| 2 | **Customer care or manager** (`payment:verify`) | Checks the credit in the shop's UPI account and confirms. Order becomes `delivered`; payment becomes `paid`. |

| ID | Rule |
|---|---|
| BR-STO-30 | `[MONEY]` A delivery person's assertion **never** marks a payment verified. It records a claim, with the actor, timestamp, reference and location where captured. |
| BR-STO-31 | `[SEC][MONEY]` **The confirming actor MUST be a different person from the asserting one.** This is enforced in the service, not by role alone — a manager who personally delivered an order cannot confirm that order's payment (BR-ADM-04 pattern). |
| BR-STO-32 | `[SEC]` Confirmation requires `payment:verify`, held by customer care, manager, finance, owner and admin — and by no role that takes payments. `counter_sales` and `delivery` MUST NOT hold it. |
| BR-STO-33 | `[MONEY]` **Automated matching is the normal path.** Where a UPI or bank statement is imported, credits match recorded payments automatically and only the unmatched surface for human confirmation. Manual confirmation is the exception queue, not the daily grind — a control that requires hundreds of clicks a day is a control staff will route around. |
| BR-STO-34 | `[MONEY]` Confirmation records **what was matched against**: the statement line or the account screen, the amount, and the actor. "I checked" is not an audit trail. |
| BR-STO-35 | `[MONEY]` Bulk confirmation against an imported statement is permitted for efficiency, but each order's confirmation is recorded individually, with its own matched credit. A single click confirming fifty orders must still produce fifty auditable records. |
| BR-STO-36 | `[MONEY]` An order left in `delivered_pending_verification` past a configured window (default 24 hours) becomes an **exception** naming the delivery person, with its value. This is the control that catches payee substitution — goods gone, no credit received. |
| BR-STO-37 | `[MONEY]` Unverified-delivery exceptions are tracked per delivery person over time. One is a customer who paid late or a mistyped reference; a pattern is a finding (BR-CPM-27). |
| BR-STO-38 | Goods are already with the customer at step 1, so a failed confirmation is a **collection problem, not a fulfilment one**. The order is not reversed; it becomes a receivable and appears on the ageing report (BR-DOC-42). |
| BR-STO-39 | `[SEC]` A delivery person sees only their own assigned deliveries, and only for as long as they are assigned. The `delivery` role grants delivery updates and nothing else — no order browsing, no customer records, no reports. |
| BR-STO-40 | Both steps emit events (`delivery.completed`, `delivery.payment_asserted`, `payment.verified`, `payment.verification_overdue`) per doc 06 §3, and both are audited (BR-ADM-06). |

### 9A.3 Goods travel on a challan; the invoice is raised at the door

**The invoice is generated at delivery, once the customer has accepted what they are keeping and immediately before they pay.** Until then it is held ready but unissued.

This is better than invoicing at dispatch and correcting afterwards, and the reason is simple: **the damaged lines never reach the invoice at all.** No credit note, no reversal, no adjustment to explain. The customer's invoice states exactly what they received and exactly what they paid, which is also what an auditor and an accountant want to see.

```
dispatch          delivery challan issued; goods in transit; stock allocated, not sold
     │
     ▼
at the door       customer inspects; damaged or refused lines removed (BR-DMG-*)
     │
     ▼
acceptance        TAX INVOICE issued — numbered, immutable, final lines only
     │
     ▼
payment           customer pays the invoice total by UPI
```

| ID | Rule |
|---|---|
| BR-DLV-01 | `[LEGAL]` Goods leave the shop on a **delivery challan**, not an invoice. The challan carries the consignment, the lines and quantities, and its own series (BR-DOC-01). |
| BR-DLV-02 | `[MONEY]` Stock is **allocated and in transit**, not sold, from dispatch until acceptance. It is unavailable to other orders and to the storefront, and it is not yet a sale (BR-BAT-15). |
| BR-DLV-03 | Until issue, the document is a **proforma**: it shows the expected total and is clearly marked *not a tax invoice*. It carries **no invoice number**, because numbers are allocated only on issue and a gap in an invoice series is what an auditor asks about first (BR-DOC-02). |
| BR-DLV-04 | `[LEGAL][MONEY]` The tax invoice is issued at **acceptance**, containing only the lines the customer is keeping, priced and taxed at the rates in force on that date (BR-TAX-02). It is numbered, immutable and final from that moment (BR-DOC-03). |
| BR-DLV-05 | `[MONEY]` Payment is taken **after** issue, against the invoice total. The invoice total is the reconciliation target (BR-CPM-20, BR-DMG-08). |
| BR-DLV-06 | `[MONEY]` Lines never accepted are **never invoiced**. They return on the challan and go to `quarantine` (BR-DMG-12). There is nothing to credit because nothing was billed. |
| BR-DLV-07 | `[MONEY]` A wholly refused delivery issues **no invoice**. The challan closes as a return, the stock comes back, and the order is recorded as refused with its reason. |
| BR-DLV-08 | `[LEGAL]` Where the consignment value requires an **e-way bill**, it is raised against the delivery challan and updated on the resulting invoice. |
| BR-DLV-09 | `[MONEY]` A challan not closed within a configured window is an exception naming the delivery person: goods left the shop and neither returned nor became a sale (BR-STO-36). |
| BR-DLV-10 | `[LEGAL]` Counter sales are unaffected: goods and customer are together, so the invoice issues at the sale with no challan (BR-DOC-13). |

> **`[LEGAL]` Requires confirmation before launch.** Under GST, a tax invoice for a supply of goods is generally due *before or at the time of removal*. Issuing at delivery instead relies on the consignment being treated as **sent on approval** (Section 31(7) with Rule 55), which permits removal on a delivery challan and an invoice when the supply is confirmed. That treatment is well suited to doorstep inspection, but **it MUST be confirmed with the tax advisor and recorded in `docs/decisions/` before this ships** — invoice timing is not a detail to be decided by whoever writes the code (BR-TAX-03 precedent).
>
> If the advisor requires invoicing at dispatch, the fallback is the credit-note flow: invoice at dispatch, credit note for refused lines. It is more paperwork and a worse customer document, but it is a configuration of the same machinery, not a rebuild.

#### Damage and refusal at the door

A customer should not pay for a damaged item, and should not pay in full and wait for a refund. The delivery person photographs the damage, a manager approves, and the lines drop off the invoice before it is issued.

| ID | Rule |
|---|---|
| BR-DMG-01 | The delivery person photographs the damage and raises an adjustment against specific challan lines and quantities, with a reason code (`damaged_in_transit`, `wrong_item`, `short_shipped`, `expired`, `spoiled`, `customer_refused`). |
| BR-DMG-02 | `[SEC][MONEY]` **A delivery person cannot approve their own adjustment.** Approval requires `order:write`, which the `delivery` role does not hold. Without this, "damaged" becomes a discount a delivery person can hand out — or collect the full amount for and keep the difference. |
| BR-DMG-03 | `[SEC]` Photographs are mandatory: at least one per claimed line. An adjustment with no evidence is refused. |
| BR-DMG-04 | `[SEC]` Uploads are content-sniffed rather than trusted by declared type, stripped of EXIF — a doorstep photo carries GPS — size-limited, and stored in object storage (BR-MED-06, BR-MED-07). |
| BR-DMG-05 | Uploads are compressed on the device and retried in the background. A delivery person standing in a lift with one bar must not be blocked by an upload; the request is raised and the photo follows. |
| BR-DMG-06 | `[MONEY]` An approved adjustment removes the lines from the **pending invoice**. Because nothing has been issued yet, there is no credit note and no reversal — the invoice is simply raised without them. |
| BR-DMG-07 | `[MONEY]` The customer is shown the adjusted total before the invoice is issued, and pays that amount. |
| BR-DMG-08 | `[MONEY]` The shop's QR is static and carries no amount (BR-CPM-09), so the issued invoice total is the reconciliation target. A credit matching neither it nor the pre-adjustment total is an exception. |
| BR-DMG-09 | `[MONEY]` **A response deadline is mandatory** (default 5 minutes). A delivery person cannot stand at a door waiting for a manager. On timeout the delivery person chooses: hand over the disputed lines and invoice in full, with the adjustment processed as a normal return afterwards; or withhold them, so they are simply not invoiced. Both outcomes are recorded, and the choice is theirs because they are the one present. |
| BR-DMG-10 | Timeout rate is monitored. Managers who routinely miss the window turn a doorstep control into a doorstep argument, and that is a staffing finding rather than a software one. |
| BR-DMG-11 | `[LEGAL]` The customer acknowledges the final invoice — an OTP to their registered number, or a signature capture — before payment. Without acknowledgement there is nothing to point at when they later dispute what they agreed to. |
| BR-DMG-12 | `[MONEY]` Refused and damaged lines return to `quarantine`, never to sellable stock, and are written off or returned to the supplier through the normal RTV path (BR-RET-07, §12A). |
| BR-DMG-13 | `[MONEY]` Damage rates are reported by **reason code, by supplier, by batch and by delivery person**. Damage concentrated in one supplier's batches is a purchasing conversation; damage concentrated on one delivery route is a different conversation entirely. |
| BR-DMG-14 | Adjustment events (`delivery.adjustment_requested`, `delivery.adjustment_approved`, `delivery.adjustment_rejected`, `delivery.adjustment_timed_out`, `challan.issued`, `challan.closed`, `invoice.issued_at_delivery`) are emitted per doc 06 §3, and every approval is audited with the actor, the lines, the value and the evidence (BR-ADM-06). |

### 9A.4 Delivery mode — custody sits with the delivery person

The app runs in **delivery mode** when a delivery person signs in on a registered device. In that mode they are not just performing steps; they are the **accountable custodian** of the goods they are carrying.

This is what makes the rest of the section enforceable. Without a named custodian, "goods left and never came back" is a discrepancy with no owner, and every control above degrades into a report nobody acts on.

> **Every unit that leaves the shop is in somebody's custody until it is either invoiced and paid for, or physically received back by someone else.** There is no third outcome, and no point at which the goods belong to nobody.

| ID | Rule |
|---|---|
| BR-DVM-01 | Delivery mode requires the `delivery` role on a registered device. In it, the app shows assigned consignments and nothing else — no order browsing, no customer records, no catalog (BR-STO-39). |
| BR-DVM-02 | `[MONEY]` **Custody is accepted explicitly.** At dispatch the delivery person confirms the consignment against the challan, line by line. Custody transfers on their acceptance, not on the dispatcher's assertion — a custodian who never agreed they had the goods cannot fairly be held to them. |
| BR-DVM-03 | `[MONEY]` A discrepancy at acceptance is raised **then**, against the dispatcher. Once accepted, the goods are the delivery person's responsibility, and that boundary must be unambiguous to both people. |
| BR-DVM-04 | `[MONEY]` Custody is discharged in exactly two ways: the goods are **invoiced and paid for** by the customer (BR-DLV-04, BR-DLV-05), or they are **physically received back** by another person at the shop. Nothing else closes it. |
| BR-DVM-05 | `[SEC][MONEY]` **A delivery person cannot discharge their own custody by declaring a return.** A return must be received and confirmed by someone else, or "returned" becomes the way goods disappear — the same maker-checker reasoning as payment verification (BR-STO-31). |
| BR-DVM-06 | `[MONEY]` Open custody — the value of goods a person is currently carrying — is visible live, per person, and **capped**. Beyond the cap, no further consignment is assigned until some is discharged. This bounds what any single loss can cost. |
| BR-DVM-07 | `[MONEY]` Custody transfer between people — a route change, a shift ending mid-round — is explicit and requires **both** parties: the outgoing custodian releases, the incoming one accepts, line by line. An unacknowledged transfer leaves custody where it was. |
| BR-DVM-08 | `[MONEY]` A consignment open past its expected duration ages on a report naming its custodian, with its value (BR-DLV-09). Ageing open custody is the single most useful number for spotting a problem before a stock-take does. |
| BR-DVM-09 | `[LEGAL]` Handover to the customer is evidenced: the invoice acknowledgement (BR-DMG-11), and where configured a photograph or signature. The custodian's record of discharge must be something other than their own word. |
| BR-DVM-10 | `[MONEY]` End of round is a reconciliation, not a sign-off: every consignment accounted for as invoiced-and-paid or received-back. A round that does not balance is an exception naming that person, raised the same day rather than at month end. |
| BR-DVM-11 | Shortfalls, unverified payments and unclosed challans are tracked **per custodian over time**. One is an accident — a customer who paid late, a parcel left with a neighbour. A pattern is a finding (BR-CPM-27, BR-STO-37). |
| BR-DVM-12 | `[SEC]` Location is captured at acceptance, at handover and at discharge, and used **only** for custody evidence and route reporting. It is personal data about a member of staff: retained to a stated period, never used for anything else, and covered by the same redaction rules as any other PII (BR-DAT-06, BR-SEC-07). |
| BR-DVM-13 | `[SEC]` Signing out of delivery mode with open custody is refused, or requires a transfer (BR-DVM-07). Custody does not lapse because someone closed the app. |
| BR-DVM-14 | Custody events (`custody.accepted`, `custody.transferred`, `custody.discharged`, `custody.discrepancy`, `custody.overdue`, `round.reconciled`) are emitted per doc 06 §3 and audited (BR-ADM-06). |

#### Transit damage — attribution, and why liability is a separate decision

Goods that leave in good condition and arrive damaged were damaged in someone's custody, and that someone is the delivery person. **Attribution is automatic.** Whether it becomes a charge against them is not, and the distinction is deliberate.

> **The risk to guard against: if reporting damage costs the delivery person money, they will stop reporting it.**
>
> The whole doorstep adjustment (§9A.3) depends on honest reporting. Under automatic liability, the rational move for a delivery person is to hand over the damaged item quietly and hope the customer does not notice — so the customer receives damaged goods with no adjustment, complains later, and the shop pays anyway *plus* loses the customer. A control that makes honesty expensive produces dishonesty, not care.

| ID | Rule |
|---|---|
| BR-DVM-15 | `[MONEY]` **Condition is recorded at acceptance, not assumed.** For fragile, high-value or previously-problematic lines, dispatch captures the condition — a photograph where configured — so "it left in good condition" is an evidenced fact. Asserting a condition nobody recorded is not a basis for holding anyone responsible. |
| BR-DVM-16 | `[MONEY]` Damage found at the door while goods are in custody is **attributed to that custody automatically**, recorded with the evidence, and reported. Attribution is a fact about where it happened, not a verdict about fault. |
| BR-DVM-17 | `[SEC][MONEY]` **Charging a custodian for a loss is a separate, deliberate action** requiring manager approval, a reason, and an audit entry. It is never an automatic consequence of an attribution, and it is never applied by the system on its own. |
| BR-DVM-18 | Attributed damage carries a **cause**, decided at review: `handling`, `packing`, `vehicle`, `product_fragility`, `supplier_condition`, `customer_refused_undamaged`, `undetermined`. Damage caused by poor packing or fragile goods is a packing or purchasing problem, and recording it as the delivery person's fault would hide the real cause (BR-DMG-13). |
| BR-DVM-19 | `[MONEY]` A single incident is an incident. **A pattern is a finding**, and the reports are built to show patterns: damage rate per custodian against the shop average, per route, per product category, over time. One broken jar tells you nothing; one person breaking jars every week tells you a great deal. |
| BR-DVM-20 | Reporting damage promptly and with evidence MUST NOT worsen a custodian's position compared with concealing it. Where a policy is applied, prompt disclosure counts in the custodian's favour — otherwise the system is paying people to hide problems. |

### 9A.5 Acceptance — payment transfers risk to the customer

**Payment is acceptance.** The customer inspected the goods, agreed what they were keeping, the invoice was issued for exactly that, and they paid it. At that moment custody and risk pass from the shop to the customer.

This is why the whole invoice-at-the-door flow is the right shape: it creates a single, evidenced moment of acceptance instead of a dispute about what was in a box that left three days ago.

```
custody:   dispatcher ──► delivery person ──► CUSTOMER
                        (accepts consignment)   (pays invoice)
```

| ID | Rule |
|---|---|
| BR-ACC-01 | `[MONEY]` Payment against the issued invoice constitutes the customer's **acceptance of the goods in their visible condition** and of the invoice contents. Custody and risk transfer at that point, and the shop's custody chain closes (BR-DVM-04). |
| BR-ACC-02 | `[LEGAL]` The acceptance record is the evidence: the invoice, its acknowledgement (BR-DMG-11), the payment, and the timestamps. Together they answer "what did they receive and what did they agree to" without anyone relying on memory. |
| BR-ACC-03 | `[LEGAL]` The customer is told **before** paying that payment confirms acceptance of visible condition, and is given the opportunity to inspect. A term the customer was never shown is not a term they agreed to. |
| BR-ACC-04 | `[MONEY]` A later claim of **visible damage** — crushed, dented, broken, obviously leaking — is refused by default and answered with the acceptance record. That is the point of inspecting at the door. |
| BR-ACC-05 | Post-acceptance claims are still **recorded**, whatever their outcome, and reported by route, custodian and product. A rise in "accepted then complained" is either a doorstep inspection not really happening or a packing problem, and both are worth knowing. |

#### What acceptance does not cover

> **`[LEGAL]` Acceptance settles condition, not conformity.** Statutory rights under the Consumer Protection Act 2019 survive it, and a term saying otherwise is unenforceable. Refusing a legitimate claim by pointing at "you paid, so you accepted" is how a shop loses a consumer forum case it should have settled for the price of one item.

| ID | Rule |
|---|---|
| BR-ACC-10 | `[LEGAL]` Acceptance covers **visible condition only**. It does not waive: a latent defect in a sealed product, goods not as described, a wrong item, an expired or short-dated batch, unsafe or spoiled food, a missing item inside sealed packaging, or a manufacturer's warranty. |
| BR-ACC-11 | `[LEGAL]` Food safety and expiry obligations are **never** discharged by acceptance. A customer who gets home and finds an expired or spoiled item has a valid claim however thoroughly they inspected the outside of the box (BR-ATR-11, BR-BAT-20). |
| BR-ACC-12 | `[LEGAL]` A claim in the categories above routes to the **normal returns process** (§12) with its statutory window, not to the doorstep-damage path. The system MUST NOT be able to refuse such a claim automatically on the grounds of acceptance. |
| BR-ACC-13 | `[LEGAL]` The distinction is recorded per claim — `visible_condition` versus `conformity` — because they have different answers, different windows and different legal footing. Collapsing them into "damaged" loses the only thing that determines the outcome. |
| BR-ACC-14 | `[MONEY]` Where a conformity claim is upheld after acceptance, it is a **credit note** against the issued invoice (BR-DOC-30) and the stock returns to `quarantine` (BR-RET-07). The invoice itself is never altered. |
| BR-ACC-15 | Acceptance events (`order.accepted`, `risk.transferred`, `claim.post_acceptance_raised`, `claim.post_acceptance_resolved`) are emitted per doc 06 §3. |

### 9A.6 Proof of delivery — a photograph on every delivery

**Every delivery is photographed at handover, and payment cannot be recorded until it is.** Not only the damaged ones.

This is what makes acceptance defensible. Refusing a later visible-damage claim (BR-ACC-04) rests on being able to show the condition of the goods when they changed hands — and a photograph taken only when something looked wrong proves nothing about the deliveries where nobody looked.

| ID | Rule |
|---|---|
| BR-POD-01 | `[MONEY]` A proof-of-delivery photograph is **mandatory on every delivery**. The invoice is not issued and payment is not recorded until one has been captured. |
| BR-POD-02 | `[SEC]` The photograph is **captured in-app from the camera**. Choosing an existing image from the gallery is prohibited, or an old photo can be re-used to evidence a delivery that never happened. |
| BR-POD-03 | `[SEC]` Capture time and location are bound to the image at capture and stored with it, not taken from the upload (BR-DVM-12). |
| BR-POD-04 | `[LEGAL]` The photograph is **of the goods only — never of a person.** The frame shows the delivered items and enough context to identify them; it does not show the customer, the delivery person, or anyone else. This is a hard rule, not a preference (BR-POD-22). |
| BR-POD-04a | `[LEGAL]` The goods are **placed down and photographed** — on the doorstep, a surface, or in the customer's own container. They are **not held up in front of anyone**, by the delivery person or the customer. Composing the shot around a person is how a face ends up in frame, and holding items to the camera also makes worse condition evidence: the items sit closer to the lens, at an angle, partly obscured by whoever is holding them. |
| BR-POD-04b | `[LEGAL]` No person is posed with, behind, or holding the items. An incidental hand steadying a box is not a breach; a torso or a face in frame is (BR-POD-22). |
| BR-POD-05 | The capture screen states, at the moment of capture: **"Place the items down. Photograph the goods only — no people."** The guidance is repeated in delivery training. A rule nobody is reminded of at the point of action is a rule that erodes. |
| BR-POD-06 | `[LEGAL]` A photograph found to contain a person is deleted on discovery and re-taken where the delivery is still open. Reported occurrences are a training finding, not a disciplinary one — the aim is fewer such photographs, not fewer reports of them. |

#### Capture blocks; upload does not

> **The control is the capture, not the round trip.** A delivery person in a stairwell with one bar must not be unable to complete a sale because a 2 MB image is still uploading. Blocking on upload would produce exactly the workaround that defeats the control: doing the delivery outside the app and reconciling it later.

| ID | Rule |
|---|---|
| BR-POD-10 | `[MONEY]` **Capture is blocking; upload is not.** The invoice is issued once the photograph is captured and its hash recorded. The image uploads in the background with retry and aggressive compression. |
| BR-POD-11 | `[SEC]` The hash is recorded at capture and verified on arrival. An image that does not match its recorded hash is rejected and raises an exception — this is what stops a photograph being substituted between capture and upload. |
| BR-POD-12 | `[MONEY]` A photograph unuploaded past a configured window (default 2 hours, or end of round if sooner) is an **exception naming the custodian**. Deliveries with permanently missing evidence are reported, because a pattern of them is how the control gets hollowed out. |
| BR-POD-13 | Images are compressed on the device to a stated maximum. Condition evidence needs a legible photograph, not a high-resolution one, and a delivery fleet generates a great many of them. |

#### Privacy — this is a photograph of somebody's home

> **`[LEGAL]` A proof-of-delivery photograph is personal data**, often of a customer's doorway and sometimes of the customer. It is collected for one purpose and must be governed like any other personal data the shop holds (BR-DAT-06, BR-SEC-07).

| ID | Rule |
|---|---|
| BR-POD-20 | `[LEGAL]` The purpose is **condition and delivery evidence, and nothing else.** These images MUST NOT be used for marketing, for staff performance surveillance beyond delivery reconciliation, or for any analysis not stated to the customer. |
| BR-POD-21 | `[LEGAL]` The customer is told at ordering that deliveries are photographed, and why. Photographing someone's home without telling them is not acceptable, whatever the operational benefit. |
| BR-POD-22 | `[LEGAL]` **Photographing a person is prohibited outright.** Not discouraged, not avoided where possible — prohibited. The subject is the goods. A customer, a delivery person, a passer-by or a child in frame makes the photograph non-compliant, and it is deleted rather than stored (BR-POD-06). |
| BR-POD-22a | `[LEGAL]` The prohibition is stated to the customer at ordering and shown to the delivery person at capture, so both sides know what is and is not being recorded (BR-POD-05, BR-POD-21). |
| BR-POD-22b | `[LEGAL]` Where a customer objects to any photograph being taken, a signature or OTP acknowledgement is accepted instead and the objection is recorded. Proof of delivery is not worth overriding someone's refusal to be photographed at their own front door (BR-DMG-11). |
| BR-POD-22c | `[SEC]` The system MUST NOT run face detection, recognition or any biometric processing over these images — not to enforce this rule, and not for any other purpose. Building a face-detection pipeline across every customer's doorstep would be a far worse privacy outcome than the problem it policed. The controls are the instruction at capture, the deletion on discovery, and the audit of access. |
| BR-POD-23 | `[LEGAL]` Retention is bounded: a routine proof-of-delivery photograph is deleted after a stated period (default 90 days). Images attached to an open claim or dispute are retained until it closes, and then follow the same rule. |
| BR-POD-24 | `[SEC]` Access is restricted to the roles that need it — customer care, manager, finance for a dispute — and every access is audited. A delivery evidence store is a searchable archive of where customers live, and it must be treated as one. |
| BR-POD-25 | `[SEC]` Images are stored in object storage, private, encrypted at rest, served only through short-lived authenticated links, and stripped of EXIF beyond the capture metadata this section requires (BR-MED-04, BR-MED-07). |
| BR-POD-26 | Proof-of-delivery events (`pod.captured`, `pod.uploaded`, `pod.upload_overdue`, `pod.hash_mismatch`, `pod.accessed`) are emitted per doc 06 §3. |

---

## 10. Order lifecycle

### States

```
draft ─> pending_payment ─> paid ─────> packed ─> shipped ─> delivered
           │                  │                                 │
           │                  ├─> cancelled                     └─> returned ─> refunded
           ├─> payment_failed │
           ├─> expired        └─> awaiting_stock ─> packed
           └─> confirmed  (COD path)
```

### Business rules

| ID | Rule |
|---|---|
| BR-ORD-01 | Transitions are defined by an explicit allowed-from table in one place. Scattered `if status ==` checks are prohibited. |
| BR-ORD-02 | An invalid transition is an error, never a silent no-op. |
| BR-ORD-03 | `[LEGAL][MONEY]` Order lines snapshot SKU, title, unit price, GST rate, and computed tax at placement. An order view must never join to live catalog data for these fields. |
| BR-ORD-04 | Order totals are immutable after placement. Corrections happen through refund or adjustment records, never by editing the order. |
| BR-ORD-05 | `[SEC]` Every order read checks ownership against the session actor, or an admin role. Order IDs and numbers are unguessable. |
| BR-ORD-06 | Every transition writes an audit row: actor, from-state, to-state, reason, request ID, IP, timestamp. |
| BR-ORD-07 | A customer may cancel an order only while it is `paid`, `confirmed` or `awaiting_stock`. After `packed`, cancellation is an admin action. |
| BR-ORD-08 | Cancellation restores stock (`on_hand` incremented), restores coupon usage, and initiates a refund if payment was captured. |
| BR-ORD-09 | `[LEGAL]` A GST invoice is generated on transition to `paid` (or `confirmed` for COD) and is immutable. Corrections are issued as credit notes. |
| BR-ORD-10 | Invoice numbers are sequential per financial year with no gaps. Generation is serialized to guarantee this. |
| BR-ORD-11 | Partial fulfilment is permitted: an order may have several shipments. The order reaches `delivered` only when all shipments are delivered. |
| BR-ORD-12 | `expired` is terminal and applies to `pending_payment` orders past the reservation window. Expired orders retain their record for funnel analysis. |

---

## 11. Fulfilment and shipping

### Features

- Pick list and packing slip generation.
- Courier assignment with AWB (tracking) number.
- Multiple shipments per order.
- Customer-facing tracking page.
- Delivery status ingestion from the courier.

### Business rules

| ID | Rule |
|---|---|
| BR-FUL-01 | An order can be packed only when every line is either in stock or explicitly marked as a partial shipment. |
| BR-FUL-02 | A shipment records its lines, quantities, courier, AWB, dispatch timestamp, and billable weight. |
| BR-FUL-03 | Shipped quantity per line can never exceed ordered quantity. |
| BR-FUL-04 | `[SEC]` The tracking page is accessible with the order number plus the last 4 digits of the delivery phone number, or with an authenticated session. The order number alone is insufficient. |
| BR-FUL-05 | Courier status updates are ingested idempotently, keyed on courier event ID. Out-of-order events never move an order backwards. |
| BR-FUL-06 | `delivered` is set only by a courier-confirmed delivery event or an explicit admin action with a recorded reason. |
| BR-FUL-07 | A shipment undeliverable after the courier's retry policy moves to `rto` (return to origin); stock is restored on receipt, not on the RTO event. |

---

## 12. Returns, cancellations and refunds

### Features

- Customer-initiated return request within the return window.
- Reason codes and photo upload for damaged goods.
- Admin approval workflow.
- Reverse pickup scheduling.
- Refund to the original payment method, or to a bank account for COD.

### Business rules

| ID | Rule |
|---|---|
| BR-RET-01 | Return window is 7 days from delivery by default, configurable per category. Non-returnable categories are flagged on the product and stated before purchase. |
| BR-RET-02 | A return may be requested only for `delivered` orders within the window, for quantities not already returned. |
| BR-RET-03 | `[MONEY]` A refund amount is computed from the order line snapshots, including the proportional share of any discount, and its GST. Shipping is refunded only for a full-order return or a Steleios-caused fault. |
| BR-RET-04 | `[MONEY]` Cumulative refunds against an order can never exceed the amount captured. |
| BR-RET-05 | `[MONEY]` **The client pays the refund; Steleios records it** (§12.2). The money goes out through the shop's own UPI or bank, exactly as it came in (ADR 0008). |
| BR-RET-06 | `[SEC]` COD refunds require bank details verified against the account holder name, an admin approval by a second actor, and a full audit trail. |
| BR-RET-07 | Stock is restored on physical receipt and inspection, not at return approval. Damaged returns restore to a `quarantine` location, not to sellable stock. |
| BR-RET-08 | `[LEGAL]` A credit note is issued for every refund, referencing the original invoice number. |
| BR-RET-09 | `[MONEY]` A refund is `refund_unverified` until the outgoing debit is matched in the shop's statement (§12.2). Recording a refund is a claim that it was sent, not proof that it was. |

### 12.2 Recording a refund — the client pays it, the system records it

Refunds mirror payments exactly: **the shop sends the money through its own UPI or bank, and Steleios records the transaction reference and the details.** The system is not in the refund path any more than it is in the payment path (ADR 0008).

| ID | Rule |
|---|---|
| BR-RFD-01 | `[MONEY]` A **credit note establishes what is owed** (BR-DOC-30). Paying it is a separate, later act, and the two are never conflated: a credit note is not a refund. |
| BR-RFD-02 | `[MONEY]` The client refunds through their own channel and then **records it**: amount, date, method (UPI, bank transfer, cash at counter), **transaction reference**, the destination, and who recorded it. |
| BR-RFD-03 | `[MONEY]` The transaction reference is mandatory and format-validated, and is **unique across all payments and refunds** (BR-CPM-06). One reference cannot settle two refunds. |
| BR-RFD-04 | `[MONEY]` A refund is `refund_unverified` until reconciliation matches the **outgoing debit** in the shop's statement (BR-CPM-20). Recording it is a claim; the statement is the proof. |
| BR-RFD-05 | `[MONEY]` Cumulative refunds against an invoice can never exceed its credit notes, which can never exceed the invoice (BR-RET-04, BR-DOC-34). |
| BR-RFD-06 | `[SEC]` Recording a refund requires `refund:write`, and above a configured value a second approver (BR-ADM-04). `counter_sales` and `delivery` hold neither — a refund is the one counter action that moves money outward. |
| BR-RFD-07 | `[SEC]` Refund destination details are entered at the point of refund and **never** taken from a customer-supplied field on an order. A per-order payee field would be a way to redirect refunds one order at a time (BR-STO-20 reasoning). |

#### The two failure modes, and the control for each

| ID | Rule |
|---|---|
| BR-RFD-10 | `[MONEY]` **A refund recorded but never sent** — closing a complaint on paper. Caught by reconciliation: no matching debit in the statement. Unmatched past the configured window is an exception naming the recorder (BR-CPM-21). |
| BR-RFD-11 | `[MONEY]` **A refund owed but never issued** — the credit note sits unpaid. This is the one that damages customers, so outstanding credit notes with no recorded refund are a **payable**, aged and reported daily like any other (BR-DOC-42). A shop that issues credit notes and never pays them will see the number grow. |
| BR-RFD-12 | `[MONEY]` A statement debit with no matching recorded refund is equally an exception: money left the account that no document accounts for (BR-CPM-22). |
| BR-RFD-13 | Refund ageing — how long between credit note and money sent — is reported. It is the customer-experience number the shop should be judged on, and nobody measures it unless the system does. |
| BR-RFD-14 | Refund events (`refund.recorded`, `refund.verified`, `refund.unmatched`, `credit_note.unpaid_aged`) are emitted per doc 06 §3, and every recording and approval is audited (BR-ADM-06). |

#### Why the reference is captured at all

The transaction reference is not bureaucracy. It is the **join** between the shop's books and its bank statement, and it exists so that **the client's accountant can reconcile the two without asking anybody anything.**

| ID | Rule |
|---|---|
| BR-RFD-20 | `[LEGAL]` Every money movement recorded — payment in, refund out — carries the reference that identifies it in the shop's own bank or UPI statement. Without it, reconciliation is a manual matching exercise on amounts and dates, which fails the moment two customers pay the same amount on the same day. |
| BR-RFD-21 | `[LEGAL]` The **export gives the CA both sides**: the document (invoice or credit note) and the money movement against it, with the reference. That is what makes an export reconcilable against a bank statement rather than merely readable (BR-DOC-60, BR-RSP-22). |
| BR-RFD-22 | `[LEGAL]` Every figure traces to its documents and every document to its money movement, so a CA can follow any number end to end without a conversation (BR-DOC-53). An accountant who cannot verify a number will re-key it into a spreadsheet, and then the system is not the record any more. |
| BR-RFD-23 | Unreconciled items — recorded but unmatched, in either direction — are **exported as such rather than hidden**. A CA needs to see what is outstanding, not a tidy file that implies everything cleared. Presenting unmatched items as settled would be the one genuinely misleading thing an export could do. |

---

### 12.1 Returnability — configured, disclosed, date-based

| ID | Rule |
|---|---|
| BR-RET-10 | `[LEGAL]` Returnability is configured at category level and overridable per product and per variant: `returnable`, `return_window_days`, `exchange_only`, or `non_returnable` with a reason (hygiene, perishable, made-to-order, clearance). |
| BR-RET-11 | `[LEGAL]` The return option and its window MUST be disclosed on the product page **before** purchase, and are snapshotted onto the order line at placement (BR-VER-03). A later policy change never shortens a window a customer already bought under. |
| BR-RET-12 | `[MONEY]` The window is **date-based**, counted from the delivery date in `Asia/Kolkata`, and expires at end of day. Boundary behaviour is inclusive of the final day. |
| BR-RET-13 | Perishable and batch-tracked goods may carry a shorter window, or none. A batch sold under a near-expiry markdown (BR-BAT-30) defaults to `non_returnable`, disclosed at the point of sale (BR-BAT-33). |
| BR-RET-14 | The order detail page shows, per line, whether it can be returned and until when — computed from the snapshot, not from current configuration. |
| BR-RET-15 | `[LEGAL]` A faulty, damaged, wrong or not-as-described item is returnable regardless of the configured window. Statutory rights override configuration, and the system MUST NOT be able to refuse such a request outright — it routes to admin review. |
| BR-RET-16 | Partial-line returns are supported: a customer may return 2 of 5 units. Quantities are tracked per line so cumulative returns can never exceed the delivered quantity. |
| BR-RET-17 | Exchange is modelled as a return plus a new order, linked by a reference. It MUST NOT mutate the original order (BR-ORD-04). |

---

## 12A. Returns to supplier

Batches expire, arrive damaged, or get recalled. That stock has to leave inventory and, where terms allow, be recovered from the supplier.

| ID | Rule |
|---|---|
| BR-RTV-01 | A return-to-vendor (RTV) document references a supplier, one or more batches, quantities in base UoM, a reason (`expired`, `damaged_in_transit`, `short_shipped`, `quality_reject`, `recall`, `overstock`), and the batch's snapshotted cost. |
| BR-RTV-02 | `[MONEY]` RTV quantities are deducted from the batch as a stock movement with reason `rtv`, never by editing `on_hand` directly (BR-BAT-08). |
| BR-RTV-03 | Only `quarantine`, `expired` or `recalled` batch quantities may be returned to a supplier. Returning `active` sellable stock requires an explicit override with a reason and approval. |
| BR-RTV-04 | `[MONEY]` RTV settlement is tracked to closure: credit note expected, credit note received, or written off. Unsettled RTV value ageing is reported to finance — an RTV that is never followed up is an unrecovered loss. |
| BR-RTV-05 | `[LEGAL]` A supplier credit note is recorded against the original purchase invoice with its GST components, so input tax credit is reversed correctly. |
| BR-RTV-06 | `[SEC]` Creating and approving an RTV are separate permissions above a configured value threshold (BR-ADM-04), and both are audited. |
| BR-RTV-07 | Supplier quality is scored from RTV rate by reason code and surfaced on the supplier record, so purchasing decisions are informed by evidence (BR-SUP-01). |
| BR-RTV-08 | RTV events (`rtv.created`, `rtv.dispatched`, `rtv.credited`, `rtv.written_off`) are emitted per doc 06 §3. |

---

## 13. Reviews

### Business rules

| ID | Rule |
|---|---|
| BR-REV-01 | Only a customer with a `delivered` order containing the product may review it. One review per customer per product. |
| BR-REV-02 | Reviews enter `pending` and are visible only after moderation. |
| BR-REV-03 | `[SEC]` Review text is stored raw and escaped on output. HTML is never rendered from user input. |
| BR-REV-04 | Rating is an integer 1–5. |
| BR-REV-05 | Aggregate rating is recomputed on approval or removal and cached with explicit invalidation. |
| BR-REV-06 | A customer may edit their review within 30 days; edits re-enter moderation. |

---

## 14. Notifications

### Business rules

| ID | Rule |
|---|---|
| BR-NOT-01 | All notifications are enqueued on asynq. Sending inline in a request handler is prohibited. |
| BR-NOT-02 | Delivery is retried with exponential backoff up to 5 attempts, then moved to a dead-letter queue with an alert. |
| BR-NOT-03 | Transactional messages (order confirmation, payment receipt, dispatch, delivery, refund, OTP, password reset) are always sent and are not subject to marketing consent. |
| BR-NOT-04 | `[LEGAL]` Marketing messages require explicit opt-in, honour opt-out immediately, and every message carries an unsubscribe path. |
| BR-NOT-05 | Each notification type is sent at most once per triggering event, enforced by an idempotency key of `(event_id, template, channel)`. |
| BR-NOT-06 | `[SEC]` OTPs, password reset links and payment identifiers are never written to application logs. |

---

## 15. Admin, roles and audit

### Roles

Roles are defined once, in `internal/platform/authz`, and enforced only through the single enforcer (SEC-10). The table below is the authority; the code mirrors it and privilege-boundary tests assert the shape.

### Two disjoint worlds

> **The vendor creates and manages the SaaS. The client owns and operates the business.** (docs/09 §6)

| World | Roles | Reaches |
|---|---|---|
| **Shop** | owner, admin, manager, counter_sales, delivery, data_entry, viewer, support, ops, finance, catalog, marketing, purchasing | Orders, stock, customers, money — confined to one shop by row-level security |
| **Platform** (vendor) | `saas_admin`, `saas_support` | Which clients exist, their shops, their subscriptions, service health — **and no shop's business data at all** |

| ID | Rule |
|---|---|
| BR-ADM-14 | `[SEC]` **The two sets of actions are disjoint.** A platform role holds no shop action; a shop role holds no platform action. A vendor role that could read a shop's orders would make the vendor a participant in its customers' businesses; a shop role that could provision clients would let one customer reach another. Asserted by test in both directions, because both are one careless grant away. |
| BR-ADM-15 | `[SEC]` `saas_admin` **creates and manages SaaS clients** — onboarding, provisioning shops, subscriptions, suspension — and cannot read or write any order, product, customer or document. |
| BR-ADM-16 | `[SEC]` `saas_support` is read-only over provisioning and billing, so a subscription question is answerable without reaching into a client's business. |
| BR-ADM-17 | `[SEC]` `admin` is the **client's own** technical administrator, not the vendor's. The naming is unfortunate and the distinction is load-bearing: the vendor's roles are the `saas_*` ones. |
| BR-ADM-18 | `[SEC]` Any vendor access to a client's data is exceptional, requires the controls in docs/09 §4, and **appears in that client's own audit log** (BR-LIC-51). A customer can see when the vendor looked at their data. |

| Role | Grants | Deliberately excluded, and why |
|---|---|---|
| `owner` | Everything, including user and role management and GST rate changes | — the role that cannot be locked out |
| `admin` | Identical to `owner`; the technical counterpart | — |
| `manager` | Orders, stock adjustment, catalog, **pricing**, refunds, purchasing, marketing, loyalty, reports | **User management** — a manager must not be able to grant themselves more. **Tax rates** — legally consequential, notification-backed, owner level (BR-TAX-09). **Contact exports** — the highest-volume data-loss path |
| `counter_sales` | Catalog read, stock read, order read and **create**, loyalty award and redeem | **Refunds** — the one till action that moves money outward; a return at the counter routes to a manager. **Stock adjustment** — they sell stock, they do not correct it. **Customer read** — a till needs a loyalty lookup, not a customer browser. **Payment verification** — they take payments, so they cannot confirm them (BR-STO-31) |
| `delivery` | `delivery:update` only — see assigned deliveries, mark delivered, assert payment taken | **Everything else.** No order browsing, no customer records, no catalog, no reports. Above all **payment verification**: the person who took the payment can never be the person who confirms it arrived, which is the control that catches payee substitution (BR-STO-31) |
| `data_entry` | Catalog read and write, stock read, purchasing read | **Pricing, orders, customer data, exports** — usually the largest group of accounts on the least controlled hardware, so it gets the smallest reach into money and personal data |
| `viewer` | Read orders, customers, catalog, reports, purchasing | Any write |
| `support` | viewer + order write (notes, address correction pre-dispatch, cancellation) | Refunds, stock adjustment |
| `ops` | support + stock adjustment and shipment management | Refunds, pricing |
| `finance` | Order read, **refunds**, customer read, reports, purchasing read | Catalog, stock, pricing |
| `catalog` | Catalog read and write, stock read, **pricing**, reports | Orders, customers, refunds |
| `marketing` | Catalog read, customer read, reports, campaigns, **contact export**, loyalty | Orders, stock, pricing |
| `purchasing` | Catalog read, stock read and write, purchasing read and write, reports | Orders, pricing, customers |

| ID | Rule |
|---|---|
| BR-ADM-10 | Role grants are **strictly nested under `owner`**: no role holds an action `owner` does not, and no role other than `owner`/`admin` holds every action. An undeclared super-user is a defect, and a test asserts it. |
| BR-ADM-11 | `[SEC]` `user:manage` is held only by `owner` and `admin`. It is the escalation path; if any other role holds it, every other boundary in this table is decorative. |
| BR-ADM-12 | A staff member may hold several roles; grants are the union. There is no role hierarchy at runtime — the nesting above is a design property asserted by tests, not an inheritance mechanism (OOP-08). |
| BR-ADM-13 | `[SEC]` Customers hold no roles. A customer record carrying a staff role grants nothing: customers are authorised by ownership only, and the enforcer ignores their roles entirely. |

### Business rules

| ID | Rule |
|---|---|
| BR-ADM-01 | `[SEC]` Authorization is enforced in middleware **and** re-asserted in the service layer. Hiding a UI control is never an access control. |
| BR-ADM-02 | `[SEC]` Every admin action checks the actor's permission against the specific resource, not merely the presence of a role. |
| BR-ADM-03 | `[SEC]` Role changes require the `admin` role, invalidate the target's sessions, and are audited. |
| BR-ADM-04 | `[SEC]` Refunds above a configured threshold require approval by a second actor distinct from the initiator. |
| BR-ADM-05 | The audit log is append-only: no `UPDATE`, no `DELETE`, enforced by database permissions on the application role. |
| BR-ADM-06 | Every audit entry records actor ID, action, resource type and ID, before-state, after-state, reason, IP, user agent, request ID and timestamp. |
| BR-ADM-07 | `[SEC]` Admin sessions have a 12-hour TTL and require re-authentication for refunds, role changes and price edits. |
| BR-ADM-08 | `[SEC]` Admin access is rate-limited and admin authentication failures raise an alert after 5 failures in 10 minutes. |
| BR-ADM-09 | Audit entries are retained for 7 years and are exportable. |

---

## 16. Reporting

### Business rules

| ID | Rule |
|---|---|
| BR-RPT-01 | Reports read from materialized views refreshed on a schedule, never from ad-hoc analytical queries against live transactional tables. |
| BR-RPT-02 | Every report states its data freshness timestamp. |
| BR-RPT-03 | `[MONEY]` Revenue figures are net of refunds and exclude tax and shipping unless explicitly labelled as gross. The definition is stated on every revenue report. |
| BR-RPT-04 | Date ranges and pagination are applied server-side. Unbounded result sets are prohibited. |
| BR-RPT-05 | `[SEC]` Exports containing customer PII require the `admin` or `finance` role and are audited with the row count and filter used. |
| BR-RPT-06 | Launch set: revenue by day, conversion funnel, top variants, abandoned carts, refund rate, COD refusal rate. |

---

## 17. Cross-cutting security rules

| ID | Rule |
|---|---|
| BR-SEC-01 | All traffic over TLS. HSTS enabled. Cookies `Secure` in every non-local environment. |
| BR-SEC-02 | All input validated server-side for type, range, format and business rule. Client-side validation is UX only. |
| BR-SEC-03 | All database access through parameterized queries (sqlc). String-concatenated SQL is prohibited. |
| BR-SEC-04 | Output escaped at render. No `v-html` on any user-supplied content. |
| BR-SEC-05 | CSRF tokens required on every state-changing request except the signature-verified webhook route. |
| BR-SEC-06 | Mass assignment prevented: request structs enumerate permitted fields explicitly. Binding directly to domain models is prohibited. |
| BR-SEC-07 | Secrets read from environment only. No secret in source, in a log line, or in an error message returned to a client. |
| BR-SEC-08 | Uploads (review photos, product media) are type-checked by content sniffing, size-limited, stripped of EXIF, stored outside the web root, and served from a separate origin. |
| BR-SEC-09 | Errors returned to clients are generic. Stack traces and driver errors are logged, never returned. |
| BR-SEC-10 | Rate limits are applied per route class and enforced on both IP and authenticated actor where both exist. |
| BR-SEC-11 | Fail closed: if an authorization, price or stock check cannot complete, the operation is refused. |
| BR-SEC-12 | Dependency and secret scanning block merge. `govulncheck` and `bun audit` run in CI on every PR. |

---

## 18. Data retention and compliance

| ID | Rule |
|---|---|
| BR-DAT-01 | `[LEGAL]` Orders, invoices, credit notes and the audit log are retained for 7 years for tax purposes and are exempt from deletion requests. |
| BR-DAT-02 | `[LEGAL]` A customer may request export of their personal data, delivered within 30 days in a machine-readable format. |
| BR-DAT-03 | `[LEGAL]` A deletion request anonymises the customer record and all non-financial PII. Order financial records are retained per BR-DAT-01 with the customer reference anonymised. |
| BR-DAT-04 | Abandoned guest carts are purged after 90 days. |
| BR-DAT-05 | Webhook payloads are retained for 90 days, then reduced to the event ID and outcome. |
| BR-DAT-06 | PII is encrypted at rest. Database backups are encrypted and access-audited. |

---

## Appendix A — Rules requiring a decision before Phase 2

These have placeholder values above and must be confirmed:

1. **Reservation window** — currently 15 minutes (BR-INV-04). Longer helps UPI; shorter protects limited stock.
2. **Return window** — currently 7 days (BR-RET-01), and which categories are non-returnable.
3. **Stacking policy** — currently one coupon per order (BR-DSC-05).
4. **COD limits** — `cod_min`, `cod_max`, and the refusal threshold in BR-COD-04.
5. **Line quantity cap** — currently 50 (BR-CRT-05).
6. **Refund second-approval threshold** — BR-ADM-04.
7. **Single vs multi-warehouse** — changes the inventory primary key and every rule in §2.
8. **INR-only vs multi-currency** — changes the price schema and §3 throughout.

## Appendix B — Test coverage obligation

Per project convention, every `BR-*` rule needs at least one passing and one failing case. Priority order for test authoring:

1. All `[MONEY]` rules — §3 pricing, §9 payments, §12 refunds.
2. All `[SEC]` rules — §6 identity, §9 signature verification, §15 authorization.
3. All `[LEGAL]` rules — GST computation, invoice immutability, retention.
4. State machine transitions — every allowed transition and a representative sample of rejected ones.
5. Everything else.
