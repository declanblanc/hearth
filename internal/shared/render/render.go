// Package render wraps html/template parsing and provides helpers for writing
// HTML and 422 form-error responses (htmx-friendly).
//
// Templates are organised as one file per page plus a shared base.html. Each
// page declares a {{define "content"}} block that base.html renders. Because
// html/template's template set is global per *Template, we parse a fresh
// (base + page) set every time a page is rendered, keyed by page filename.
// This is slightly slower than caching one mega-set but it's the only way to
// keep "content" overrides isolated per page.
package render

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type Renderer struct {
	fs    fs.FS
	dev   bool
	mu    sync.RWMutex
	cache map[string]*template.Template
}

func New(templates fs.FS, dev bool) (*Renderer, error) {
	r := &Renderer{fs: templates, dev: dev, cache: map[string]*template.Template{}}
	if !dev {
		if err := r.warm(); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func NewFromEmbed(efs embed.FS, root string, dev bool) (*Renderer, error) {
	sub, err := fs.Sub(efs, root)
	if err != nil {
		return nil, err
	}
	return New(sub, dev)
}

func (r *Renderer) warm() error {
	return fs.WalkDir(r.fs, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path == "base.html" {
			return err
		}
		_, perr := r.parsePage(path)
		return perr
	})
}

func (r *Renderer) parsePage(name string) (*template.Template, error) {
	t := template.New("").Funcs(funcMap())
	baseSrc, err := fs.ReadFile(r.fs, "base.html")
	if err == nil {
		if _, err := t.New("base.html").Parse(string(baseSrc)); err != nil {
			return nil, fmt.Errorf("render: parse base.html: %w", err)
		}
	}
	pageSrc, err := fs.ReadFile(r.fs, name)
	if err != nil {
		return nil, fmt.Errorf("render: read %s: %w", name, err)
	}
	if _, err := t.New(name).Parse(string(pageSrc)); err != nil {
		return nil, fmt.Errorf("render: parse %s: %w", name, err)
	}
	if !r.dev {
		r.mu.Lock()
		r.cache[name] = t
		r.mu.Unlock()
	}
	return t, nil
}

func (r *Renderer) lookup(name string) (*template.Template, error) {
	if !r.dev {
		r.mu.RLock()
		t, ok := r.cache[name]
		r.mu.RUnlock()
		if ok {
			return t, nil
		}
	}
	return r.parsePage(name)
}

func (r *Renderer) HTML(w http.ResponseWriter, name string, data any) {
	r.Status(w, http.StatusOK, name, data)
}

func (r *Renderer) Status(w http.ResponseWriter, status int, name string, data any) {
	t, err := r.lookup(name)
	if err != nil {
		slog.Error("render: lookup", "template", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("render: execute", "template", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// Error renders the generic error page. Never leaks stack traces.
func (r *Renderer) Error(w http.ResponseWriter, status int) {
	t, err := r.lookup("error.html")
	if err != nil {
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = t.ExecuteTemplate(w, "error.html", map[string]any{"Status": status, "Message": http.StatusText(status)})
}

// humanDatetime formats t as a compact human-readable UTC string. The year is
// dropped when t falls in the same year as now — the common case in a feed of
// recent posts — and kept only for older cross-year timestamps, so the date
// stays unambiguous without wasting space on a redundant year. Returns "" for
// the zero time. now is passed in (rather than read from the clock) so the
// format is deterministic under test.
func humanDatetime(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.UTC()
	layout := "Jan 2, 3:04 PM"
	if t.Year() != now.UTC().Year() {
		layout = "Jan 2, 2006, 3:04 PM"
	}
	return t.Format(layout) + " UTC"
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"safe": func(s string) template.HTML { return template.HTML(s) }, //nolint:gosec
		// datetime renders a human-readable fallback for a timestamp, e.g.
		// "Jan 2, 3:04 PM UTC" (or with the year for older posts). Timestamps
		// are stored in UTC, so we format in UTC and label it as such — this is
		// the text shown when the client-side localtime.js enhancement does not
		// run. When it does run, this text is replaced with the value converted
		// to the viewer's local timezone (see web/static/localtime.js).
		"datetime": func(t time.Time) string {
			return humanDatetime(t, time.Now())
		},
		// isodatetime renders a machine-readable ISO-8601 timestamp in UTC,
		// suitable for a <time datetime="..."> attribute. The trailing "Z"
		// marks it as UTC so the browser (and localtime.js) can parse and
		// convert it to the viewer's local timezone unambiguously.
		"isodatetime": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.UTC().Format(time.RFC3339)
		},
	}
}
