package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
)

const muteAddr = "monitorable_server_mute.test"

// muteConfig is a server and a mute on it. Empty until and reason are omitted.
func muteConfig(serverName, until, reason string) string {
	return serverConfig(serverName) + muteBlock("monitorable_server.test.id", until, reason)
}

// muteConfigFor mutes a server that already exists.
func muteConfigFor(serverID int64, until, reason string) string {
	return muteBlock(fmt.Sprintf("%q", idString(serverID)), until, reason)
}

func muteBlock(serverRef, until, reason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"monitorable_server_mute\" \"test\" {\n  server_id = %s\n", serverRef)
	if until != "" {
		fmt.Fprintf(&b, "  until     = %q\n", until)
	}
	if reason != "" {
		fmt.Fprintf(&b, "  reason    = %q\n", reason)
	}
	b.WriteString("}\n")
	return b.String()
}

// inHours is a whole-second instant hours ahead, as RFC 3339 UTC.
func inHours(h int) string {
	return time.Now().UTC().Truncate(time.Second).Add(time.Duration(h) * time.Hour).Format(time.RFC3339)
}

// checkServerMute asserts what the API shows for the server of addr.
func checkServerMute(t *testing.T, addr string, muted bool, until, reason string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id, err := serverIDOf(s, addr)
		if err != nil {
			return err
		}
		srv, err := testAccClient(t).GetServer(context.Background(), id)
		if err != nil {
			return err
		}
		if srv.AlertsMuted != muted {
			return fmt.Errorf("server %d alerts_muted = %v, want %v", id, srv.AlertsMuted, muted)
		}
		switch {
		case until == "" && srv.AlertsMutedUntil != nil:
			return fmt.Errorf("server %d alerts_muted_until = %v, want null", id, srv.AlertsMutedUntil)
		case until != "":
			want, err := time.Parse(time.RFC3339Nano, until)
			if err != nil {
				return err
			}
			if srv.AlertsMutedUntil == nil || srv.AlertsMutedUntil.Sub(want).Abs() > time.Microsecond {
				return fmt.Errorf("server %d alerts_muted_until = %v, want %s", id, srv.AlertsMutedUntil, until)
			}
		}
		if srv.AlertsMutedReason != reason {
			return fmt.Errorf("server %d alerts_muted_reason = %q, want %q", id, srv.AlertsMutedReason, reason)
		}
		return nil
	}
}

// mutePosts counts the mute POSTs the shared fake saw for a server.
func mutePosts(id string) int {
	n := 0
	for _, r := range accFake.Requests() {
		if r.Method == "POST" && r.Path == "/v1/servers/"+id+"/alerts:mute" {
			n++
		}
	}
	return n
}

func TestServerMuteWindow(t *testing.T) {
	testAccPreCheck(t)
	name := testAccName("mute-window")
	until := inHours(1)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{{
			Config: muteConfig(name, until, "deploy"),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPair(muteAddr, "id", serverAddr, "id"),
				resource.TestCheckResourceAttrPair(muteAddr, "server_id", serverAddr, "id"),
				resource.TestCheckResourceAttr(muteAddr, "until", until),
				resource.TestCheckResourceAttr(muteAddr, "reason", "deploy"),
				resource.TestCheckResourceAttr(muteAddr, "active", "true"),
				checkServerMute(t, muteAddr, true, until, "deploy"),
			),
		}},
	})
}

// Destroying the mute unmutes the server (the server outlives it here).
func TestServerMuteDestroyUnmutes(t *testing.T) {
	skipIfRealEndpoint(t)
	id := seedServer(t, "mute-destroy")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			srv, err := testAccClient(t).GetServer(context.Background(), id)
			if err != nil {
				return err
			}
			if srv.AlertsMuted {
				return fmt.Errorf("server %d is still muted after destroy", id)
			}
			return nil
		},
		Steps: []resource.TestStep{{
			Config: muteConfigFor(id, "", "maintenance"),
			Check: func(*terraform.State) error {
				srv, err := testAccClient(t).GetServer(context.Background(), id)
				if err != nil {
					return err
				}
				if !srv.AlertsMuted {
					return fmt.Errorf("server %d is not muted after apply", id)
				}
				return nil
			},
		}},
	})
}

