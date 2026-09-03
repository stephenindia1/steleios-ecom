# Steleios — SEO and AI Discoverability

Engraved rules for organic and AI-assisted discovery. **Normative: MUST / MUST NOT.**
Companions: [01-architecture.md](01-architecture.md) §6 · [05-data-access-and-performance.md](05-data-access-and-performance.md) §7 · [06-observability-and-event-logging.md](06-observability-and-event-logging.md).

Status: draft · 3 September 2026

---

## 0. What changed, and what this means for the build

Search now has two consumers of the same page, with different capabilities:

1. **Classic crawl-and-rank** — Googlebot fetches, renders (with delay), indexes, ranks. Tolerates JavaScript, eventually.
2. **AI answer surfaces** — Google AI Overviews and AI Mode, plus ChatGPT Search, Perplexity, Claude and Copilot. These retrieve pages, ground an answer in them, and cite a few. Most of these crawlers **do not execute JavaScript at all**, and the ones that do are working against a much tighter latency budget than the classic index.

The consequence is blunt: **content that only exists after client-side hydration is invisible to the AI layer.** A price rendered by `fetch` after mount is not in the HTML an answer engine reads. It will describe your product without a price, or not cite you.

| ID | Rule |
|---|---|
| SEO-001 | Every fact that should influence a search result or an AI answer — title, description, price, currency, availability, rating, shipping, returns, specifications — MUST be present in the **server-rendered HTML** of the first response. Client-side hydration may enhance it; it MUST NOT be the only source. |
| SEO-002 | This settles open decision #1 in doc 01 §8: **the storefront is server-rendered (Nuxt 3 SSR)**. A client-only Vite SPA storefront is prohibited. The admin and reporting app remains a Vite SPA, where discovery is irrelevant. |
| SEO-003 | Every storefront route MUST return a complete, useful document to a non-JavaScript client. The test is literal: `curl -s <url> | grep` for the price and the product title. If they are absent, the page is broken. |

**A note on durability.** AI search surfaces change quickly, and specific product behaviours will have moved since this was written. The rules below are deliberately weighted toward what has been stable and is unlikely to invert: server-rendered HTML, correct structured data, fast pages, clean crawlable architecture, and content structured so a machine can extract a self-contained answer. Tactics tied to one vendor's current feature set are marked **[volatile]** and MUST be re-verified against current documentation before relying on them.

---

## 1. Rendering and delivery

| ID | Rule |
|---|---|
| SEO-010 | Product, category, collection, content and home routes are SSR or statically generated with incremental revalidation. Cart, checkout and account routes are client-rendered and `noindex`. |
| SEO-011 | The critical content MUST NOT depend on `onMounted`, `IntersectionObserver`, or any user interaction. Tabs and accordions render their content in the DOM and hide it with CSS, not by omitting it. |
| SEO-012 | Server-rendered HTML MUST NOT differ in substance between bot and human requests. Cloaking is prohibited — it is both a policy violation and a correctness hazard. |
| SEO-013 | TTFB budget 300 ms at p75 for SSR routes. Cached catalog reads back the render (doc 05 RD-007). A slow render costs crawl budget and citation eligibility. |
| SEO-014 | Core Web Vitals targets at p75, field data: **LCP ≤ 2.0 s**, **INP ≤ 200 ms**, **CLS ≤ 0.1**. A release that regresses any of them beyond target does not ship without a recorded decision. |
| SEO-015 | Images carry explicit `width`/`height` (or `aspect-ratio`), `loading="lazy"` below the fold, `fetchpriority="high"` on the LCP image, modern formats with fallbacks, and descriptive `alt` text that names the product — not "image1". |
| SEO-016 | HTTP: HTTP/2 or better, compression, long-lived immutable asset caching, correct `Last-Modified`/`ETag` on documents so conditional crawls are cheap. |
| SEO-017 | JavaScript and CSS MUST NOT be blocked in `robots.txt`. Blocking them breaks rendering for the crawlers that do render. |

---

## 2. Structured data

Structured data is how a machine reads a page without guessing. For AI answer surfaces it is the difference between being parsed correctly and being paraphrased wrongly.

