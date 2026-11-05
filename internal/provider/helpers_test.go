package provider

import (
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timetypes/timetypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
)

func TestParseID(t *testing.T) {
	good := map[string]int64{"42": 42, "1": 1, "9007199254740993": 9007199254740993}
	for in, want := range good {
		var diags diag.Diagnostics
		got, ok := parseID(path.Root("id"), in, &diags)
		if !ok || got != want || diags.HasError() {
			t.Errorf("parseID(%q) = %d, %v, %v; want %d, true, no diagnostics", in, got, ok, diags, want)
		}
	}

	// Positive decimal integers only: zero is never a valid server or template id.
	for _, in := range []string{"x", "-1", "0", "", " 42", "42 ", "+5", "007", "1.5", "0x10", "99999999999999999999"} {
		var diags diag.Diagnostics
		got, ok := parseID(path.Root("id"), in, &diags)
		if ok || got != 0 {
			t.Errorf("parseID(%q) = %d, %v; want 0, false", in, got, ok)
		}
		if !diags.HasError() {
			t.Fatalf("parseID(%q): no diagnostic", in)
		}
		want := fmt.Sprintf("id must be a decimal integer, got %q", in)
		if d := diags.Errors()[0].Detail(); d != want {
			t.Errorf("parseID(%q) detail = %q, want %q", in, d, want)
		}
	}
}

func TestIDString(t *testing.T) {
	if got := idString(42); got != "42" {
		t.Errorf("idString(42) = %q", got)
	}
}

func TestRFC3339RoundTrip(t *testing.T) {
	want := time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)

	v := rfc3339(&want)
	if v.IsNull() || v.IsUnknown() || v.ValueString() != "2026-11-05T06:00:00Z" {
		t.Fatalf("rfc3339 = %v, want 2026-11-05T06:00:00Z", v)
	}
	got, diags := timeFrom(v)
	if diags.HasError() || got == nil || !got.Equal(want) {
		t.Fatalf("timeFrom = %v, %v; want %v", got, diags, want)
	}

	// A non-UTC value is written in UTC and compares equal to its UTC form.
	plus1 := time.Date(2026, 11, 5, 7, 0, 0, 0, time.FixedZone("", 3600))
	if s := rfc3339(&plus1).ValueString(); s != "2026-11-05T06:00:00Z" {
		t.Errorf("rfc3339(+01:00) = %q, want the UTC form", s)
	}
	off, diags := timeFrom(timetypes.NewRFC3339ValueMust("2026-11-05T07:00:00+01:00"))
	if diags.HasError() || off == nil || !off.Equal(want) {
		t.Errorf("timeFrom(+01:00) = %v, %v; want the instant %v", off, diags, want)
	}

	// nil <-> null.
	if n := rfc3339(nil); !n.IsNull() {
		t.Errorf("rfc3339(nil) = %v, want null", n)
	}
	for name, in := range map[string]timetypes.RFC3339{
		"null": timetypes.NewRFC3339Null(), "unknown": timetypes.NewRFC3339Unknown(),
	} {
		tm, diags := timeFrom(in)
		if tm != nil || diags.HasError() {
			t.Errorf("timeFrom(%s) = %v, %v; want nil and no diagnostics", name, tm, diags)
		}
	}
}