func TestServerMuteIndefinite(t *testing.T) {
	testAccPreCheck(t)
	name := testAccName("mute-indef")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{
			{
				Config: muteConfig(name, "", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(muteAddr, "until"),
					resource.TestCheckNoResourceAttr(muteAddr, "reason"),
					resource.TestCheckResourceAttr(muteAddr, "active", "true"),
					checkServerMute(t, muteAddr, true, "", ""),
				),
			},
			{Config: muteConfig(name, "", ""), PlanOnly: true},
		},
	})
}

// Review Focus 2: a mute whose window has passed stays in state as completed
// and is not re-applied. The API reports the server unmuted once until has
// passed, and the plan must stay empty instead of muting again.
func TestServerMuteExpiredStaysInState(t *testing.T) {
	skipIfRealEndpoint(t)
	name := testAccName("mute-expired")
	// Far enough ahead for a cold Terraform start-up and the API's "in the future"
	// check, near enough to wait for.
	until := time.Now().UTC().Truncate(time.Second).Add(15 * time.Second)
	cfg := muteConfig(name, until.Format(time.RFC3339), "deploy")
	var serverID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(muteAddr, "active", "true"),
					captureID(serverAddr, &serverID),
				),
			},
			{
				PreConfig: func() {
					time.Sleep(time.Until(until.Add(time.Second)))
					var n int64
					fmt.Sscanf(serverID, "%d", &n)
					accFake.SetMuted(n, nil, "") // the backend's sweep clears the expired mute
				},
				Config:   cfg,
				PlanOnly: true, // empty: PlanOnly fails on any change
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(muteAddr, "active", "false"),
					resource.TestCheckResourceAttr(muteAddr, "until", until.Format(time.RFC3339)),
					resource.TestCheckResourceAttr(muteAddr, "reason", "deploy"),
					checkServerMute(t, muteAddr, false, "", ""),
					func(*terraform.State) error {
						if n := mutePosts(serverID); n != 1 {
							return fmt.Errorf("the server was muted %d times, want 1 (the expired window must not be re-applied)", n)
						}
						return nil
					},
				),
			},
		},
	})
}

// A mute lifted early in the dashboard (or one that was never ours) is not a
// completed window: the plan mutes again.
func TestServerMuteLiftedInDashboardRecreates(t *testing.T) {
	skipIfRealEndpoint(t)
	name := testAccName("mute-lifted")
	var serverID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{
			{
				Config: muteConfig(name, "", "deploy"),
				Check:  captureID(serverAddr, &serverID),
			},
			{
				PreConfig: func() {
					var n int64
					fmt.Sscanf(serverID, "%d", &n)
					accFake.SetMuted(n, nil, "")
				},
				Config:             muteConfig(name, "", "deploy"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectResourceAction(muteAddr, plancheck.ResourceActionCreate)},
				},
			},
		},
	})
}

// A window lifted early (until still ahead, server unmuted) is also re-created.
func TestServerMuteWindowLiftedEarlyRecreates(t *testing.T) {
	skipIfRealEndpoint(t)
	name := testAccName("mute-lifted-win")
	until := inHours(2)
	var serverID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{
			{
				Config: muteConfig(name, until, ""),
				Check:  captureID(serverAddr, &serverID),
			},
			{
				PreConfig: func() {
					var n int64
					fmt.Sscanf(serverID, "%d", &n)
					accFake.SetMuted(n, nil, "")
				},
				Config:             muteConfig(name, until, ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectResourceAction(muteAddr, plancheck.ResourceActionCreate)},
				},
			},
		},
	})
}

// The mute changed in the dashboard (same server, still muted, other window or
// reason): Read takes the API's values, so the plan replaces the mute.
func TestServerMuteChangedInDashboardReplaces(t *testing.T) {
	skipIfRealEndpoint(t)
	name := testAccName("mute-changed")
	var serverID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{
			{
				Config: muteConfig(name, "", "deploy"),
				Check:  captureID(serverAddr, &serverID),
			},
			{
				PreConfig: func() {
					var n int64
					fmt.Sscanf(serverID, "%d", &n)
					u := time.Now().Add(3 * time.Hour)
					accFake.SetMuted(n, &u, "someone else")
				},
				Config:             muteConfig(name, "", "deploy"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectResourceAction(muteAddr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
			},
		},
	})
}

