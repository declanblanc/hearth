// Package changelog holds the user-facing "What's New" list: the plain-language
// summary of changes a person would actually notice, distinct from the
// developer-facing CHANGELOG.md at the repo root. It is deliberately just a Go
// slice — no markdown, no file parsing, no dependency — because the content is
// short, hand-curated, and edited in one place.
package changelog

// Release is one dated batch of user-facing changes.
type Release struct {
	// Version is a stable key used only to dedupe the new-release notification
	// (see notifications.Service.AnnounceRelease). It is never shown to users.
	Version string
	// Date is the display heading, e.g. "June 29, 2026".
	Date string
	// Changes are plain-language, user-facing lines — one per thing a person
	// would notice. Keep the language non-technical.
	Changes []string
}

// Releases lists what users would notice, newest first. When you ship something
// a person would see, add an entry at the top and keep the language plain.
var Releases = []Release{
	{
		Version: "0.10.0",
		Date:    "June 30, 2026",
		Changes: []string{
			"This page! A plain-language list of what's new, plus a heads-up whenever there's an update.",
			"Reply to any comment in a thread; replies line up together and show who they answer.",
			"Tapping \"Reply\" opens a single box at the bottom of the thread.",
			"Each notification is now a single tap that takes you straight to what it's about.",
			"The comment or post a notification points to glows briefly when you arrive.",
		},
	},
	{
		Version: "0.9.0",
		Date:    "June 29, 2026",
		Changes: []string{
			"Share videos, not just photos.",
			"Photos are compressed automatically so they upload faster.",
			"Paste an image straight into the composer to attach it.",
			"Posts now appear as tidy tiles, with the warm firelit look carried through the whole app.",
		},
	},
	{
		Version: "0.8.0",
		Date:    "June 16, 2026",
		Changes: []string{
			"Everything you can adjust now lives on one Settings page.",
			"Optionally let friends-of-friends see your comments.",
			"Pull down to refresh on your phone.",
			"Swipe between photos in the gallery.",
			"Photos fill the post cleanly and no longer make the page jump.",
		},
	},
	{
		Version: "0.7.0",
		Date:    "June 11, 2026",
		Changes: []string{
			"A welcome page and an About page for people you invite.",
			"A calmer, darker sign-in screen.",
			"Invite links now show a nice preview when you share them.",
			"Add Hearth to your home screen with its own icon.",
			"Accepting an invite connects you right away — no extra confirmation step.",
		},
	},
	{
		Version: "0.6.0",
		Date:    "June 10, 2026",
		Changes: []string{
			"Add a profile photo when you sign up.",
			"Profile photos now show in the feed.",
			"Your own posts appear in your feed.",
			"A small dot marks unread notifications.",
			"Refreshed posts and comments; the menu tucks away neatly on mobile.",
			"Removed likes and reactions.",
		},
	},
	{
		Version: "0.5.0",
		Date:    "June 7, 2026",
		Changes: []string{
			"Reply to comments in threads.",
			"A clear button and box for writing a new post.",
			"Tap a photo to view it full-screen.",
			"Friendlier profile links.",
			"Copy an invite link to share.",
			"Times now show in your own timezone.",
		},
	},
	{
		Version: "0.4.0",
		Date:    "June 6, 2026",
		Changes: []string{
			"Dark mode.",
			"See your images before you post them.",
			"A progress bar while photos upload.",
		},
	},
	{
		Version: "0.3.0",
		Date:    "June 2, 2026",
		Changes: []string{
			"Add photos to your posts.",
			"Crop your profile photo when you upload it.",
		},
	},
	{
		Version: "0.2.0",
		Date:    "May 30, 2026",
		Changes: []string{
			"The basics — invite friends, connect, post, see your feed, and get notified.",
		},
	},
	{
		Version: "0.1.0",
		Date:    "May 20, 2026",
		Changes: []string{
			"Hearth's first light — accounts and sign-in.",
		},
	},
}

// Latest returns the newest release, used to decide which release to announce.
func Latest() Release { return Releases[0] }
