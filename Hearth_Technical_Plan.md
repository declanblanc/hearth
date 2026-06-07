# Hearth — Technical Plan & Specifications (v0.3 Draft)

This document translates the product description and technical brainstorm into concrete requirements, a recommended stack, a data model, and a phased build plan. It is a starting point, not a final spec — every section is open to revision.

---

## 1. Guiding principles

Three constraints drive every decision:

- **Cheap to run.** Donation-funded means runaway costs are existential. Target hosting cost at MVP: **under $15/month** for up to 1,000 users.
- **Simple to maintain.** Prefer boring, well-documented tech over clever-and-cutting-edge.
- **Privacy by default.** No content is publicly visible. All access is gated on an active connection. This is enforced at the data layer, not just the UI.

A useful frame: many "social media architecture" assumptions can be discarded outright. There is no fanout problem (small connection counts, no real-time delivery), no ranking problem (chronological), no discovery problem (no discovery), and no celebrity problem (no celebrities). This is closer to a private group blog with permissions than to Twitter.

---

## 2. Recommended stack

### Frontend: htmx + server-rendered HTML

Rationale: React/Vue exist to manage complex client-side state with real-time updates. Hearth has neither. Plain HTML works but gets unwieldy once you have threaded comments, post composition, edit modals, etc. **htmx is the sweet spot**: the server renders HTML, htmx adds Ajax interactivity via attributes like `hx-post` and `hx-target`. Bundle size is ~14KB. No build step. No virtual DOM to debug.

Why this fits Hearth specifically:

- Pages are mostly static lists of content. htmx's "swap a chunk of HTML into the page" model maps directly onto "load more posts" and "post a comment."
- Server-rendered means SEO and accessibility are essentially free.
- Works without JavaScript for basic flows (progressive enhancement).
- PWA-friendly via a small service worker for caching static assets and the most recent feed.