func TestServerMutePastUntilRefused(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{{
			Config:      muteConfig(testAccName("mute-past"), "2020-01-01T00:00:00Z", ""),
			ExpectError: regexp.MustCompile(`invalid_argument`),
		}},
	})
}

// Timestamps compare as instants: an offset spelling of the same moment is no
// diff, and respelling it later is an in-place no-op, not a replacement.
func TestServerMuteTimezoneNoDiff(t *testing.T) {
	testAccPreCheck(t)
	name := testAccName("mute-tz")
	at := time.Now().UTC().Truncate(time.Second).Add(2 * time.Hour)
	plus1 := at.In(time.FixedZone("", 3600)).Format(time.RFC3339)
	utc := at.Format(time.RFC3339)
	var serverID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{
			{
				Config: muteConfig(name, plus1, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					checkServerMute(t, muteAddr, true, utc, ""),
					captureID(serverAddr, &serverID),
				),
			},
			{Config: muteConfig(name, plus1, ""), PlanOnly: true},
			{
				// The same instant spelled in UTC: updated in place, no new mute.
				Config: muteConfig(name, utc, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(muteAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr(muteAddr, "until", utc),
			},
			{Config: muteConfig(name, utc, ""), PlanOnly: true},
		},
	})
}

// Postgres keeps microseconds, so a nanosecond-precision until reads back
// rounded; the provider keeps the configured value and the plan stays empty.
func TestServerMuteNanosecondUntilNoDiff(t *testing.T) {
	testAccPreCheck(t)
	name := testAccName("mute-nanos")
	until := time.Now().UTC().Truncate(time.Second).Add(2*time.Hour + 123456789*time.Nanosecond).Format(time.RFC3339Nano)
	cfg := muteConfig(name, until, "")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(muteAddr, "until", until),
					checkServerMute(t, muteAddr, true, until, ""),
				),
			},
			{Config: cfg, PlanOnly: true},
		},
	})
}

// A muted server imports with its window and reason.
func TestServerMuteImport(t *testing.T) {
	id := seedServer(t, "mute-import")
	until := inHours(3)
	until2 := inHours(4)
	c := testAccClient(t)
	mute := func(until, reason string) {
		u, err := time.Parse(time.RFC3339, until)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.MuteServerAlerts(context.Background(), id, &u, reason); err != nil {
			t.Fatalf("muting server %d: %v", id, err)
		}
	}
	mute(until, "migration")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             muteConfigFor(id, until, "migration"),
				ResourceName:       muteAddr,
				ImportState:        true,
				ImportStateId:      idString(id),
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d resources, want 1", len(states))
					}
					a := states[0].Attributes
					if a["id"] != idString(id) || a["server_id"] != idString(id) || a["until"] != until || a["reason"] != "migration" || a["active"] != "true" {
						return fmt.Errorf("imported %v", a)
					}
					return nil
				},
			},
			{Config: muteConfigFor(id, until, "migration"), PlanOnly: true},
			{
				Config:            muteConfigFor(id, until, "migration"),
				ResourceName:      muteAddr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// A different window is a replacement, not an in-place change.
				PreConfig: func() { mute(until2, "migration") },
				Config:    muteConfigFor(id, until, "migration"),
				PlanOnly:  true, ExpectNonEmptyPlan: true,
			},
		},
	})
}

// An indefinite mute without a reason imports with both null.
func TestServerMuteImportIndefinite(t *testing.T) {
	id := seedServer(t, "mute-import-indef")
	if _, err := testAccClient(t).MuteServerAlerts(context.Background(), id, nil, ""); err != nil {
		t.Fatalf("muting server %d: %v", id, err)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             muteConfigFor(id, "", ""),
				ResourceName:       muteAddr,
				ImportState:        true,
				ImportStateId:      idString(id),
				ImportStatePersist: true,
			},
			{Config: muteConfigFor(id, "", ""), PlanOnly: true},
		},
	})
}

