# Hiddify user create/delete — optimized implementation guide

> Hand this to the session that will implement user creation/deletion in **bot-free**.
> It distills the battle-tested patterns from a sister project (`invoice_system`) that runs
> against ~10 production Hiddify v12 panels, plus the exact Hiddify v2 API contract and the one
> performance trick that matters. Targets this repo's stack: **Go 1.25 + telebot.v4 + SQLite**,
> existing client at `internal/hiddify/hiddify.go`.

---

## 0. TL;DR — what to actually build

bot-free's workload is: **create one user per claim (interactive)** and **delete many expired
users once a day (batch)**. So:

1. **CREATE stays single-call** (there is no bulk-create in Hiddify) — but make it **robust**:
   retry-with-backoff, a tuned HTTP transport, and a **pending→confirmed** record so a panel-create
   that succeeds is never lost / never orphaned.
2. **DELETE becomes bulk** — the daily cleanup must stop doing N sequential `DELETE /user/{uuid}/`
   (each one reapplies the whole proxy config server-side). Instead resolve the numeric ids and use
   Hiddify's **native Flask-Admin bulk action** = *one* server-side apply for the whole batch.
3. Add the cross-cutting hardening bot-free lacks: **retry/backoff**, **id cache**, **per-panel
   serialization + pacing**, **idempotency**, and the **v12 gotchas** below.

The single biggest win is #2: turning the daily cleanup from "N applies" into "1 apply per panel."

---

## 1. Panel mental model (Hiddify v12)

- A **panel** has **admins** (in invoice_system these are "resellers"). The **API key *is* an
  admin's UUID**, passed as header `Hiddify-API-Key: <admin_uuid>`. The calling admin is identified
  by that key. bot-free already stores one `AdminUUID` per panel — every user it creates is
  attributed (`added_by_uuid`) to that admin. Good enough; no sub-admins needed.
- **Two different secret paths** (this trips people up):
  - **admin/API path** = `Panel.AdminProxyPath` → used for the REST API and the Flask-Admin UI.
  - **subscription (client) path** = `Panel.SubProxyPath` on `Panel.SubDomain` → used to build the
    link you hand the end-user. bot-free already separates these — keep using `SubLink(...)` for the
    user-facing link, and `AdminProxyPath` for all create/delete calls.
- **URLs:**
  ```
  apiBase    = https://<domain>/<AdminProxyPath>/api/v2/admin   ← REST (bot-free's base()+"/admin")
  panelRoot  = https://<domain>/<AdminProxyPath>                ← Flask-Admin UI (for bulk action)
  bulk list  = <panelRoot>/admin/user/                          ← GET (scrape CSRF + get session cookie)
  bulk action= <panelRoot>/admin/user/action/                  ← POST (the batch op)
  ```
  > bot-free's `base()` returns `…/api/v2`; its calls use `/admin/user/...`. The bulk endpoints are
  > **NOT** under `/api/v2` — they're the Flask-Admin web UI under `<panelRoot>/admin/...`. Add a
  > `panelRoot()` helper alongside `base()`.

---

## 2. The v2 REST API contract (single-user)

All relative to `apiBase`, header `Hiddify-API-Key: <admin_uuid>`, JSON.

| Op | Method · path | Body / notes |
|----|---------------|--------------|
| **Create** | `POST /user/` | `{uuid, name, usage_limit_GB(float), package_days(int), enable:true, comment?, mode?}` → returns the user `{uuid, id, ...}`. **Supply your own `uuid` (uuid4)** so you know the sub-link before the response. `added_by_uuid` is set server-side from the API key. `start_date` defaults to today. |
| **Get** | `GET /user/{uuid}/` | returns `{uuid, id, name, usage_limit_GB, current_usage_GB, enable, package_days, added_by_uuid, start_date, is_active, mode, last_online, ...}`. **404 = absent.** Use this to resolve the numeric **`id`** (needed for bulk) and to check existence. |
| **Edit / disable** | `PATCH /user/{uuid}/` | e.g. `{"enable": false}` or any subset of fields. |
| **Delete** | `DELETE /user/{uuid}/` | 404 → treat as already-gone (success). |

bot-free already implements Create/Get/Delete correctly. **Two fixes needed on the existing client:**