| ID | Rule |
|---|---|
| SEO-020 | Structured data is **JSON-LD in the server-rendered `<head>`**. Microdata, RDFa and client-injected JSON-LD are prohibited. |
| SEO-021 | Structured data MUST match the visible page exactly. A price, rating or availability in JSON-LD that differs from the rendered page is a policy violation and a support incident. It is generated from the same server-side view model — one source, never a second hand-maintained copy (DRY). |
| SEO-022 | Every product page emits `Product` with: `name`, `description`, `sku`, `mpn`/`gtin` where known, `brand`, `image` (multiple, absolute URLs), `offers`, and `aggregateRating` + `review` **only when real reviews exist**. Fabricated or aggregate-only-from-nothing ratings are prohibited. |
| SEO-023 | `offers` MUST carry `price` (decimal string), `priceCurrency: "INR"`, `availability`, `itemCondition`, `url`, `priceValidUntil`, `shippingDetails` and `hasMerchantReturnPolicy`. The last three are what make a listing eligible for merchant surfaces. Values come from the same services that price the cart (BR-PRC-01) and own the return window (BR-RET-01). |
| SEO-024 | Multi-variant products emit `ProductGroup` with `hasVariant` and the declared `variesBy` axes, so a machine understands size/colour rather than seeing near-duplicate products. |
| SEO-025 | Category pages emit `ItemList` with ordered `url` entries. Site-wide: `Organization` (with `sameAs`, `logo`, `contactPoint`), `WebSite` with `SearchAction`, and `BreadcrumbList` on every non-home page. |
| SEO-026 | `FAQPage` markup is used only where a genuine question-and-answer section is visible on the page. **[volatile]** Rich-result eligibility for FAQ has been restricted; the markup is still worth emitting for machine extraction, but MUST NOT be added as a rich-result trick on pages without real Q&A. |
| SEO-027 | Structured data is validated in CI against the schema.org vocabulary, and a golden-file test asserts the emitted JSON-LD for a representative product, category and article page. |

---

## 3. Content structure for machine extraction

An answer engine quotes a passage. Pages MUST be written so a self-contained, correct passage exists.

| ID | Rule |
|---|---|
| SEO-030 | Exactly one `<h1>` per page, naming the entity. Heading hierarchy is strictly nested — no level skipped, headings never chosen for size. |
| SEO-031 | Each section's **first sentence answers its own heading** without needing the surrounding page. Preamble before the answer is prohibited on informational sections. |
| SEO-032 | Product specifications are a real `<table>` with `<th>` headers, or a `<dl>`. Specifications rendered as a styled `<div>` grid are not extractable. |
| SEO-033 | Semantic HTML throughout: `<main>`, `<article>`, `<nav>`, `<time datetime>`, `<address>`. Landmark structure is both accessibility and machine legibility. |
| SEO-034 | Facts that answer buying questions — materials, dimensions with units, care instructions, warranty, delivery estimate, return window, GST-inclusive price — MUST be present as text, not only in an image or a PDF. |
| SEO-035 | Product descriptions are original and specific. Manufacturer boilerplate duplicated across the catalog, and bulk-generated filler, MUST NOT be published. |
| SEO-036 | Entity consistency: the brand name, legal entity, address and contact details are identical everywhere they appear, and match `Organization` structured data. Ambiguous entities get merged or dropped by retrieval systems. |
| SEO-037 | `<title>` ≤ 60 characters and unique per page; `<meta name="description">` ≤ 155 characters, written for a human, unique per page. Templated descriptions differing only by a product name are prohibited on top-level pages. |
| SEO-038 | Open Graph and Twitter Card tags on every public page, with a per-product image. |
| SEO-039 | Publication and update dates are visible and marked up. Freshness is a retrieval signal and a trust signal. |

---

## 4. Crawl architecture

Faceted navigation is the standard way an ecommerce site destroys its own crawl budget. These rules are not optional.

| ID | Rule |
|---|---|
| SEO-040 | URLs are lowercase, hyphenated, human-readable and stable: `/products/{slug}`, `/c/{category-path}`. Internal IDs MUST NOT appear in URLs. |
| SEO-041 | Exactly one canonical URL per product. Variant selection is a query parameter or fragment that canonicalises to the parent product URL — never a separate indexable page per size (BR-CAT-03). |
| SEO-042 | A slug change issues a permanent 301 from the old slug, forever. Old slugs are never reused (BR-CAT-03). |
| SEO-043 | Facet combinations: a small allowlist of high-value facets (category × one attribute) is indexable; **every other combination is `noindex, follow` and canonicalises to the clean category**. Parameter patterns for sort, page size, view mode and session artefacts are disallowed in `robots.txt`. |
| SEO-044 | Pagination uses real crawlable `<a href>` links to distinct `?page=n` URLs. Infinite scroll MUST be backed by paginated URLs. Each page self-canonicalises — it MUST NOT canonicalise to page 1. |
| SEO-045 | Internal search result pages are `noindex`. |
| SEO-046 | An out-of-stock product URL MUST NOT 404 or redirect. It stays live with `availability: OutOfStock` and a restock or alternatives path. Discontinued products 301 to the closest equivalent, or to their category — never to the home page (BR-CAT-07). |
| SEO-047 | Sitemaps are generated from the database, split by type (products, categories, content), under 50k URLs and 50 MB each, indexed by a sitemap index, with accurate `lastmod`. Only canonical, indexable, 200-returning URLs appear. Regenerated by a worker job on catalog change. |
| SEO-048 | `robots.txt` is generated from configuration, disallows `/cart`, `/checkout`, `/account`, `/admin`, `/api`, and search/parameter patterns, and references the sitemap index. It MUST NOT be hand-edited in production. |
| SEO-049 | No orphan pages: every indexable URL is reachable by crawlable links from the home page within four clicks. |
| SEO-050 | IndexNow is pinged on publish, price change and stock transitions for participating engines; sitemap `lastmod` is the signal for Google. **[volatile]** — re-verify supported endpoints before relying on them. |

