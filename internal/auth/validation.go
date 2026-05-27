package auth

import (
	"net/mail"
	"regexp"
	"strings"
)

const (
	UsernameMinLen = 3
	UsernameMaxLen = 30
	NameMinLen     = 1
	NameMaxLen     = 50
	PasswordMinLen = 12
	BioMaxLen      = 1000
	PronounsMaxLen = 50
)

var usernameRe = regexp.MustCompile(`^[a-z0-9_]+$`)

// FieldErrors is a map from form field name to a human-readable error message.
// 422 responses re-render the form with these inline.
type FieldErrors map[string]string

func (f FieldErrors) Has() bool { return len(f) > 0 }

func (f FieldErrors) Add(field, msg string) {
	if f[field] == "" {
		f[field] = msg
	}
}

// NormaliseUsername lowercases and trims; usernames are case-insensitive in
// storage and URLs (Appendix C #2).
func NormaliseUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// NormaliseEmail lowercases and trims. We intentionally don't apply
// provider-specific tricks (gmail dots etc.) — uniqueness is on the literal
// string after this step.
func NormaliseEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func ValidateUsername(u string) string {
	switch {
	case len(u) < UsernameMinLen || len(u) > UsernameMaxLen:
		return "Username must be 3–30 characters."
	case !usernameRe.MatchString(u):
		return "Username can only contain lowercase letters, numbers, and underscores."
	}
	return ""
}

func ValidateEmail(e string) string {
	if e == "" {
		return "Email is required."
	}
	if _, err := mail.ParseAddress(e); err != nil {
		return "That doesn't look like a valid email address."
	}
	return ""
}

func ValidatePassword(p string) string {
	if len(p) < PasswordMinLen {
		return "Password must be at least 12 characters."
	}
	return ""
}

func ValidateFirstName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < NameMinLen || len(s) > NameMaxLen {
		return "First name must be 1–50 characters."
	}
	return ""
}

func ValidateLastName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < NameMinLen || len(s) > NameMaxLen {
		return "Last name must be 1–50 characters."
	}
	return ""
}

func ValidateBio(b string) string {
	if len(b) > BioMaxLen {
		return "Bio must be 1000 characters or fewer."
	}
	return ""
}

func ValidatePronouns(p string) string {
	if len(p) > PronounsMaxLen {
		return "Pronouns must be 50 characters or fewer."
	}
	return ""
}
