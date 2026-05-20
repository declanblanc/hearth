# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository status

This repository is **pre-code**. It currently contains only planning documents — no source, build system, tests, or deployment config exists yet. Treat the two markdown files as the authoritative spec:

- [Hearth_Technical_Plan.md](Hearth_Technical_Plan.md) — stack decisions, data model, key flows, anti-features, decision log.
- [Hearth_Build_and_Test_Plan.md](Hearth_Build_and_Test_Plan.md) — phase-by-phase work breakdown with acceptance criteria and test plans.

When asked to scaffold the project, follow the decisions in the Technical Plan's "Decision log" (§9) and the structure of "Phase 0 — Foundation" in the Build & Test Plan. Don't relitigate stack choices unless the user asks.

## Product in one paragraph

Hearth is a private, invite-only, donation-funded social app for close connections. Hard constraints shape every decision: **<$15/month hosting for ~1,000 users**, **privacy enforced at the data layer**. Many social-architecture assumptions don't apply — no fanout, no ranking, no discovery, no celebrities. It's closer to a private group blog with permissions than to Twitter.

## Planned stack (from Technical Plan §2, §9)

- **Backend:** Go (single static binary, low memory footprint).
- **Frontend:** htmx + server-rendered HTML. No build pipeline, no SPA framework.
- **Database:** SQLite with Litestream streaming WAL backups to R2. Use `BEGIN IMMEDIATE` for write transactions.
- **Media:** Cloudflare R2, accessed only via short-lived signed URLs generated server-side after a connection check.
- **Email:** Resend.
- **Hosting:** Fly.io with a persistent volume for the SQLite file.
- **Auth:** email+password+verification, argon2id, server-side sessions via HttpOnly/Secure/SameSite=Lax cookies. No JWTs.

Specific Go libraries (router, templating, SQLite driver, sessions) are intentionally deferred — pick boring/well-documented options when the user asks for them.

## Architectural rules that are easy to violate

These are load-bearing — many features in the plan assume them:

1. **Privacy is a data-layer check, not a UI affordance.** Every endpoint returning another user's content must call a single `IsConnected(viewer_id, author_id) bool` helper. Negative paths must be tested. `/{username}` returns 404 to non-connected viewers (not 403 — non-existence is part of the privacy model).
2. **No counts, anywhere.** The product is partly defined by what it doesn't show. Connection list returns names, not a count. Comment thread returns comments, never a `count`. Unread notifications surface as a **dot**, not a number. No `*_count` columns, no `likes` table, no aggregates.
3. **Connections are stored as `(min_id, max_id)`** in `connections.user_a_id`/`user_b_id` to make the pair unique. Don't insert both directions.
4. **The feed's new/old split is driven by `users.last_feed_loaded_at`.** Update it **after** running the feed query so the same load doesn't reclassify its own results (Technical Plan §4.1, step ordering matters).
5. **Invite tokens are consumed when the connection request is submitted, not when `/i/{token}` is opened.** A `pending_invite` cookie carries the token across the signup/email-verification round-trip, because verification typically happens in a different browser session.
6. **Media access control is per-request.** Signed R2 URLs are generated at render time, only after verifying the viewer is connected to the post author. URLs expire in ~1 hour. Object keys must not embed `user_id` or `post_id`.
7. **Rate limits** (20 invites / 7d, 10 accepted connections / 7d) are enforced inside the write transaction with `BEGIN IMMEDIATE` to avoid boundary races.
8. **Notifications fan out in two steps:** always insert into `notifications` (in-app is universal); send email only if the recipient's `notification_preferences` flag for that category is true. All preference flags default to `false`.
9. **Post edits snapshot the _old_ content** into `post_edits` before overwriting `posts.content`. The current version always lives on `posts`.
10. **Delete vs. archive:** archive sets `status='archived'` and preserves rows; delete sets `status='deleted'`, nulls content, removes R2 media, and deletes `post_edits` rows.

## Anti-features (do not build)

No likes/reactions, no user search/discovery, no algorithmic ranking, no messaging, no visible counts, no public profiles. Enforced by _not having the tables, endpoints, or UI_ — not by hiding things in the frontend.

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

None yet — no code, no `go.mod`, no Makefile, no scripts. When scaffolding begins, add the actual commands here (build, test, run a single test, lint, migrate, dev server) rather than guessing them.