1. **Add the numeric id to the `User` struct** (the bulk action needs it):
   ```go
   type User struct {
       ID           int     `json:"id,omitempty"`   // ← ADD: Hiddify's internal rowid
       UUID         string  `json:"uuid,omitempty"`
       // ...existing fields...
   }
   ```
2. **Pass an explicit `uuid` on create** (you already generate one) and persist it immediately
   (see §5 idempotency).

---

## 3. The performance crux: `quick_apply_users`

Every **single-user** write through the v2 API — `POST /user/`, `PATCH /user/{uuid}/`,
`DELETE /user/{uuid}/` — makes Hiddify **recompile the whole proxy config and push it to the core
(xray)** server-side. That's `quick_apply_users`. It's **per call** and is **slow on busy panels**
(can take seconds; can 503 on 10k-user panels). So:

- Creating users one-by-one is unavoidable (no bulk-create), but **don't fire a burst unpaced**.
- **Deleting/disabling/enabling N users one-by-one = N applies = the thing to avoid.**

### The trick: Hiddify's native Flask-Admin bulk action = ONE apply per batch
The public v2 API has no bulk endpoint, but the panel's own web UI does. Hitting it does **one SQL
update + one `quick_apply_users` for the entire selected set**:

```
1) GET  <panelRoot>/admin/user/          (header Hiddify-API-Key, keep the session cookie)
        → scrape   name="csrf_token" value="..."   from the HTML
2) POST <panelRoot>/admin/user/action/   (same cookie jar + API-key header), form-encoded:
        csrf_token = <scraped>
        url        = <panelRoot>/admin/user/
        action     = "delete" | "disable" | "enable"
        rowid      = <numeric id>   (repeat the key once per user)
        → HTTP 302 on success (DO NOT auto-follow the redirect)
```

Confirmed actions: **`enable`, `disable`, `delete`** (operate on existing rows only — there is **no
bulk create**). `rowid` is the numeric **`id`**, *not* the uuid.

---

## 4. Numeric id resolution + cache

The bulk action needs rowids. **Do not download the whole user list** (`GET /user/` returns every
user — it 503s on large panels and invoice_system explicitly moved off it). Instead:

- Resolve **per target**: `GET /user/{uuid}/` → `.id`, with **bounded concurrency** (a semaphore of
  ~8 per panel). 404 → user already gone, skip it.
- **Cache `uuid → id` durably.** In bot-free the natural home is the `claims`/configs table: add a
  `panel_user_id INTEGER` column and fill it when you create the user (the create response echoes
  `id`) or the first time you resolve it. Next cleanup then needs **zero** lookups.

---

## 5. Robust single-user CREATE (avoid orphans)

bot-free's current claim flow does `CreateUser` → then `RecordClaim`. If the process dies between
them, the panel has a user with no DB row → an **orphan** that only the daily sweep might catch.
Fix with a **pending→confirmed (outbox) record** and a deterministic uuid:

```
1. uuid = uuid4()            // generated by US, not the panel
2. INSERT claim row (uuid, panel_id, tgid, status='pending')   // DB first, with OUR uuid
3. CreateUser(panel, {uuid, name, gb, days, enable:true})      // idempotent: same uuid retried is safe
4. UPDATE claim row SET panel_user_id = resp.id, status='active'
5. send the sub-link to the user
```

Because the uuid is ours and written first, a retry after a crash re-POSTs the **same** uuid (Hiddify
treats a duplicate-uuid create as the existing user / a no-op-ish create — verify with a `GET` first
if you want to be strict), and a `pending` row with no confirmation is trivially reconcilable
(GET by uuid: exists → confirm; 404 → re-create or drop). No orphans, no double-charge.

> If Hiddify rejects the create because the admin's `max_users` is reached, the panel returns
> **400/403** with a quota message. Detect it (substring match on `max`/`limit`/`quota`/`ظرفیت`/
> `حداکثر`) and surface a clean "panel full — try another" so `PickPanelForVolume` can pick the next
> panel instead of failing the claim.

---

## 6. Bulk DELETE for the daily cleanup (the main rewrite)

Today `scheduler.cleanupOldConfigs()` loops `DeleteUser` sequentially (N applies). Rewrite it to
**group expired configs by panel** and bulk-delete per panel:

```go
// pseudo-Go, fits internal/scheduler + internal/hiddify
olds := store.ConfigsBefore(today)                 // []Claim
byPanel := groupBy(olds, c => c.PanelID)

for panelID, rows := range byPanel {
    p   := store.GetPanel(panelID)
    cl  := clientFor(p)

    // 1. resolve numeric ids (use cached panel_user_id; else GET /user/{uuid}/ with sem=8)
    ids, missing := cl.ResolveUserIDs(ctx, uuidsOf(rows))   // 404 → missing (already gone)

    // 2. one bulk delete for the whole panel  ← ONE quick_apply instead of N
    if len(ids) > 0 {
        if err := cl.BulkUserAction(ctx, "delete", ids); err != nil {
            log.Printf("bulk delete panel %d: %v", panelID, err)   // keep DB rows → retry next run
            continue
        }
    }
    // 3. drop the local rows (deleted + already-missing)
    store.DeleteClaimRows(idsDone + missing)
}
```

For very large panels, **chunk** the rowids (e.g. 300–500 per bulk POST) so one action isn't huge;
that's still 1 apply per chunk, vastly fewer than per-user.

### The bulk helper (new code on the existing client)

```go
// internal/hiddify/hiddify.go

func (c *Client) panelRoot() string {
    return fmt.Sprintf("https://%s/%s", c.domain, c.proxyPath)
}

var csrfRe = regexp.MustCompile(`name=["']csrf_token["'][^>]*value=["']([^"']+)["']`)

// BulkUserAction runs Hiddify's native Flask-Admin action (enable|disable|delete) on numeric rowids
// in ONE request → one server-side quick_apply for the whole batch.
func (c *Client) BulkUserAction(ctx context.Context, action string, ids []int) error {
    if len(ids) == 0 {
        return nil
    }
    jar, _ := cookiejar.New(nil)                       // MUST keep the Flask session cookie GET→POST
    httpc := &http.Client{
        Timeout:       5 * time.Minute,                 // a big batch apply is slow
        Jar:           jar,
        CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, // 302 = ok
    }
    listURL := c.panelRoot() + "/admin/user/"
    actURL  := c.panelRoot() + "/admin/user/action/"

    // 1) GET the list page → CSRF token (+ session cookie into the jar)
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
    req.Header.Set("Hiddify-API-Key", c.apiKey)
    page, err := httpc.Do(req)
    if err != nil { return err }
    body, _ := io.ReadAll(io.LimitReader(page.Body, 8<<20)); page.Body.Close()
    m := csrfRe.FindSubmatch(body)
    if m == nil { return fmt.Errorf("no csrf token on bulk page (status %d)", page.StatusCode) }
    csrf := html.UnescapeString(string(m[1]))

    // 2) POST the action (form-encoded, repeated rowid)
    form := url.Values{}
    form.Set("csrf_token", csrf)
    form.Set("url", listURL)
    form.Set("action", action)
    for _, id := range ids { form.Add("rowid", strconv.Itoa(id)) }

    req2, _ := http.NewRequestWithContext(ctx, http.MethodPost, actURL, strings.NewReader(form.Encode()))
    req2.Header.Set("Hiddify-API-Key", c.apiKey)
    req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    resp, err := httpc.Do(req2)
    if err != nil { return err }
    defer resp.Body.Close()
    if resp.StatusCode >= 400 {                          // 200/302 = success
        b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
        return fmt.Errorf("bulk %s failed (HTTP %d): %s", action, resp.StatusCode, b)
    }
    return nil
}
```

> Notes: (a) a **cookie jar is required** — the CSRF token is bound to the Flask session cookie set
> by the GET. (b) Disable redirect-following so the 302 is treated as success. (c) The Hiddify-API-Key
> header authenticates the Flask-Admin UI too (same as the REST API).

---

## 7. Cross-cutting hardening (apply to the whole client)

These are the gaps the recon found; add them once on the client and everything benefits.

1. **Tuned HTTP transport** (the default pool is small for multi-panel bursts):
   ```go
   tr := &http.Transport{
       MaxIdleConns: 100, MaxIdleConnsPerHost: 10, MaxConnsPerHost: 10,
       IdleConnTimeout: 90 * time.Second, ForceAttemptHTTP2: true,
   }
   // create/get use ~30s; bulk uses ~5m (set per-request via context, not one global timeout)
   ```
2. **Retry with backoff + jitter** in `do()` for **idempotent** ops (GET, DELETE, and create-by-known-uuid):
   retry on network error / 502 / 503 / 504 / 429, 3–4 attempts, `200ms → 400 → 800 …` + jitter,
   honoring `ctx`. **Do not** blindly retry a non-idempotent POST unless it's the same client-supplied uuid.
3. **Per-panel serialization + pacing.** Never fire many writes at one panel in parallel (each
   quick_applies). Keep a `map[panelID]*sync.Mutex` (or a 1-worker channel per panel) so a panel's
   writes run one-at-a-time; for create bursts add a small gap (e.g. token bucket ~2–4 ops/sec/panel).
   Parallelism is fine **across** panels.
4. **429 / overload awareness.** If a panel returns 429 or repeated 503, back off that panel and let
   `PickPanelForVolume` route elsewhere; record a short "cooldown until" in memory.
5. **id cache** (see §4) — `panel_user_id` column, filled on create, reused by cleanup.
6. **Idempotent create / outbox** (see §5) — pending row first, our uuid, confirm after.

---

## 8. Gotchas (learned the hard way in production)

- **`core_type` must be `xray`, not `singbox`.** On a panel set to `singbox`, online-tracking and
  new-user apply silently misbehave. If creates "succeed" but users don't come online, check this
  on the panel first — it's a panel config issue, not your code.
- **admin vs sub path** — build the user link from `SubDomain`/`SubProxyPath`, never the admin path.
  bot-free already does this; don't regress it when refactoring.
- **404 on DELETE = success** (already gone). bot-free already handles this; keep it for bulk too
  (treat "missing" ids as done).
- **The v12 `admin_user` PATCH-500 bug**: `PATCH /api/v2/admin/admin_user/{uuid}/` *applies* the
  change but returns HTTP 500 (`name 'admins' is not defined`). Only relevant if you ever change
  admin limits (you probably won't in bot-free). If you do: on non-2xx, re-`GET` and accept the
  change if the fields took. **User** endpoints (`/user/...`) do **not** have this bug.
- **`usage_limit_GB` is a float, `package_days` an int** — match the JSON key casing exactly
  (`usage_limit_GB`, not `usage_limit_gb`) on writes.
- **Don't over-optimize creates into "bulk"** — there is no bulk create; the win there is
  robustness + pacing, not batching.

---

## 9. Suggested implementation order (for the other session)

1. Add `ID int json:"id"` to `hiddify.User`; add `panelRoot()` + `BulkUserAction(...)` +
   `ResolveUserIDs(...)` (sem=8, 404→missing) to `internal/hiddify/hiddify.go`. Add `panel_user_id`
   to the configs/claims table + store it on create.
2. Rewrite `scheduler.cleanupOldConfigs()` to group-by-panel + bulk delete (§6). **Biggest win.**
3. Add retry/backoff + tuned transport to the client `do()` (§7.1–7.2).
4. Make the claim/create flow idempotent (pending→confirmed, our uuid, §5) and add the `max_users`
   "panel full → next panel" path.
5. Add per-panel serialization/pacing + 429 cooldown (§7.3–7.4).

## 10. Testing / safety

- **Test only against a disposable admin/panel**, never a customer's live admin. In invoice_system
  the rule is "only test on the reseller named `ali`." Do the same here: a throwaway panel admin.
- Verify the bulk delete really does **one** apply: create ~5 users, expire them, run cleanup, and
  confirm a single `/admin/user/action/` POST removed all of them (check the panel UI).
- Load-test pacing on a non-prod panel before pointing it at busy ones — an unpaced create/delete
  burst can overwhelm a panel (its proxy keeps recompiling). Pacing > raw speed.

---

### Reference: the proven Python implementation (read-only, for exact shapes)
`invoice_system/backend/app/services/panel_client/admin_api.py` — `create_user`, `get_user_id`,
`_user_bulk_action` (the CSRF+POST flow this guide ports to Go), `bulk_delete_users`. The
enforcement queue (`…/services/enforcement.py`) is the reference for per-panel parallelism, chunking,
resumable progress, and bounded retries if you ever need that scale.