_Alternatives considered: SvelteKit (good but adds build pipeline), Astro (server-render with interactive islands, more setup), plain HTML/JS (you'll reinvent half of htmx by week three)._

### Backend: Go

Chosen for the cost angle: single static binary, ~20–50 MB memory footprint, no runtime to patch, excellent operational simplicity. Python (Flask/FastAPI) was the runner-up — faster dev speed and larger ecosystem, but heavier at runtime. Specific library choices (router, template engine, SQLite driver, session library) deferred to the implementation document.

### Database: SQLite (with Litestream for backups)

For <1,000 users, SQLite isn't a compromise — it's the right answer:

- No separate database process to run, patch, or pay for.
- Reads are microseconds (it's in-process).
- A backup is a single file copy.
- **Litestream** streams continuous WAL backups to S3/R2 for ~$1/month, giving you point-in-time recovery.
- Comfortably handles 10,000+ users before you'd need to think about Postgres.

The two "hot paths" you flagged (fetching feed posts, creating posts) are trivial for SQLite at this scale, especially with the indexes proposed below.

### Media storage: Cloudflare R2

- S3-compatible API (no vendor lock-in).
- **Zero egress fees** — this is the headline feature. S3 charges $0.09/GB out, which can become the single biggest cost line at any real usage.
- $0.015/GB/month for storage.
- Native support for signed URLs, which we need (see media access control below).

### Email: Resend

- Free tier: 3,000 emails/month, 100/day. Plenty for verification + password reset + (eventual) notification email at MVP scale.
- Simple API, good deliverability.
- AWS SES is the cheaper alternative at scale ($0.10 per 1,000) but has more setup overhead.

### Hosting: Fly.io

- ~$5/month for a small shared-CPU instance with persistent volume.
- Easy deploys, automatic TLS, regional placement.
- Persistent volume is important — SQLite database file lives on it.

Alternative: a Hetzner CX11 VPS at €4/month is cheaper if you're comfortable managing your own server.

### Estimated monthly cost at MVP scale

| Item                         | Cost             |
| ---------------------------- | ---------------- |
| Fly.io app + 3GB volume      | ~$5              |
| Cloudflare R2 (100 GB media) | ~$1.50           |
| Litestream backups to R2     | ~$1              |
| Resend email                 | $0               |
| Domain                       | ~$1 amortized    |
| **Total**                    | **~$8–10/month** |

---

## 3. Data model

Tables below use generic SQL types; adapt to your chosen ORM/driver.

### `users`

| column              | type        | notes                                              |
| ------------------- | ----------- | -------------------------------------------------- |
| id                  | INTEGER PK  |                                                    |
| username            | TEXT UNIQUE | immutable after signup, used in `/u/{username}` URLs |
| email               | TEXT UNIQUE |                                                    |
| password_hash       | TEXT        | argon2id                                           |
| display_name        | TEXT        | editable                                           |
| photo_key           | TEXT        | R2 object key, nullable                            |
| bio                 | TEXT        | nullable                                           |
| pronouns            | TEXT        | nullable                                           |
| created_at          | TIMESTAMP   |                                                    |
| email_verified_at   | TIMESTAMP   | nullable                                           |
| last_feed_loaded_at | TIMESTAMP   | the "seen" timestamp                               |

### `sessions`

Standard session table (id, user_id, token_hash, created_at, expires_at, last_seen_at). Cookies, not JWTs — simpler for server-rendered apps.

### `connections`

| column     | type       | notes                           |
| ---------- | ---------- | ------------------------------- |
| id         | INTEGER PK |                                 |
| user_a_id  | INTEGER FK | always the lower of the two IDs |
| user_b_id  | INTEGER FK | always the higher               |
| created_at | TIMESTAMP  |                                 |

Storing `(min_id, max_id)` avoids duplicate rows for the same pair. Unique index on `(user_a_id, user_b_id)`.

### `invites`

| column      | type        | notes                      |
| ----------- | ----------- | -------------------------- |
| id          | INTEGER PK  |                            |
| sender_id   | INTEGER FK  |                            |
| token       | TEXT UNIQUE | URL-safe random, ~32 bytes |
| created_at  | TIMESTAMP   |                            |
| expires_at  | TIMESTAMP   | created_at + 72h           |
| consumed_at | TIMESTAMP   | nullable; set when used    |

### `connection_requests`

| column       | type       | notes                                |
| ------------ | ---------- | ------------------------------------ |
| id           | INTEGER PK |                                      |
| invite_id    | INTEGER FK | the invite the requester arrived via |
| requester_id | INTEGER FK |                                      |
| recipient_id | INTEGER FK |                                      |
| status       | TEXT       | 'pending' \| 'accepted' \| 'denied'  |
| created_at   | TIMESTAMP  |                                      |
| resolved_at  | TIMESTAMP  | nullable                             |

### `posts`

| column     | type       | notes                                |
| ---------- | ---------- | ------------------------------------ |
| id         | INTEGER PK |                                      |
| author_id  | INTEGER FK |                                      |
| content    | TEXT       | max 1000 chars                       |
| status     | TEXT       | 'active' \| 'archived' \| 'deleted'  |
| created_at | TIMESTAMP  | the immutable original creation time |
| updated_at | TIMESTAMP  | last edit                            |

Index: `(author_id, created_at DESC)` for profile views. For the feed query, see "Performance notes" below.

### `post_edits`

| column    | type       | notes                                 |
| --------- | ---------- | ------------------------------------- |
| id        | INTEGER PK |                                       |
| post_id   | INTEGER FK |                                       |
| content   | TEXT       | snapshot of content _before_ the edit |
| edited_at | TIMESTAMP  | when this version was replaced        |

On edit, we insert a snapshot of the _old_ content before overwriting the post. The current version always lives on `posts`.

### `post_media`

| column     | type       | notes  |
| ---------- | ---------- | ------ |
| id         | INTEGER PK |        |
| post_id    | INTEGER FK |        |
| object_key | TEXT       | R2 key |
| order      | INTEGER    | 0–4    |
| mime_type  | TEXT       |        |

### `comments`

| column            | type       | notes                       |
| ----------------- | ---------- | --------------------------- |
| id                | INTEGER PK |                             |
| post_id           | INTEGER FK |                             |
| parent_comment_id | INTEGER FK | nullable, enables threading |
| author_id         | INTEGER FK |                             |
| content           | TEXT       |                             |
| status            | TEXT       | 'active' \| 'deleted'       |
| created_at        | TIMESTAMP  |                             |

### `likes`

| column     | type       | notes                       |
| ---------- | ---------- | --------------------------- |
| id         | INTEGER PK |                             |
| post_id    | INTEGER FK |                             |
| user_id    | INTEGER FK | the user who liked the post |
| created_at | TIMESTAMP  |                             |

A unique constraint on `(post_id, user_id)` makes a like idempotent — a user can like a given post at most once. This table is the **only** place a like is recorded. It exists to (a) drive the like notification and (b) let the post author see the list of who liked, and nothing else. It is never used to compute or display a count, publicly or privately. See §4.4.5 for the flow and the privacy constraints.

### `notification_preferences`

One row per user, all flags default to `false`. Categories at MVP: connection requests, comments on your posts, replies to your comments, likes on your posts. These flags only control whether **email** is sent — in-app notifications (below) are always created.

### `notifications`

| column     | type       | notes                                                                                    |
| ---------- | ---------- | ---------------------------------------------------------------------------------------- |
| id         | INTEGER PK |                                                                                          |
| user_id    | INTEGER FK | recipient                                                                                |
| type       | TEXT       | 'connection_request' \| 'connection_accepted' \| 'comment_on_post' \| 'reply_to_comment' \| 'like_on_post' |
| actor_id   | INTEGER FK | user who triggered it, nullable                                                          |
| post_id    | INTEGER FK | nullable; set for comment-related notifications                                          |
| comment_id | INTEGER FK | nullable; set for comment-related notifications                                          |
| created_at | TIMESTAMP  |                                                                                          |
| read_at    | TIMESTAMP  | nullable; set when user visits `/notifications`                                          |

Indexes: `(user_id, created_at DESC)` for the notifications view, `(user_id) WHERE read_at IS NULL` for the unread indicator.

### Counters

Deliberately absent: any aggregate count columns (connection_count, comment_count, like_count). The product is defined by what it doesn't show.

A `likes` table exists (see above), but it is **not** a counter. Likes are private signals of appreciation, never public metrics:

- Liking a post sends the author a notification ("{name} liked your post") — that is the entire payoff.
- There is no public indication that a post has been liked, and no like count anywhere — not on the post, not in the author's view, not in any API response.
- The post author (and only the author) can see the _list of names_ of users who liked a given post. Names, never a number.

In short: the `likes` table feeds a notification and an author-only liker list, and is never aggregated.

---

## 4. Key flows

### 4.1 The feed

```
GET /
1. Fetch the user's connection list (cached in session).
2. SELECT * FROM posts
     WHERE author_id IN (connection_ids)
       AND status = 'active'
     ORDER BY created_at DESC
     LIMIT 50;
3. Split results in the view layer by created_at vs. users.last_feed_loaded_at:
     - "New": created_at > last_feed_loaded_at
     - "Old": created_at <= last_feed_loaded_at  (rendered behind a toggle)
4. UPDATE users SET last_feed_loaded_at = NOW() WHERE id = ?;
5. Older posts paginate via cursor (?before=<post_id>).
```

Note: step 4 happens _after_ the query in step 2, so the same load doesn't re-classify its own results.

### 4.2 Connection flow

1. User A: `POST /invites` → generate token, insert into `invites`, return link `https://hearth.app/i/{token}`.
   - Reject if A has sent ≥20 invites in the trailing 7 days.
2. A shares the link out-of-band (email, SMS, in person).
3. User B opens `/i/{token}`. The handler routes based on auth state and invite state:

   ```
   GET /i/{token}
   ├─ Token not found / expired / consumed → "this invite is no longer valid" page.
   ├─ Requester is signed in:
   │   ├─ Requester is the sender → "you can't connect to yourself" error.
   │   ├─ Already connected → "you're already connected with {name}" page.
   │   └─ Otherwise → render "Request to connect with {name}" page,
   │                  showing A's display name and photo.
   └─ Requester is NOT signed in:
       1. Set a short-lived signed cookie: `pending_invite={token}`, 30-min expiry.
       2. Redirect to /welcome?invite={token}, a chooser page with two
          buttons: "I have an account → Log in" and "I'm new → Sign up".
       3. After successful login or signup (including the email verification
          round-trip), the auth handler checks for the pending_invite cookie
          and redirects back to /i/{token}, which now hits the signed-in branch.
   ```

   **Notes:**
   - The cookie matters even though the token is in the URL, because email verification typically happens in a different browser session (user clicks the link in their email client). The cookie is what carries the pending invite across that gap.
   - The invite is _consumed_ only when B accepts the invitation (step 4), not when the link is opened. Opening `/i/{token}` multiple times during signup doesn't burn the invite.

4. B clicks "Accept invitation" (`POST /i/{token}/accept`): insert `connection_requests` row (pending), mark invite consumed.
5. A is notified and sees the pending connection pinned to the top of their connections page (`/connections`). There is no separate requests page — the word "request" is avoided in the UI as it doesn't fit the mutual connection model.
6. A clicks confirm (`POST /connections/{id}/confirm`) → insert `connections` row, set the pending row's status to 'accepted'. (Or declines via `POST /connections/{id}/decline`.)
   - Reject if A has 10+ accepted connections in the trailing 7 days; show "you've reached your weekly limit, try again on [date]."

### 4.3 Disconnect

1. User A: `POST /connections/{user_b_id}/disconnect`.
2. Delete row from `connections`.
3. B is not notified.
4. From now on, neither user sees the other's posts/profile. Old posts B had seen are no longer accessible (they fall out of the feed query naturally because B is no longer in A's connection list, and vice versa).

### 4.4 Post lifecycle

- **Create**: text + 0–5 image references. Validate length, count, mime type.
- **Edit**: snapshot old content into `post_edits`, update `posts.content` and `updated_at`. UI shows "edited" with a "view history" link visible to anyone who can see the post.
- **Archive**: `status = 'archived'`. Excluded from feeds and profile views. Preserved in DB. Author can unarchive.
- **Delete**: `status = 'deleted'`, content nulled, associated media objects deleted from R2, `post_edits` rows deleted. The post effectively vanishes — including from anyone's "old posts" view, since the feed query filters on `status = 'active'`.

### 4.4.5 Likes (private appreciation, never a metric)

A like is a one-way, private signal from a reader to a post author. It is the only "reaction" the product has, and it is intentionally invisible to everyone except the author.

```
POST /posts/{id}/like      (toggle on)
1. Verify the liker is connected to the post author (privacy check). 404 if not.
2. INSERT OR IGNORE INTO likes (post_id, user_id, created_at) — unique (post_id, user_id) makes it idempotent.
3. If the row was newly inserted AND the liker is not the author:
     CreateNotification(author_id, 'like_on_post', actor_id=liker_id, post_id=id).
   (Liking your own post is allowed but never notifies you.)

DELETE /posts/{id}/like    (toggle off / unlike)
1. DELETE FROM likes WHERE post_id = ? AND user_id = ?.
2. Delete the corresponding 'like_on_post' notification for this (post, actor),
   so an unliked post doesn't leave a stale "{name} liked your post" entry.
```

Visibility rules — these are load-bearing:

- **No public indication.** Nothing in the post's rendering tells any viewer (including other likers) that the post has been liked, or by whom.
- **No count, anywhere.** Not on the post, not in the author's view, not in any response body.
- **Author-only liker list.** `GET /posts/{id}/likes` returns the display names of users who liked the post. It is authorized for the post author only; everyone else (connected or not) gets a 404. The response is a list of names — never a count.
- A viewer can see whether _they_ have liked a post (so the like control can render its toggled state), but nothing about anyone else's likes.

### 4.5 Media handling

- **Upload**: client compresses images using `browser-image-compression` library before posting. Targets: max 1600px on the long edge, 80% JPEG quality. Result is typically 100–400KB even for high-quality photos.
- **Accepted formats** at MVP: JPEG, PNG, WebP. GIFs accepted but treated as static images (no animation playback). HEIC converted client-side to JPEG via Canvas API.
- **Server validation**: max 5 files per post, max 5MB per file _after_ client compression (generous ceiling), mime sniff to confirm it's actually an image.
- **Storage**: random key like `posts/2026/05/{uuid}.jpg`. Never expose the user_id or post_id in the key.
- **Access**: every image URL is a signed R2 URL with 1-hour expiry, generated server-side at render time. Server first verifies the requesting user is connected to the post author.

### 4.6 Rate limit enforcement

Both checks are simple count queries on indexed columns:

```sql
-- Invites sent in last 7 days
SELECT COUNT(*) FROM invites
  WHERE sender_id = ? AND created_at > NOW() - INTERVAL '7 days';

-- Connections accepted in last 7 days
SELECT COUNT(*) FROM connections
  WHERE (user_a_id = ? OR user_b_id = ?)
    AND created_at > NOW() - INTERVAL '7 days';
```

Use a transaction with `BEGIN IMMEDIATE` in SQLite when inserting to avoid races at the limit boundary.

### 4.7 Notifications

Notifiable events at MVP: connection requests, connection accepts, comments on your posts, replies to your comments, and **likes on your posts** (`like_on_post`). When one happens, the server does two things:

1. **Always** insert a row in `notifications`. The in-app view is populated regardless of preferences.
2. **Conditionally** send an email via Resend — only if the recipient's `notification_preferences` flag for that category is `true`. All flags default to `false`, so the MVP default is silent email-wise.

The `/notifications` page lists the user's notifications, newest first. On load:

```sql
UPDATE notifications
   SET read_at = NOW()
 WHERE user_id = ? AND read_at IS NULL;
```

A small dot indicator appears in the site header when the user has unread notifications. **Deliberately no numeric count** — consistent with the no-visible-counts ethos applied to UX affordances. The dot disappears once the user visits `/notifications`.

---

## 5. Performance & caching

For <1,000 users this is mostly belt-and-suspenders, but the patterns are worth establishing early:

- **HTTP caching**: long `Cache-Control` on static assets (CSS, JS, images via signed URL).
- **Service worker**: cache the shell (CSS, JS, fonts) and the most recent feed HTML. On reload, show cached feed instantly, then revalidate.
- **DB indexes** at minimum:
  - `posts (author_id, created_at DESC) WHERE status = 'active'` — feed & profile queries
  - `connections (user_a_id)` and `connections (user_b_id)` — connection lookups
  - `invites (sender_id, created_at)` — rate limit query
  - `comments (post_id, created_at)` — comment thread loading
- **Feed query optimization**: if the `IN (connection_ids)` query ever gets slow (it won't at this scale), denormalize by storing connection IDs in a single column as a delimited string, or precompute a "feed entries" table. Not needed for MVP.

---

## 6. Anti-features: how they're enforced

These are the things the product _doesn't_ do, with a note on how that's enforced architecturally:

| Anti-feature                 | How it's enforced                                                                                                     |
| ---------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| No _public_ likes/reactions  | A like exists only as a private notification to the author plus an author-only liker list (§4.4.5). No public like indicator, no reactions, no emoji palette. |
| No discovery                 | No user search endpoint. `/u/{username}` returns 404 unless the requesting user is connected. No "people you may know." |
| No algorithm                 | Feed query is `ORDER BY created_at DESC`. Period.                                                                     |
| Notifications off by default | All flags in `notification_preferences` default to `false`                                                            |
| No visible counts            | Connection list returns names only. Comment thread returns comments, never `count`. Likes surface as a notification and an author-only list of names — never a number. |
| No messaging                 | No `messages` table, no endpoints                                                                                     |

---

## 7. Open decisions

Things I made a judgment call on while drafting. Each is worth a sanity check.

1. **Usernames are immutable, display names are editable.** Usernames appear in URLs (`/u/{username}`), so changing them would break invite links and bookmarks. Display names are what users see in the UI.
2. **GIFs treated as static images at MVP.** Animated GIFs are a low-grade form of video and bring most of the same complexity. Easy to revisit.
3. **Email notifications are the only notification channel at MVP.** No push, no SMS. Push notifications would require a service worker setup with VAPID keys; doable but adds scope.
4. **Account deletion ships in MVP.** Privacy-focused apps essentially require this for both ethical and GDPR-adjacent reasons. Soft-delete the user (anonymize email, null content), then hard-delete after 30 days.
5. **Data export ships in v2.** Useful but not blocking initial launch.
6. **HEIC accepted but converted to JPEG on the client.** iPhone users will otherwise be confused why their photos don't upload.
7. **`/i/{token}` is the invite URL pattern.** Short for brevity in shared links.
8. **Unread notifications shown as a dot, not a count.** Extends the no-visible-counts ethos to UX affordances. Easy to revisit if a count would be genuinely useful.

---

## 8. Phased build plan

### Phase 0 — Foundation

- Project skeleton, deploy pipeline, TLS
- `users` table, signup, email verification, login, logout, password reset
- Session management
- Basic profile view & edit (display name, bio, pronouns, photo)
- Account deletion flow

### Phase 1 — Core social loop

- Invites, pending connections, confirm/decline
- Disconnect
- Rate limiting (20 invites/week, 10 accepts/week)
- Post creation (text only)
- Feed with new/old split via `last_feed_loaded_at`
- Other-user profile view (connected only)
- Notifications table + `/notifications` view (connection events trigger in-app notifs)

**End of Phase 1: usable for friends-and-family beta.**

### Phase 2 — Richer content

- Image uploads (client compression, R2 storage, signed URLs)
- Threaded comments (extends the notifications system to comment events)
- Likes (private: notification to author + author-only liker list, no counts)
- Post editing + history view
- Archive + delete

### Phase 3 — Polish

- PWA manifest + service worker
- Email delivery for notifications (Resend integration) + per-category preferences UI in account settings
- Empty-state and error-state polish
- Basic abuse mitigations (per-IP signup rate limit, content reporting → email to you)

### Out of scope for v1

- Video uploads
- Donations + donor badge
- Public FAQ page
- Content moderation tooling
- Data export

---

## 9. Decision log

All major architectural decisions are now resolved for MVP:

- **Backend:** Go
- **Frontend:** htmx + server-rendered HTML
- **Database:** SQLite with Litestream backups
- **Media storage:** Cloudflare R2 with signed URLs (1-hour expiry)
- **Hosting:** Fly.io
- **Email:** Resend
- **Auth:** email + password + verification, session cookies
- **Invite flow:** `pending_invite` cookie carries the token through signup/login round-trips
- **Notifications:** email-only delivery (opt-in per category), plus an always-on in-app `/notifications` view with a dot indicator for unread
- **Likes (revised):** the original plan had _no_ like feature at all. Reconsidered: likes are allowed, but strictly private — liking a post notifies the author ("{name} liked your post"), the author can see an author-only list of who liked, and there is no public indicator and no count anywhere (§4.4.5). This preserves the no-public-metrics ethos while allowing lightweight appreciation.
- **Beta plan:** deploy to prod and share invite links with friends — no feature flag system

Next document: detailed schema migrations and a complete API endpoint list, after which we can start building.
