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

## 9. Payments

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
| BR-RET-05 | Refunds are issued to the original payment method via Razorpay. Refunds to an alternative destination are prohibited except for COD orders. |
| BR-RET-06 | `[SEC]` COD refunds require bank details verified against the account holder name, an admin approval by a second actor, and a full audit trail. |
| BR-RET-07 | Stock is restored on physical receipt and inspection, not at return approval. Damaged returns restore to a `quarantine` location, not to sellable stock. |
| BR-RET-08 | `[LEGAL]` A credit note is issued for every refund, referencing the original invoice number. |
| BR-RET-09 | Refund state is driven by Razorpay's `refund.processed` webhook, not by the refund API call's response. |

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

| Role | Capabilities |
|---|---|
| `viewer` | Read orders, customers, catalog, reports |
| `support` | viewer + order notes, address correction pre-dispatch, cancellation |
| `ops` | support + fulfilment, stock adjustment, shipment management |
| `finance` | viewer + refunds, settlement import, invoice and credit note reissue |
| `catalog` | viewer + product, variant, price and media management |
| `admin` | All of the above + user and role management |

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
