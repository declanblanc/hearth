# Hearth — Build & Test Plan (v0.1)

This document expands the phased build plan from the **Hearth Technical Plan & Specifications (v0.3)** into a detailed work breakdown suitable for handoff to a developer or development team. Each phase is broken into discrete work items with implementation notes, acceptance criteria, and a paired testing plan (automated + manual).

This document does **not** replace the Technical Plan — it assumes familiarity with the data model, key flows, anti-features, and stack decisions documented there. Refer back to that document for context on any architectural choice.

There are no time estimates. Phases are sequential — each delivers a coherent state of the product that the prior phase did not. Within a phase, work items can often be parallelized.

---

## Phase gates

Each phase ends when its gate criteria are demonstrably true on the deployed environment.

- **Phase 0 → 1 gate:** a user can sign up, verify email, log in, edit their profile, and delete their account. All flows work end-to-end in production.
- **Phase 1 → 2 gate:** two users can connect via an invite link, post text content, see each other's posts in their feeds (with the new/old split), and receive in-app notifications for connection events.
- **Phase 2 → 3 gate:** posts support images and comments; edit, archive, and delete all work. The product is feature-complete for MVP scope.
- **Phase 3 → launch gate:** PWA installable on iOS and Android; email notifications working for opted-in users; abuse mitigations in place.

---

## Cross-cutting conventions

These apply throughout the build. Documenting once to avoid repetition.

### Recommended implementation defaults

These are not specified in the Technical Plan but a developer will need numbers. Treat as defaults; tune as needed.

| Setting                                  | Default                                                                              |
| ---------------------------------------- | ------------------------------------------------------------------------------------ |
| Username                                 | 3–30 chars, `[a-z0-9_]`, lowercase, unique, immutable                                |
| Display name                             | 1–50 chars                                                                           |
| Bio                                      | 0–1000 chars                                                                         |
| Pronouns                                 | 0–50 chars                                                                           |
| Password                                 | minimum 12 chars, hashed with argon2id (no other complexity rules per NIST guidance) |
| Session lifetime                         | 30 days, refreshed on activity                                                       |
| Email verification token lifetime        | 24 hours                                                                             |
| Password reset token lifetime            | 1 hour                                                                               |
| Soft-delete retention before hard delete | 30 days                                                                              |

### Logging and errors

- Structured JSON logs to stdout; Fly.io captures these automatically.
- Form errors: return 422 with the form re-rendered and field-level error messages inline (htmx-friendly).
- Unexpected errors: return 500 with a generic styled page. Never leak stack traces.
- All 4xx/5xx responses logged server-side with request context (method, path, user_id if any, request_id).

### Authentication

- All authenticated routes check the session cookie via middleware; unauthenticated requests redirect to `/login?next=<original-url>`.
- Cookies: HttpOnly, Secure, SameSite=Lax.

### Privacy enforcement

Every endpoint that returns content authored by another user must verify the requesting user has an active connection to that author. This check lives in one helper — `IsConnected(viewer_id, author_id) bool` — and its negative paths must be covered by tests.

### Transactions

Any write that touches multiple tables runs in a single transaction. Use `BEGIN IMMEDIATE` in SQLite for write transactions to avoid the lock-upgrade footgun.

### Testing tools

