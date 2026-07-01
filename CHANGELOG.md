# Changelog

All notable changes to Hearth are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Versions are pre-1.0 development milestones; numbers are assigned
retroactively to mark meaningful steps, not formal releases.

## [Unreleased]

### Security

- Production now refuses to start if transactional email is not configured, so
  password-reset and verification tokens can never fall back to being logged to
  stdout. Staging and development are unaffected.
- Rate limiting no longer trusts the spoofable X-Forwarded-For header; only
  Fly's authoritative client IP (or the direct connection address) is used, so
  an attacker can no longer rotate forged IPs to evade login/password-reset
  throttling or pin a victim's IP.
- The server now enforces read, write, and idle timeouts and caps the size of
  incoming request bodies, so slow or oversized requests can't tie up
  connections or grow unbounded.
- State-changing requests are now rejected unless they originate from the site
  itself (CSRF defense-in-depth on top of the SameSite session cookie).

### Changed

- A reply to one of your own comments now reads "replying to you" (in a warm
  accent, so it stands out) instead of repeating your own name.

## [0.10.0] - 2026-06-30

### Added

- A "What's New" page (linked from the footer) listing recent updates in plain,
  non-technical language, plus an in-app notification sent to everyone when a
  new release ships, pointing them to it.
- Opening a post or comment from a notification now briefly highlights the
  target with a fading accent glow, so the item the notification pointed to
  stands out on arrival. Comment and reply notifications now deep-link to the
  comment itself rather than just the post it lives under.

### Changed

- Comments can now be replied to at any depth; replies appear in a single flat
  tier under the top-level comment, each prefaced by a subtle "replying to
  {username}" line showing who it answers.
- Clicking "Reply" on any comment now opens a single reply box at the bottom of
  that comment's thread, below its existing replies, instead of an inline box
  under each comment. The box shows which comment you're replying to.
- Each notification is now a single clickable row that opens its point of
  interest directly — the comment for a comment or reply, the person's profile
  for a new connection — instead of embedding several links in the text.

## [0.9.0] - 2026-06-29

### Added

- Video attachments (MP4 and MOV) alongside images.
- Client-side image compression before upload.
- Paste-to-attach for images pasted into the composer.
- Staging environment seeded with mock data.

### Changed

- Carried the "firelit room" visual treatment into the app interior; posts
  now render as tiles.
- Upload errors name the rejected file's type.

## [0.8.0] - 2026-06-16

### Added

- Consolidated `/settings` hub, linked from the nav.
- Opt-in cross-network comment visibility.
- Pull-to-refresh on touch devices.
- Swipe navigation in the mobile image gallery.

### Changed

- Feed photos fill the post width, fit instead of cropping, and reserve their
  dimensions to stop layout jump.

### Removed

- The new/old "older posts" feed split.

## [0.7.0] - 2026-06-11

### Added

- Public landing page and About page.
- "By Firelight" immersive dark landing redesign.
- Open Graph link previews for invite links.
- Home-screen / PWA app icons.

### Changed

- Accepting an invite now connects both users immediately; the separate
  confirmation step is gone.

## [0.6.0] - 2026-06-10

### Added

- Avatar upload at signup with a default silhouette placeholder.
- Profile pictures shown in the feed.
- Viewer's own posts included in the home feed.
- Unread-notification dot on the hamburger menu.

### Changed

- Modernized post and comment styling; comment inputs are toggleable.
- Collapsed the nav into a hamburger on mobile.
- Non-connected authors' comments are hidden; reply depth is capped.
- Invite links accept up to 10 uses each.

### Removed

- The like feature and all reaction affordances — removed wholesale.

## [0.5.0] - 2026-06-07

### Added

- Threaded comments on connected users' posts.
- Create-post button and a shared compose modal.
- Photo lightbox.
- Profiles served from `/u/{username}`.
- Single-use invite links shown in a copy modal.
- Profile preview for pending connection requesters.
- Descriptive, privacy-preserving page for inaccessible profiles.
- Empty-feed prompt linking to the Connections page.

### Changed

- Replaced the requests page with pending connections.

### Fixed

- Redirect to `/u/{username}` after creating a post.
- Lightbox close behavior and conditional nav arrows.
- Timestamps render in the viewer's local timezone.
- Notifications carry post links and meaningful comment/reply text.

## [0.4.0] - 2026-06-06

### Added

- Dark mode toggle.
- Preview selected images before posting.
- Upload progress after clicking Post.
- Private post likes with a liker modal.
- Per-post total image limit raised to 200 MB.
- Pre-verified dev user seeded on startup.

## [0.3.0] - 2026-06-02

### Added

- Image uploads: create, render via short-lived signed R2 URLs, delete cascade.
- `post_media` table with presigned GET URLs (25 MB cap).
- Responsive image grid and compose file picker.
- Client-side square crop UI and server-side square-JPEG normalization for
  profile photos.

### Changed

- Warm editorial "Private Hearth" visual refresh.
- Hard-delete now cleans post media from R2.

## [0.2.0] - 2026-05-30

### Added

- Phase 1: invites, connections, posts, feed, and notifications.
- Profile photo upload via R2.
- Split display name into first and last name.
- Client-side inline validation on the login form.
- Pretty dev logs via tint; JSON logs in production.
- GitHub Actions deploy to Fly.io — staging on `dev`, production on `main`.

### Fixed

- Resend verification by email without auth; issue a session after signup so
  resend works; allow unverified emails to be reclaimed on re-signup.
- Failed-login timestamp format mismatch.
- Upgraded the Dockerfile to Go 1.24 to match `go.mod`.

## [0.1.0] - 2026-05-20

### Added

- Project scaffolding: Go module, config, `/healthz` server, Dockerfile,
  `fly.toml`, and CI.
- SQLite with WAL and foreign-key pragmas; embedded goose migrations.
- Shared `render`, `middleware`, and `email` packages.
- Auth: signup, email verification, login, logout, password reset.
- Profiles: view/edit, soft delete, and an hourly cleanup sweep.
- Base layout, auth and profile templates, minimal CSS.
- Litestream sidecar for streaming WAL backups.

[0.10.0]: https://github.com/declanblanc/hearth/releases/tag/v0.10.0
[0.9.0]: https://github.com/declanblanc/hearth/releases/tag/v0.9.0
[0.8.0]: https://github.com/declanblanc/hearth/releases/tag/v0.8.0
[0.7.0]: https://github.com/declanblanc/hearth/releases/tag/v0.7.0
[0.6.0]: https://github.com/declanblanc/hearth/releases/tag/v0.6.0
[0.5.0]: https://github.com/declanblanc/hearth/releases/tag/v0.5.0
[0.4.0]: https://github.com/declanblanc/hearth/releases/tag/v0.4.0
[0.3.0]: https://github.com/declanblanc/hearth/releases/tag/v0.3.0
[0.2.0]: https://github.com/declanblanc/hearth/releases/tag/v0.2.0
[0.1.0]: https://github.com/declanblanc/hearth/releases/tag/v0.1.0
