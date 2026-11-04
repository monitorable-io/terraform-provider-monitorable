package provider

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timetypes/timetypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client"
)

// parseID converts a Terraform id string to the API's integer id. Only positive
// decimal integers in canonical form are ids ("42"; not "0", "-1", "+5", "007"
// or " 42"). On failure it adds an attribute error at p and returns false.
func parseID(p path.Path, s string, diags *diag.Diagnostics) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != s {
		diags.AddAttributeError(p, "Invalid id", fmt.Sprintf("id must be a decimal integer, got %q", s))
		return 0, false
	}
	return n, true
}

// decimalIDValidator accepts the canonical decimal form of an API id and
// refuses everything else at plan time, with parseID's message. Unknown values
// (a reference to a resource not yet created) pass.
type decimalIDValidator struct{}

const decimalIDDescription = "must be the decimal integer id of an existing object"

func (decimalIDValidator) Description(context.Context) string { return decimalIDDescription }

func (v decimalIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (decimalIDValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	parseID(req.Path, req.ConfigValue.ValueString(), &resp.Diagnostics)
}

// idString is the Terraform form of an API id.
func idString(id int64) string { return strconv.FormatInt(id, 10) }

// rfc3339 is the Terraform value of an optional API timestamp: null when nil,
// otherwise the instant in UTC.
func rfc3339(t *time.Time) timetypes.RFC3339 {
	if t == nil {
		return timetypes.NewRFC3339Null()
	}
	return timetypes.NewRFC3339TimeValue(t.UTC())
}

// timeFrom is the API timestamp of a Terraform value: nil for null and for
// unknown (an unknown value has no instant yet).
func timeFrom(v timetypes.RFC3339) (*time.Time, diag.Diagnostics) {
	if v.IsNull() || v.IsUnknown() {
		return nil, nil
	}
	t, diags := v.ValueRFC3339Time()
	if diags.HasError() {
		return nil, diags
	}
	return &t, diags
}

// clientFromProviderData is the shared Configure step of every data source
// and resource: nil provider data (the provider is not configured yet) gives a
// nil client without an error.
func clientFromProviderData(data any, diags *diag.Diagnostics) *client.Client {
	if data == nil {
		return nil
	}
	c, ok := data.(*client.Client)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T", data))
		return nil
	}
	return c
}

// idList renders ids ascending and comma-separated: "3, 7".
func idList(ids []int64) string {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	parts := make([]string, len(sorted))
	for i, id := range sorted {
		parts[i] = idString(id)
	}
	return strings.Join(parts, ", ")
}
