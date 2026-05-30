package render

// M is a convenience alias for template data maps.
type M = map[string]any

// Page merges a user value and page-specific fields into one map so that
// base.html always has a "User" key regardless of which handler is rendering.
// Pass nil for user when the page is always unauthenticated (e.g. login).
func Page(user any, fields M) M {
	if fields == nil {
		fields = M{}
	}
	fields["User"] = user
	return fields
}
