# CLAUDE.md
@AGENTS.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository status

The project is **under active development**. The Go backend is scaffolded and the core social loop is implemented; work is progressing through the phases below. The two planning documents remain the authoritative spec for intent and architecture — keep them and the code in sync:

- [Hearth_Technical_Plan.md](Hearth_Technical_Plan.md) — stack decisions, data model, key flows, anti-features, decision log.
- [Hearth_Build_and_Test_Plan.md](Hearth_Build_and_Test_Plan.md) — phase-by-phase work breakdown with acceptance criteria and test plans.

When the plans and the code disagree, treat it as a bug in one of them: fix the code to match the spec, or update the spec if the decision has genuinely changed (and note it in the decision log). Don't relitigate settled stack choices unless the user asks.

### Code layout

- `cmd/server/` — entrypoint (`main.go` wires config, DB, router via `http.NewServeMux`; `seed.go` for dev data).
- `internal/<domain>/` — one package per feature area: `auth`, `connections`, `feed`, `posts`, `profiles`, `notifications`, `media`. Each typically has `handlers.go` (HTTP), `service.go` (business logic), and `*_test.go`.
- `internal/shared/` — cross-cutting infrastructure: `config`, `db` (connection + embedded goose migrations), `email`, `middleware`, `render` (html/template wrapper).
- `internal/shared/db/migrations/` — numbered goose SQL migrations (`NNN_name.sql`), embedded via `//go:embed` and applied on startup.
- `web/templates/` — server-rendered `html/template` files. `web/static/` — CSS/JS (htmx, no build step).
- `tests/integration/` and `tests/e2e/` — cross-package and Playwright tests.

## Product in one paragraph

Hearth is a private, invite-only, donation-funded social app for close connections. Hard constraints shape every decision: **<$15/month hosting for ~1,000 users**, **privacy enforced at the data layer**. Many social-architecture assumptions don't apply — no fanout, no ranking, no discovery, no celebrities. It's closer to a private group blog with permissions than to Twitter.

## Stack (from Technical Plan §2, §9)

- **Backend:** Go (single static binary, low memory footprint).
- **Frontend:** htmx + server-rendered HTML. No build pipeline, no SPA framework.
- **Database:** SQLite with Litestream streaming WAL backups to R2. Use `BEGIN IMMEDIATE` for write transactions.
- **Media:** Cloudflare R2, accessed only via short-lived signed URLs generated server-side after a connection check.
- **Email:** Resend.
- **Hosting:** Fly.io with a persistent volume for the SQLite file.
- **Auth:** email+password+verification, argon2id, server-side sessions via HttpOnly/Secure/SameSite=Lax cookies. No JWTs.

Concrete library choices (kept deliberately boring — prefer stdlib, add deps only when they earn their place):

- **Router:** stdlib `net/http.NewServeMux` (Go 1.22+ method+path patterns). No third-party router.
- **Templating:** stdlib `html/template`, wrapped by `internal/shared/render`.
- **SQLite driver:** `modernc.org/sqlite` (pure Go, so `CGO_ENABLED=0` static binaries work).
- **Migrations:** `pressly/goose`, embedded and run at startup.
- **Sessions:** hand-rolled server-side sessions in `internal/auth` (no session library, no JWTs).
- **Password hashing:** `golang.org/x/crypto` (argon2id).
- **R2/object storage:** `aws-sdk-go-v2` S3 client (R2 is S3-compatible).
- **Logging:** stdlib `log/slog` with `lmittmann/tint` for readable dev output.

## Architectural rules that are easy to violate

These are load-bearing — many features in the plan assume them:

1. **Privacy is a data-layer check, not a UI affordance.** Every endpoint returning another user's content must call a single `IsConnected(viewer_id, author_id) bool` helper. Negative paths must be tested. `/{username}` returns 404 to non-connected viewers (not 403 — non-existence is part of the privacy model).
2. **No counts, anywhere.** The product is partly defined by what it doesn't show. Connection list returns names, not a count. Comment thread returns comments, never a `count`. Unread notifications surface as a **dot**, not a number. No `*_count` columns, no aggregates.
3. **Connections are stored as `(min_id, max_id)`** in `connections.user_a_id`/`user_b_id` to make the pair unique. Don't insert both directions.
4. **The feed's new/old split is driven by `users.last_feed_loaded_at`.** Update it **after** running the feed query so the same load doesn't reclassify its own results (Technical Plan §4.1, step ordering matters).
5. **Invite tokens are consumed when the invitee accepts the invitation (`POST /i/{token}/accept`), not when `/i/{token}` is opened.** A `pending_invite` cookie carries the token across the signup/email-verification round-trip, because verification typically happens in a different browser session. Accepting an invite creates a pending `connection_requests` row; the inviter then **confirms or declines it from the top of the connections page** (`POST /connections/{id}/confirm` / `POST /connections/{id}/decline`). There is no separate requests page — "request" is avoided in the UI as it doesn't fit the mutual connection model, though the `connection_requests` table keeps the name internally.
6. **Media access control is per-request.** Signed R2 URLs are generated at render time, only after verifying the viewer is connected to the post author. URLs expire in ~1 hour. Object keys must not embed `user_id` or `post_id`.
7. **Rate limits** (20 invites / 7d, 10 accepted connections / 7d) are enforced inside the write transaction with `BEGIN IMMEDIATE` to avoid boundary races.
8. **Notifications fan out in two steps:** always insert into `notifications` (in-app is universal); send email only if the recipient's `notification_preferences` flag for that category is true. All preference flags default to `false`.
9. **Post edits snapshot the _old_ content** into `post_edits` before overwriting `posts.content`. The current version always lives on `posts`.
10. **Delete vs. archive:** archive sets `status='archived'` and preserves rows; delete sets `status='deleted'`, nulls content, removes R2 media, and deletes `post_edits` and `comments` rows.
11. **There is no like feature.** Likes were removed entirely (see decision log): no liking posts, no liker lists, no `like_on_post` notification, no `likes` table. Don't reintroduce any of it — there is deliberately no reaction or "favorite" affordance of any kind.

## Anti-features (do not build)

No likes/reactions of any kind, no user search/discovery, no algorithmic ranking, no messaging, no visible counts, no public profiles. Enforced by _not having the tables, endpoints, or UI_ — not by hiding things in the frontend. (Likes were removed wholesale — see rule #11 and the decision log.)

## Conventions called out in the Build & Test Plan

- Form errors: return 422 with the form re-rendered and inline field errors (htmx-friendly partial responses).
- Unexpected errors: 500 with a generic styled page. Never leak stack traces.
- Structured JSON logs to stdout (Fly captures them). Log all 4xx/5xx with method, path, user_id, request_id.
- Testing: stdlib `testing` + `testify`; `net/http/httptest` for handlers; in-memory SQLite for unit/integration, file-based for migration tests; Playwright for E2E; stub email in tests.
- Defaults: username `[a-z0-9_]` 3–30 chars (immutable), display name 1–50, bio ≤1000, pronouns ≤50, password ≥12 chars argon2id, sessions 30d sliding, email verify 24h, password reset 1h, soft-delete retained 30d.

## Build phases (gates, not estimates)

- **Phase 0:** signup → verify → login → profile edit → account deletion, end-to-end in prod.
- **Phase 1:** two users connect via invite, post text, see each other's posts with new/old split, in-app notifications for connection events.
- **Phase 2:** images, threaded comments, edit/archive/delete — feature-complete MVP.
- **Phase 3:** installable PWA, email notifications for opted-in users, abuse mitigations.

## Commands

```sh
make run          # dev server on :8080 (email printed to stdout, no R2 needed)
make build        # compile to bin/hearth (CGO_ENABLED=0)
make test         # go test ./...
make fmt          # gofmt -w .
make vet          # go vet ./...
make tidy         # go mod tidy
```

Run a single test package:
```sh
go test ./internal/auth/...
go test ./internal/shared/db/...
```

Run a single test by name:
```sh
go test ./internal/auth/... -run TestAuthenticate
```

Deploy to Fly.io:
```sh
flyctl deploy            # builds and deploys; migrations run at startup
flyctl logs              # tail production logs
flyctl secrets set KEY=VALUE   # add/update an env secret
```