- Go test framework: standard library + `testify` for assertions.
- HTTP handler tests: `net/http/httptest`.
- DB tests: in-memory SQLite for unit/integration, file-based SQLite for migration tests.
- Browser/end-to-end tests: Playwright (TypeScript or Python — team's call).
- Email tests: a local stub in test runs (capture & assert calls); Resend's sandbox mode for staging.

---

# Phase 0 — Foundation

## Goal

Establish the deployment pipeline, the database, and the authentication system. At the end of Phase 0, users can create accounts, manage them, and edit profiles, but the product has no social features.

## 0.1 Project skeleton and deployment

### Implementation

- Initialize Go module; suggested directory structure in the appendix.
- Configuration via environment variables, loaded once at startup into a typed `Config` struct.
- Dockerfile producing a minimal image (multi-stage build, distroless or `scratch` base).
- Fly.io app, persistent volume mounted at `/data` for SQLite + Litestream, secrets configured.
- Cloudflare R2 bucket created; access key with scoped permissions.
- Resend account; API key in Fly secrets.
- Domain configured; TLS via Fly.io automatic certificates.
- CI pipeline runs on every PR: `gofmt -l`, `go vet`, `go test ./...`, linter (`staticcheck` or `golangci-lint`).

### Acceptance criteria

- `git push` to `main` triggers a deploy.
- `GET https://hearth.app/healthz` returns 200.
- No secrets in the repository; all sensitive values from env.

### Testing plan

**Automated**

- CI runs lint, vet, and tests on every PR; merges blocked on red CI.
- Smoke test post-deploy: `GET /healthz` returns 200.

**Manual**

- Deploy a trivial change end-to-end (branch → PR → merge → deploy → verify in prod).
- Confirm Fly.io TLS cert auto-renewal is configured.
- Confirm R2 bucket credentials work via a one-off upload script.

## 0.2 Database and migrations

### Implementation

- Migration tool: recommend `goose` for ergonomics with Go.
- Migration 001: `users` table per Technical Plan §3.
- Migration 002: `sessions` table.
- Migration 003: `email_verifications` and `password_resets` tables (each has columns: token, user_id, expires_at, consumed_at).
- Migrations run on application startup.
- Application enables `PRAGMA journal_mode = WAL` and `PRAGMA foreign_keys = ON` at boot.
- Litestream configured to stream the WAL to the R2 bucket.
- DB connection pool sized small (5 max connections is plenty for SQLite).

### Acceptance criteria

- Fresh deploy from empty DB results in a fully-migrated schema.
- Litestream backups visible in R2 within a minute of any write.
- Disaster recovery has been performed at least once (manually) and verified to work.

### Testing plan

**Automated**

- Test that runs all migrations against a fresh in-memory DB and asserts expected tables/columns/indexes exist.
- Per-migration rollback test: apply, roll back, confirm DB state matches pre-application.

**Manual**

- Force a write, watch Litestream upload WAL segments to R2.
- Simulate disaster recovery: spin up a new instance, restore from Litestream, confirm the test write is present.

## 0.3 Authentication: signup

### Implementation

- `POST /signup` accepts `{ username, email, password, display_name }`.
- Validation per the defaults table above. Username normalized to lowercase.
- On success: insert user with `email_verified_at = NULL`, create row in `email_verifications` with random token, send verification email via Resend.
- `GET /verify?token=...`: look up token, ensure unexpired and unconsumed, set `users.email_verified_at = NOW()`, mark token consumed, redirect to `/` (or to pending invite if cookie is set).
- Unverified users can log in but the feed displays a "verify your email" banner; posting, inviting, and accepting connections are blocked.
- "Resend verification email" link available on the banner; rate-limited (max 3/hour).

### Acceptance criteria

- New users receive verification email within ~10 seconds.
- Duplicate username and duplicate email both return clear inline errors (no leaked info beyond what the request already provided).
- Each verification token works exactly once.
- Expired tokens prompt a resend.

### Testing plan

**Automated**

- Unit tests for input validation (each rule, both pass and fail cases).
- Integration test: full signup flow with stubbed email; assert exactly one email queued with the right token.
- Integration test: duplicate email/username returns 422 with field-level error.
- Integration test: verifying a token twice — second attempt returns "token not found / already used."
- Integration test: expired token returns expected error and resend option.

**Manual**

- Sign up with a real email; confirm delivery to inbox (not spam) in Gmail and a non-Gmail provider.
- Try malformed inputs (empty fields, oversize bio, invalid email shapes); confirm UI errors are clear.
- Sign up flow on mobile Safari and mobile Chrome.

## 0.4 Authentication: login, logout, sessions

### Implementation

- `POST /login` accepts `{ email, password }`.
- Both invalid email and invalid password produce **identical** responses (no user enumeration).
- On success: insert `sessions` row, set cookie.
- `POST /logout` deletes the session row and clears the cookie.
- Middleware loads `User` from session cookie on every request, attaches to request context. Missing/invalid cookie → context user is nil.
- Failed login attempts per IP: 10 per 15 minutes, then block with 429.

### Acceptance criteria

- Wrong password and unknown email produce indistinguishable responses.
- Successful login redirects to `next` param if present, otherwise to `/`.
- Logout invalidates session immediately.
- Session cookie is not readable from client JS.

### Testing plan

**Automated**

- Unit test for password hash verification (correct, incorrect, malformed hash).
- Integration test: login → access protected route → logout → access protected route should redirect.
- Integration test: wrong password and wrong email return identical body and headers.
- Test: session past `expires_at` is rejected.
- Test: 11th failed login attempt from one IP within window returns 429.

**Manual**

- Log in from two browsers; confirm both sessions work independently.
- Log in, manually clear cookies, confirm redirect to login on next request.

## 0.5 Authentication: password reset

### Implementation

- `POST /password/forgot` accepts `{ email }`. Always returns success.
- If user exists: insert `password_resets` row, send email with reset link.
- `GET /password/reset?token=...` shows reset form.
- `POST /password/reset` accepts `{ token, new_password }`, updates password, deletes **all** sessions for that user, marks token consumed.
- Per-email and per-IP rate limit: 3 reset requests per hour each.

### Acceptance criteria

- Reset email arrives within seconds.
- Token works exactly once.
- After reset, all existing sessions for the user are terminated.

### Testing plan

**Automated**

- Integration test: full reset flow happy path.
- Test: forgot-password for unknown email returns success but no email queued.
- Test: token expiry enforced.
- Test: post-reset, an old session cookie is rejected.
- Test: rate limit triggers.

**Manual**

- Run the flow with a real email; confirm clarity of UI copy at every step.

## 0.6 Profile view and edit

### Implementation

- `GET /settings/profile`: form to edit display name, bio, pronouns, and profile photo.
- `POST /settings/profile`: saves changes.
- Photo upload: client-side compression (same library and settings as post media — see 2.1), upload to R2 with key like `profile/{user_id}/{uuid}.jpg`, store key in `users.photo_key`. Old photo deleted from R2 on replacement.
- `GET /u/{username}` for own profile shows profile + an "edit profile" link. Other users' profiles handled in Phase 1.

### Acceptance criteria

- Photo upload accepts JPEG, PNG, WebP, HEIC (HEIC converted client-side).
- Server enforces a 5MB max post-compression.
- Photo previewed in the form before save.

### Testing plan

**Automated**

- Integration test: profile update persists across requests.
- Test: oversized images rejected.
- Test: non-image mime types rejected via mime sniff (not just by extension).
- Test: old photo is deleted from R2 when replaced.

**Manual**

- Upload from iPhone camera roll (HEIC), Android gallery (JPEG), desktop (PNG).
- Test extreme inputs: very long bio, emoji, RTL text, mixed scripts.
- Verify photo persists across logout/login.

## 0.7 Account deletion

### Implementation

- `POST /settings/account/delete` requires password confirmation.
- Soft delete: set `users.deleted_at = NOW()`, replace email with `deleted-{uuid}@example.invalid`, null all profile fields (display_name → `[deleted user]`, bio → null, pronouns → null, photo_key → null and old photo removed from R2).
- All sessions for the user deleted immediately.
- Background sweep: hard-delete users where `deleted_at < NOW() - 30 days`. On hard delete: delete posts, post_edits, media from R2, comments authored by the user, connections referencing the user.
- Recommend running the sweep as a cron-style hourly job (Fly machines have a scheduling primitive for this).

### Acceptance criteria

- Soft-deleted users cannot log in (response indistinguishable from "no such user").
- Soft-deleted users do not appear in any connected user's connection list.
- Profile route for a soft-deleted user returns 404.
- After 30 days, the user row and all associated content are gone from the DB and R2.

### Testing plan

**Automated**

- Integration test: soft-delete renders the account inaccessible.
- Test: hard-delete sweep finds expired soft-deleted users and removes them.
- Test: hard-delete cascades to posts, post_edits, comments, media.
- Test: connection rows referencing the deleted user are gone.

**Manual**

- Soft-delete an account; from a connected account, verify they vanish.
- Verify R2 media is actually removed (inspect the bucket).
- Run the sweep manually against a record older than 30 days; confirm clean removal.

---

# Phase 1 — Core social loop

## Goal

Two users can connect via invite, post text content, and see each other's posts in chronological feeds with the new/old split. In-app notifications work for connection events. By the end of Phase 1, the product is usable for a friends-and-family beta.

## 1.1 Invites

### Implementation

- `POST /invites` (authenticated, verified): inserts `invites` row with random 32-byte token (URL-safe base64, no padding), 72-hour expiry. Returns `https://hearth.app/i/{token}`.
- Rate limit: 20 invites in trailing 7 days. 21st returns 429 with the date/time the limit will lift.
- `GET /settings/invites`: lists the user's invites (active and expired/consumed), with copy-to-clipboard buttons for active ones.

### Acceptance criteria

- Tokens are 32 bytes of cryptographic randomness; not guessable.
- Rolling 7-day window correctly counts only invites within the window.
- Expired invites show as expired; consumed ones show as used.

### Testing plan

**Automated**

- Integration test: invite creation, link format, persistence.
- Test: rate limit enforced at exactly 20 (the 20th succeeds, the 21st fails).
- Test: rolling window — generate 20 invites 7 days and 1 second ago + 1 today succeeds (the old ones fall out of the window).
- Test: token entropy sanity check (statistical).

**Manual**

- Generate an invite, open the link in a private window, confirm it works.
- Generate 20 invites quickly, confirm 21st is blocked with a useful message.

## 1.2 Invite opening flow (the critical path)

This is the most user-facing fragile path in the product. Test it thoroughly.

### Implementation

Implements Technical Plan §4.2 step 3 exactly. The handler is a state machine over (token state) × (auth state):

| Token state                    | Auth state                   | Result                                                                        |
| ------------------------------ | ---------------------------- | ----------------------------------------------------------------------------- |
| Not found / expired / consumed | any                          | "This invite is no longer valid" page                                         |
| Valid                          | not signed in                | Set `pending_invite` cookie, redirect to `/welcome?invite={token}`            |
| Valid                          | signed in as sender          | "You can't connect to yourself" error                                         |
| Valid                          | signed in, already connected | "You're already connected with {name}" page                                   |
| Valid                          | signed in, not connected     | "Request to connect with {name}" page (shows sender's display name and photo) |

- `pending_invite` cookie: signed (HMAC), 30-minute expiry, contains the token.
- `/welcome?invite={token}`: two buttons — "I have an account → Log in" and "I'm new → Sign up". Both targets carry the invite param forward.
- After login or signup completion (including email verification), the auth handler checks the cookie; if present and valid, redirects to `/i/{token}` instead of `/`.

### Acceptance criteria

- Every state combination above is handled.
- A user who clicks an invite link from email, signs up, verifies via a different browser, and logs back in lands on the invite page with the original sender's info intact.

### Testing plan

**Automated**

- Unit test of the state-machine resolver: every (token state, auth state) combination produces the expected outcome.
- Integration test: full unauthenticated → signup → email verify → invite page flow, with the email verification opened in a fresh client (simulating the different-browser case).
- Test: invite-to-self correctly rejected.
- Test: invite when already connected shows the right page.
- Test: invite link survives login flow when the user already has an account.
- Test: `pending_invite` cookie expires after 30 minutes (forge a stale signed cookie, confirm it's ignored).
- Test: invalid HMAC on `pending_invite` cookie causes it to be ignored.

**Manual (high priority)**

- Open the invite link in a private window, sign up, click the verification link in a different browser tab as if from email, return to the original window or log in fresh, and confirm landing on the correct invite page. This must be smooth.
- Repeat with an existing-user flow (login instead of signup).
- Try on mobile, where the email link opens in the system browser (which may or may not have the cookie).

## 1.3 Accept-to-connect

Accepting an invite link **connects the two users immediately** — there is no confirm step. The inviter is simply notified that their invitation was accepted. This replaced the original three-step handshake (accept → inviter confirms), which confused new users who expected to be connected the moment they accepted. The word "request" is still avoided in the UI; the `connection_requests` table name is kept internally as an audit trail only.

### Implementation

- `POST /i/{token}/accept` (authenticated, verified, must have passed the invite handler's gate): in one `BEGIN IMMEDIATE` transaction — claim one of the link's uses, insert the canonical `connections` row (`min(user_a, user_b), max(...)`), insert an audit `connection_requests` row (`status='accepted'`), and insert a `connection_accepted` notification for the **sender** (the inviter) with the accepter as `actor_id`. The accepter sees a "you're now connected with {name}" page.
- No rate limit on accepting: a user can connect via as many invite links as they receive. Only invite *creation* is rate-limited (§1.1).
- `GET /connections`: lists established connections plus the always-present invite-generation control. No pending section, no confirm/decline, no "waiting to connect" list, no Connections-tab pending dot.
- The inviter's notification names the accepter and links to their profile (`/u/{username}`); if the link was used by someone unintended, the inviter follows it and disconnects.

### Acceptance criteria

- Connections always stored with canonical (lower_id, higher_id) ordering.
- Accepting connects immediately; the inviter is notified, the accepter is not.
- Accepting is not rate-limited.
- A repeat accept by the same person is an idempotent no-op (no duplicate connection, notification, or slot claim).

### Testing plan

**Automated**

- Integration test: accept invite → produces a canonically-ordered connections row and a `connection_accepted` notification for the sender with the accepter as `actor_id`.
- Test: accepting is not rate-limited — a user already holding many recent connections can still accept and connect.
- Test: a single link can be accepted up to its multi-use cap; the next accept is rejected as exhausted.
- Test: a second accept by the same person reports already-connected and changes nothing.
- Test: accepting your own invite is rejected.

**Manual**

- Two-user test: A invites B, B accepts the link; B immediately sees a "you're now connected" page, A sees a "{B} accepted your invitation" notification, and both see each other on their profiles.
- From that notification, A clicks B's name → B's profile → Disconnect, and the connection is removed for both.

## 1.4 Disconnect

### Implementation

- `POST /connections/{user_id}/disconnect`: delete the `connections` row (or rows, defensively).
- No notification to the other user.

### Acceptance criteria

- After disconnect, neither user appears in the other's connection list.
- After disconnect, neither user's posts appear in the other's feed.
- After disconnect, `/{ex-username}` returns 404 to either user.

### Testing plan

**Automated**

- Integration test: disconnect removes the row.
- Test: post-disconnect feed query for either side does not return the other's posts.
- Test: post-disconnect profile route returns 404.

**Manual**

- Two-user disconnect; verify clean removal across feed, profile, and connection list.

## 1.5 Posts (text only)

### Implementation

- `POST /posts` accepts `{ content }`, max 1000 chars after trimming. Reject empty.
- `GET /u/{username}` (when connected): paginated list of the user's active posts, reverse chronological.
- `DELETE /posts/{id}` (author only): set `status = 'deleted'`, null content, clear `post_edits` (none yet in Phase 1), delete associated media (none yet in Phase 1).

### Acceptance criteria

- Empty post rejected with a clear error.
- 1001-char post rejected.
- Deleted posts return 404 even to previously-connected viewers.
- Content stored as-is — no smart-quotes conversion, no entity escaping at storage (escape at render time).

### Testing plan

**Automated**

- Integration test: create, read, delete.
- Test: content length validation at boundaries (0, 1, 1000, 1001).
- Test: non-author cannot delete (403).
- Test: deleted post excluded from feed and profile.

**Manual**

- Post emoji, RTL text, mixed scripts, code blocks, very long content.
- Verify rendering matches input exactly.

## 1.6 Feed

### Implementation

Implements Technical Plan §4.1 exactly.

- `GET /` (authenticated, verified):
  1. Fetch connection user IDs for the requesting user.
  2. `SELECT * FROM posts WHERE author_id IN (...) AND status = 'active' ORDER BY created_at DESC LIMIT 50`.
  3. In the view layer, split results into "new" (created_at > `last_feed_loaded_at`) and "old" (created_at <= `last_feed_loaded_at`).
  4. After query, `UPDATE users SET last_feed_loaded_at = NOW() WHERE id = ?`.
- "Show older posts" toggle reveals the old group below the new group.
- "Load older posts" button paginates via `?before=<post_id>` cursor for older history.

### Acceptance criteria

- The same load does not re-classify its own results (update happens after the query, see Tech Plan note).
- The split correctly reflects the timestamp captured at query time.
- Pagination is stable (no duplicates, no skips) even if new posts arrive between page loads.

### Testing plan

**Automated**

- Integration test: feed returns only posts from connected users with `status = 'active'`.
- Test: the new/old split correctly reflects `last_feed_loaded_at` at the moment of the query.
- Test: `last_feed_loaded_at` is updated after the query, not before (set up a scenario where this would matter and assert the result).
- Test: pagination cursor returns the expected next page.
- Test: archived and deleted posts do not appear.

**Manual**

- Multi-device test: load feed on phone, then on laptop. Posts seen on phone appear in "old" on laptop (since `last_feed_loaded_at` is global per user).
- Set up several connections, have them post, verify chronological ordering.
- Visit feed, scroll, leave, come back; new posts since last visit appear in the new section.

## 1.7 Notifications (in-app, Phase 1 scope)

### Implementation

- `notifications` table per Technical Plan §3.
- Helper: `CreateNotification(user_id, type, actor_id?, post_id?, comment_id?)` — used by the rest of the codebase.
- Types in Phase 1 scope: `connection_accepted` (sent to the inviter when their invite is accepted). The `connection_request` type is retained in the schema's CHECK constraint as a legacy value but is no longer produced.
- `GET /notifications`: list newest first.
- On `GET /notifications`: `UPDATE notifications SET read_at = NOW() WHERE user_id = ? AND read_at IS NULL`.
- Header indicator: a small dot if any unread notifications, nothing otherwise. **No numeric count anywhere.**

### Acceptance criteria

- Invite accept inserts a `connection_accepted` notification for the inviter (the sender), with the accepter as `actor_id`.
- Dot present iff any unread exist.
- Visiting `/notifications` clears the unread state for all of the user's notifications.

### Testing plan

**Automated**

- Integration test: invite accept creates the right notification for the inviter with the right metadata.
- Test: visiting `/notifications` clears `read_at`.
- Test: header indicator helper returns the right state for various unread counts.

**Manual**

- Two-user flow: A invites, B accepts; A sees the dot, opens `/notifications`, sees "{B} accepted your invitation", dot clears.
- Visit `/notifications` with none present; verify the empty state copy.

---

# Phase 2 — Richer content

## Goal

Posts get richer: images, threaded comments, edit history, archive/delete. End of Phase 2: feature-complete for MVP, missing only polish.

## 2.1 Image uploads

### Implementation

- Client: `browser-image-compression` library — max 1600px long edge, 80% JPEG quality. Run compression in a Web Worker to avoid jank.
- Accepted input formats: JPEG, PNG, WebP, HEIC (converted to JPEG via Canvas API on the client).
- HEIC detection by magic bytes (don't trust the extension).
- Server validation on upload: max 5 files per post, max 5MB per file post-compression, mime sniff (don't trust client-provided content-type).
- Upload destination: R2 keys like `posts/{yyyy}/{mm}/{uuid}.jpg`. Never put `user_id` or `post_id` in the path.
- `post_media` row per file with `order` (0–4).
- Image rendering: server generates a signed R2 URL with 1-hour expiry per image, **after** confirming the viewer is connected to the post author.

### Acceptance criteria

- Mobile uploads work from camera and library.
- HEIC photos from iPhone are converted client-side and upload successfully.
- Signed URLs are not guessable and do expire.
- A disconnected user who somehow obtained a signed URL cannot use it after disconnection (since the URL is short-lived and not regenerated for them).

### Testing plan

**Automated**

- Unit test: server-side mime sniffing rejects mismatched content (HTML pretending to be JPEG).
- Integration test: 6 files in one post is rejected.
- Integration test: file size limit enforced after compression.
- Integration test: signed URL generation refuses to issue URLs when caller is not connected.
- Integration test: post deletion removes all `post_media` files from R2.

**Manual**

- iPhone Safari: upload from camera roll (HEIC); verify it shows up correctly.
- Android Chrome: same.
- Various aspect ratios including very tall (9:16) and very wide (panorama).
- Verify compression doesn't block the UI on a low-end Android device.
- Attempt malicious uploads (e.g., HTML renamed to `.jpg`) and confirm rejection.

## 2.2 Threaded comments

### Implementation

- `comments` table per Technical Plan §3.
- `POST /posts/{id}/comments`: creates a top-level comment.
- `POST /comments/{id}/replies`: creates a reply (sets `parent_comment_id`).
- `DELETE /comments/{id}`: allowed for the comment author or the post owner.
  - If the comment has any direct or descendant replies: set `status = 'deleted'`, render as a `[deleted]` placeholder preserving thread structure.
  - If no replies: hard-delete the row.
- Comment counts are not rendered anywhere.
- Notification triggers:
  - New top-level comment → notify post author (`comment_on_post`).
  - New reply → notify parent comment author (`reply_to_comment`).
  - No self-notifications (commenting on your own post or replying to your own comment doesn't notify yourself).
- Extend `CreateNotification` usage to cover these types.

### Acceptance criteria

- Threading preserved even when intermediate comments are deleted.
- Permission rules enforced.
- Notifications fire correctly without self-notification.

### Testing plan

**Automated**

- Integration test: top-level comment creation and rendering.
- Test: reply creation references the correct parent.
- Test: deletion by neither author nor post owner returns 403.
- Test: deletion of a parent with replies preserves thread (soft delete).
- Test: deletion of a leaf hard-deletes.
- Test: no self-notifications.
- Test: comment on post creates the right notification for the post author.
- Test: reply creates a notification for the parent comment author (and only them).

**Manual**

- Build a multi-level thread (4–5 deep); test rendering.
- Delete a parent comment with replies; verify children survive as `[deleted]` placeholder threads.
- Delete a leaf; verify it disappears entirely.
- Verify deleted comments are not editable to anyone.

## 2.3 Post editing and edit history

### Implementation

- `GET /posts/{id}/edit`: form (author only).
- `POST /posts/{id}`: snapshot current `content` into `post_edits` with `edited_at = NOW()`, then update `posts.content` and `posts.updated_at`.
- Edited posts render with an "edited" indicator and a link to history.
- `GET /posts/{id}/history`: lists all prior versions in reverse chronological order. Visible to any user with access to the post (i.e., connected to the author).

### Acceptance criteria

- Edit history is append-only — old versions cannot be individually deleted.
- Anyone who can see the post can see its history.
- Non-author cannot edit.

### Testing plan

**Automated**

- Integration test: editing produces a `post_edits` row.
- Test: history endpoint returns all versions in correct order.
- Test: non-author edit attempt returns 403.
- Test: non-connected user requesting history returns 404.

**Manual**

- Edit a post 5–6 times; verify each version captured.
- Verify history rendering with long content.
- Verify "edited" indicator copy and link target.

## 2.4 Archive and delete

### Implementation

- `POST /posts/{id}/archive` (author only): `status = 'archived'`.
- `POST /posts/{id}/unarchive` (author only): `status = 'active'`.
- Archived posts excluded from feeds and profile views; they remain in DB with comments intact.
- `DELETE /posts/{id}` (author only): `status = 'deleted'`, null content, delete `post_edits` rows, delete `post_media` rows and the underlying R2 objects, delete `comments` referencing the post.
- `GET /settings/archived`: paginated list of the author's archived posts with unarchive buttons.

### Acceptance criteria

- Archived posts recoverable via unarchive into chronological position.
- Deleted posts are not recoverable; all associated content and media are gone.
- A deleted post disappears from all views, including any "old posts" piles (the feed query filters on `status = 'active'`, so this falls out for free).

### Testing plan

**Automated**

- Integration test: archived post excluded from feed and profile.
- Test: archive → unarchive restores the post to feed.
- Test: delete cascades to `post_edits`, `post_media`, and `comments`.
- Test: delete removes R2 objects (verify with a mock or test bucket).

**Manual**

- Archive several posts; verify they vanish from feed.
- Visit `/settings/archived`; confirm they're listed.
- Unarchive one; confirm it reappears in chronological position.
- Delete one; confirm it's gone everywhere, including the R2 bucket.

## 2.5 Likes — removed

The like feature was built during Phase 2 and subsequently **removed in its entirety**. There is no `likes` table, no like/unlike endpoints, no author-only liker list, and no `like_on_post` notification. Migration `010_remove_likes.sql` drops the table and the `likes_on_posts` preference column, and tightens the `notifications` type CHECK to exclude `like_on_post`. The product has no reaction or "favorite" affordance of any kind — see Technical Plan §4.4.5 and the §9 decision log. Do not reintroduce one.

---

# Phase 3 — Polish

## Goal

Make Hearth feel like a finished product. PWA installable. Email notifications working. Empty and error states properly handled. Basic abuse protections in place.

## 3.1 PWA manifest and service worker

### Implementation

- `manifest.json` with: app name, short name, icons (192px, 512px, maskable), `start_url: "/"`, theme color, `display: "standalone"`.
- Service worker registered on first authenticated page load.
- Caching strategy:
  - Shell (CSS, JS, fonts): cache-first, version-stamped URLs.
  - Feed HTML: stale-while-revalidate — render cached feed instantly, fetch fresh in the background.
  - Signed-URL image responses: do **not** cache (they expire).

### Acceptance criteria

- App installable on iOS Safari (Add to Home Screen) and Android Chrome.
- Cached shell loads in offline mode (shows feed cache if available, helpful offline message otherwise).
- App icon and theme color visible on home screen.

### Testing plan

**Automated**

- Manifest validation against the W3C PWA spec.
- Playwright test for service worker registration on a fresh session.
- Lighthouse PWA audit in CI; target score ≥ 90.

**Manual**

- Install on iPhone (iOS 16+); launch from home screen; verify standalone mode (no browser chrome).
- Install on Android; same.
- Toggle airplane mode mid-session; verify shell still loads with a sensible message.

## 3.2 Email notifications via Resend

### Implementation

- Resend client wrapper. One function: `SendEmail(to, template, vars)`.
- Email templates (plain HTML, dark-mode friendly, no images): verification, password reset, invitation accepted, comment on your post, reply to your comment.
- Notification flow: at `CreateNotification` time, **also** check the recipient's `notification_preferences` for the type. If enabled, enqueue email.
- Email retries: 1 retry on Resend failure for notification emails. Verification and password-reset emails get 3 retries (they're more important).
- One-click unsubscribe: every notification email has a link like `/u/{signed_token}` that toggles the relevant preference flag off. Token contains user_id + category + HMAC. Does not require login.
- `GET /settings/notifications`: toggles for each category (default off).

### Acceptance criteria

- User with all flags off receives zero notification emails. Verification + password reset still arrive (those are transactional).
- User with flag on receives the corresponding email within ~10 seconds of the event.
- Unsubscribe link works without requiring login.
- Failed Resend call does not block the in-app notification (the row is still created).

### Testing plan

**Automated**

- Unit test: `shouldEmail(user, type)` helper returns the right answer for each combination.
- Integration test: connection-accept with email flag on → mock Resend called with the right payload.
- Integration test: same with flag off → mock Resend not called.
- Integration test: unsubscribe link toggles the correct flag.
- Integration test: Resend failure does not roll back the in-app notification.

**Manual**

- End-to-end: toggle "email me about new comments" on, post, have someone comment, confirm email arrives.
- Verify email rendering in Gmail web, Apple Mail, and Outlook web.
- Click unsubscribe in a logged-out browser; confirm it works and shows confirmation.

## 3.3 Empty and error states

### Implementation

Empty state copy:

- New user with zero connections (feed): "Generate an invite link to connect with someone."
- New user with connections but empty feed: "Your feed will fill up as your connections post."
- `/notifications` empty: "Nothing here yet."
- `/settings/archived` empty: "You haven't archived any posts."
- `/u/{username}` (connected, no posts): "{Name} hasn't posted yet."

Error pages:

- 404: friendly page with link back to feed.
- 500: generic apology + retry guidance.
- 429: explain the rate limit and when it'll lift (using the actual lift time from the rate-limit logic).
- htmx swap failure: inline error block with retry button.

### Acceptance criteria

- No raw stack traces visible in production for any failure mode.
- All errors logged server-side with request_id, user_id (if any), method, and path.

### Testing plan

**Automated**

- Test: each error code renders the styled page, not the default.
- Test: htmx error swap shows the inline retry component.

**Manual**

- Visit `/notifications` as a new user; check copy.
- Trigger each error class manually:
  - Visit a nonexistent `/u/{username}` → 404.
  - Trigger a 429 by hitting an endpoint past the rate limit.
  - Induce a 500 (e.g., by temporarily breaking a DB query) — confirm graceful degradation.

## 3.4 Abuse mitigations

### Implementation

- Signup: per-IP rate limit. Suggest 5 signups per IP per 24 hours. Track in a `signup_attempts` table or in-memory if simple enough.
- Login: 10 failed attempts per IP per 15 minutes → block with 429 (already implemented in 0.4).
- Password reset: 3 per email and 3 per IP per hour (already implemented in 0.5).
- Content reporting: `POST /reports` available from any post or comment. Stores `{ reporter_id, target_type, target_id, reason, reported_at }` and sends an email to the configured `ADMIN_REPORT_EMAIL`.
- No admin UI in MVP — the admin handles reports manually from their inbox.

### Acceptance criteria

- Bursts of signups from one IP are rejected after the limit.
- Login brute-force on one account from one IP gets blocked.
- Reports send an email and show a "thanks, we'll look into it" confirmation.

### Testing plan

**Automated**

- Per-IP signup rate limit test: 5 succeed, 6th returns 429.
- Login brute-force test: 10 wrong passwords, 11th returns 429.
- Test: report stores the row and triggers an email.

**Manual**

- Verify admin report emails arrive at the configured address.
- Try to legitimately sign up two accounts from the same IP (e.g., a household); confirm the limit isn't so tight as to be a problem.

---

# Appendix A — Suggested directory structure

```
hearth/
├── cmd/
│   └── server/
│       └── main.go
├── internal/
│   ├── auth/                  # signup, login, sessions, password reset
│   ├── connections/           # connections, invites, accept-to-connect, disconnect
│   ├── feed/                  # feed query + pagination
│   ├── media/                 # R2 upload, signed URLs, image processing helpers
│   ├── notifications/         # in-app + email dispatch
│   ├── posts/                 # posts, edits, comments, archive/delete
│   ├── profiles/              # profile view + edit, account deletion
│   └── shared/
│       ├── config/            # env-based config loading
│       ├── db/                # DB init, migrations runner
│       ├── email/             # Resend client + templates
│       ├── middleware/        # auth, request ID, logging, rate limiting
│       └── render/            # html/template wrapper, error pages
├── migrations/                # goose migration files
├── web/
│   ├── templates/             # html/template files, organized by package
│   └── static/                # CSS, htmx.min.js, manifest.json, service worker
├── tests/
│   ├── integration/           # cross-package handler tests
│   └── e2e/                   # Playwright suite
├── Dockerfile
├── fly.toml
├── go.mod
└── go.sum
```

# Appendix B — Test coverage targets

Pragmatic, not absolute:

- **Unit tests:** >70% coverage on business-logic packages (auth, connections, feed, posts, notifications).
- **Integration tests:** every HTTP handler has at least one happy-path and one failure-path test.
- **E2E tests:** the critical user flows are fully covered:
  - Sign up → email verify → log in → edit profile → log out.
  - Invite → unauthenticated open → sign up → email verify → return to invite → accept → both immediately connected, inviter notified.
  - Post → comment → both users see the comment → post author gets notification.
  - Disconnect → posts and profile no longer visible to either party.
  - Account deletion → soft delete → 30 days pass → hard delete cleans up.

# Appendix C — Open implementation questions

These are minor and don't block the build. Mostly worth a decision before the relevant phase.

1. **Timezone for the "lift time" message on rate limits.** Server's UTC vs. user's locale? Recommend rendering a relative time ("in 3 days") and a UTC absolute as a tooltip.
2. **Username case sensitivity in URLs.** Recommend: always lowercase in storage; `/SomeUser` 301-redirects to `/someuser`.
3. **Should `last_feed_loaded_at` update on `/notifications` view, or only on `/` view?** Recommend: only on `/`. Notifications are a separate surface.
4. **Comment ordering within a thread.** Recommend chronological ascending (oldest first), which is the typical thread convention.
5. **Edit history retention on archive.** When a post is archived, history is preserved (it's only deleted on actual delete). Confirm this is the intent.