// A server that is not muted has no mute to import.
func TestServerMuteImportNotMutedRefused(t *testing.T) {
	id := seedServer(t, "mute-import-none")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:        muteConfigFor(id, "", ""),
			ResourceName:  muteAddr,
			ImportState:   true,
			ImportStateId: idString(id),
			ExpectError:   regexp.MustCompile(fmt.Sprintf(`server\s+%d\s+is\s+not\s+muted;\s+there\s+is\s+no\s+mute\s+to\s+import`, id)),
		}},
	})
}

func TestServerMuteImportInvalidID(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:        muteConfigFor(1, "", ""),
			ResourceName:  muteAddr,
			ImportState:   true,
			ImportStateId: "web-1",
			ExpectError:   regexp.MustCompile(`id must be a decimal integer, got "web-1"`),
		}},
	})
}

func TestServerMuteValidation(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `resource "monitorable_server_mute" "test" {
  server_id = "web-1"
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`id\s+must\s+be\s+a\s+decimal\s+integer`),
			},
			{
				Config:      muteConfigFor(1, "tomorrow", ""),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?i)RFC3339|RFC\s+3339`),
			},
			{
				Config:      muteConfigFor(1, "", " deploy"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`reason\s+must\s+not\s+start\s+or\s+end\s+with\s+whitespace`),
			},
		},
	})
}

func muteSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &fwresource.SchemaResponse{}
	NewServerMuteResource().Schema(context.Background(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// The reason validators match what the backend accepts: at most 200
// characters (not bytes), no surrounding whitespace (it trims), no control
// characters (it refuses them).
func TestServerMuteReasonValidators(t *testing.T) {
	vs := muteSchema(t).Attributes["reason"].(schema.StringAttribute).Validators
	cases := []struct {
		reason string
		ok     bool
	}{
		{"deploy 42", true},
		{"", true},
		{strings.Repeat("é", 200), true},
		{strings.Repeat("界", 200), true},
		{strings.Repeat("a", 201), false},
		{strings.Repeat("é", 201), false},
		{" deploy", false},
		{"deploy ", false},
		{"\tdeploy", false},
		{"deploy\n", false},
		{" deploy", false},
		{"dep\nloy", false},
		{"dep\x00loy", false},
		{"dep\x7floy", false},
	}
	for _, tc := range cases {
		var diags diag.Diagnostics
		for _, v := range vs {
			resp := &validator.StringResponse{}
			v.ValidateString(context.Background(), validator.StringRequest{
				Path: path.Root("reason"), ConfigValue: types.StringValue(tc.reason),
			}, resp)
			diags.Append(resp.Diagnostics...)
		}
		if got := !diags.HasError(); got != tc.ok {
			t.Errorf("reason %q: valid = %v, want %v (%v)", tc.reason, got, tc.ok, diags)
		}
	}
}

func TestServerMuteSchemaShape(t *testing.T) {
	s := muteSchema(t)
	for k, want := range map[string]struct{ required, optional, computed bool }{
		"server_id": {required: true},
		"until":     {optional: true},
		"reason":    {optional: true},
		"id":        {computed: true},
		"active":    {computed: true},
	} {
		a, ok := s.Attributes[k]
		if !ok {
			t.Fatalf("schema has no %s", k)
		}
		if a.IsRequired() != want.required || a.IsOptional() != want.optional || a.IsComputed() != want.computed {
			t.Errorf("%s: required=%v optional=%v computed=%v", k, a.IsRequired(), a.IsOptional(), a.IsComputed())
		}
	}
	const desc = "A maintenance mute on a server's alerts. Set `until` for a window; omit it to mute until the resource is destroyed. " +
		"A mute whose `until` has passed stays in state as completed and is not re-applied; change `until` to open a new window. " +
		"Destroying the resource unmutes the server."
	if s.Description != desc || s.MarkdownDescription != desc {
		t.Errorf("description = %q", s.Description)
	}
}
