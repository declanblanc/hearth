package render

import (
	"testing"
	"time"
)

// callStringFunc pulls a string-returning template helper out of the funcMap and
// invokes it with the given time, failing the test if the helper is missing or
// has an unexpected signature.
func callStringFunc(t *testing.T, name string, arg time.Time) string {
	t.Helper()
	raw, ok := funcMap()[name]
	if !ok {
		t.Fatalf("funcMap missing %q helper", name)
	}
	fn, ok := raw.(func(time.Time) string)
	if !ok {
		t.Fatalf("%q helper has unexpected type %T", name, raw)
	}
	return fn(arg)
}

func TestDatetimeRendersUTCFallback(t *testing.T) {
	// 8:08 PM US/Eastern is the scenario from the bug report. Regardless of the
	// location attached to the time value, the fallback must render the same
	// instant in UTC and label it as such, so a non-JS viewer is never misled.
	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("loading timezone: %v", err)
	}
	ts := time.Date(2026, time.June, 7, 20, 8, 0, 0, eastern) // 20:08 EDT == 00:08 UTC next day

	// Viewed within the same year, the year is dropped for compactness.
	sameYear := time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC)
	if got, want := humanDatetime(ts, sameYear), "Jun 8, 12:08 AM UTC"; got != want {
		t.Errorf("humanDatetime(%v, same year) = %q, want %q", ts, got, want)
	}

	// Viewed in a later year, the year is kept so the date stays unambiguous.
	laterYear := time.Date(2027, time.January, 2, 0, 0, 0, 0, time.UTC)
	if got, want := humanDatetime(ts, laterYear), "Jun 8, 2026, 12:08 AM UTC"; got != want {
		t.Errorf("humanDatetime(%v, later year) = %q, want %q", ts, got, want)
	}
}

func TestDatetimeZeroIsEmpty(t *testing.T) {
	if got := humanDatetime(time.Time{}, time.Now()); got != "" {
		t.Errorf("humanDatetime(zero) = %q, want empty string", got)
	}
}

func TestISODatetimeIsMachineReadableUTC(t *testing.T) {
	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("loading timezone: %v", err)
	}
	ts := time.Date(2026, time.June, 7, 20, 8, 0, 0, eastern)

	got := callStringFunc(t, "isodatetime", ts)
	want := "2026-06-08T00:08:00Z"
	if got != want {
		t.Errorf("isodatetime(%v) = %q, want %q", ts, got, want)
	}

	// The output must round-trip through the standard ISO-8601 parser so the
	// browser's Date() (and localtime.js) can convert it reliably.
	parsed, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("isodatetime output %q is not valid RFC3339: %v", got, err)
	}
	if !parsed.Equal(ts) {
		t.Errorf("round-tripped instant = %v, want %v", parsed, ts)
	}
}

func TestISODatetimeZeroIsEmpty(t *testing.T) {
	if got := callStringFunc(t, "isodatetime", time.Time{}); got != "" {
		t.Errorf("isodatetime(zero) = %q, want empty string", got)
	}
}
