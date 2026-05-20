// Package web embeds the HTML templates and static assets so the production
// binary is fully self-contained.
package web

import "embed"

//go:embed all:templates
var TemplatesFS embed.FS

//go:embed all:static
var StaticFS embed.FS