---

## 5. AI crawler policy

Which bots may fetch, and for what, is a **business decision that MUST be made explicitly** — not left to a default.

Bots split into three purposes, and they are separable:

| Purpose | Examples | Consequence of blocking |
|---|---|---|
| Search indexing | `Googlebot`, `Bingbot` | You disappear from search. Never block. |
| AI answer / retrieval | `OAI-SearchBot`, `PerplexityBot`, and Google's AI surfaces (governed by `Google-Extended`) | You lose citation and referral traffic from answer engines. |
| Model training | `GPTBot`, `ClaudeBot`, `CCBot`, `Bytespider`, `Applebot-Extended` | No traffic effect either way; purely a rights and content decision. |

| ID | Rule |
|---|---|
| SEO-060 | The crawler policy is recorded in `docs/decisions/` with the reasoning and the date, and is reviewed twice yearly. A silent default is not a decision. |
| SEO-061 | Recommended default for a retail storefront: **allow search and answer-engine crawlers** (they drive qualified traffic), and make the training-crawler decision separately and deliberately. Product content is commercial content you want quoted. |
| SEO-062 | `Google-Extended` gates Google's AI usage without affecting classic search ranking. Disallowing it forfeits AI Overview and AI Mode presence. **[volatile]** — confirm current semantics before changing it. |
| SEO-063 | Blocking is by `robots.txt` first. Enforcement for abusive crawlers is by rate limiting on the edge, never by cloaking or by serving different content. |
| SEO-064 | Bot traffic is excluded from business metrics (conversion, funnel) and included in infrastructure metrics. Mixing them corrupts both. |
| SEO-065 | `llms.txt` may be published as a low-cost machine-readable index of key pages. **[volatile]** — it is a community convention, not a standard supported by major engines; it MUST NOT substitute for sitemaps, structured data or SSR. |

---

## 6. Observability for discovery

Discovery is measurable, and unmeasured SEO is superstition. This hooks directly into doc 06.

| ID | Rule |
|---|---|
| SEO-070 | Every request is classified by user agent into `human`, `search_bot`, `ai_bot`, `unknown`, and the classification is a bounded-cardinality metric label: `http_requests_total{audience,bot_name,route}`. |
| SEO-071 | A `crawl.fetched` domain event records bot name, route pattern, status and response time, so "which AI crawlers fetched which products, and what did they get" is a query, not a guess (doc 06 §3). |
| SEO-072 | Referral traffic from answer engines is attributed by referrer and landing page, and reported separately from classic organic. AI referrals convert differently and MUST NOT be averaged into one "organic" number. |
| SEO-073 | Search Console and Merchant Center data are ingested into reporting: impressions, clicks, position, and structured-data errors — surfaced beside revenue, not in a separate silo (BR-RPT-06 extension). |
| SEO-074 | Alert on: sitemap generation failure, a spike in 404s or 5xxs served to search bots, structured-data validation failures in CI, indexable-page count dropping more than 10% week over week, and Core Web Vitals field data breaching target. |
| SEO-075 | Rendering regressions are caught in CI: a test fetches representative routes **with JavaScript disabled** and asserts the presence of title, price, availability and JSON-LD (the automated form of SEO-003). |

---

## 7. Prohibited

| ID | Rule |
|---|---|
| SEO-080 | Cloaking, doorway pages, hidden text, keyword stuffing, and link schemes are prohibited without exception. |
| SEO-081 | Structured data that does not match visible content is prohibited (SEO-021), including review markup for reviews that are not shown, and offers for products not sold. |
| SEO-082 | Mass-generated thin content — programmatic pages with no distinct value, spun descriptions, or bulk AI-written product copy published unreviewed — MUST NOT be published. It is the most likely way to lose accumulated authority. |
| SEO-083 | Fake urgency, fake stock counts, and fake review counts are prohibited. They are consumer-protection exposure as well as a ranking risk. |
| SEO-084 | Indexable pages MUST NOT be generated for combinations that produce no results or duplicate another page's content. |

---

## 8. Review checklist — discovery

A pull request touching a storefront route is rejected if any of these is true.

1. A price, availability, title or review is absent from the server-rendered HTML.
2. JSON-LD is injected client-side, is hand-maintained separately from the view model, or disagrees with the visible page.
3. A new route has no `<title>`, no meta description, or no canonical.
4. A new facet or parameter is crawlable without an indexing decision.
5. Pagination has no distinct crawlable URLs, or pages canonicalise to page 1.
6. An out-of-stock or removed product now 404s.
7. The sitemap generator was not updated for a new indexable route type.
8. A new bot user agent is handled without classification for metrics.
9. Core Web Vitals regress beyond target on a budgeted route.
10. The JavaScript-disabled render test was not extended to cover the new route.
