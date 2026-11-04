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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client"
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

// fakeMuteCheck is a plan check that only runs fn: it lets a test change the
// fake between Terraform's plan and its apply.
type fakeMuteCheck func()

func (f fakeMuteCheck) CheckPlan(context.Context, plancheck.CheckPlanRequest, *plancheck.CheckPlanResponse) {
	f()
}

// Under create_before_destroy the replacement mute is created before the old
// instance is deleted. Deleting the old one must not lift the new window.
func TestServerMuteCreateBeforeDestroyKeepsReplacement(t *testing.T) {
	skipIfRealEndpoint(t)
	id := seedServer(t, "mute-cbd")
	first, second := inHours(1), inHours(2)
	cfg := func(until string) string {
		return fmt.Sprintf(`resource "monitorable_server_mute" "test" {
  server_id = %q
  until     = %q
  reason    = "deploy"

  lifecycle {
    create_before_destroy = true
  }
}
`, idString(id), until)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg(first), Check: checkServerMute(t, muteAddr, true, first, "deploy")},
			{
				Config: cfg(second),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(muteAddr, plancheck.ResourceActionCreateBeforeDestroy)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(muteAddr, "until", second),
					resource.TestCheckResourceAttr(muteAddr, "active", "true"),
					checkServerMute(t, muteAddr, true, second, "deploy"),
				),
			},
			{Config: cfg(second), PlanOnly: true},
		},
	})
}

// A mute set in the dashboard between Terraform's plan and its apply is not
// the resource's: removing the resource from the configuration (which destroys
// it) leaves that mute in place.
func TestServerMuteDestroyLeavesForeignMute(t *testing.T) {
	skipIfRealEndpoint(t)
	id := seedServer(t, "mute-foreign")
	other := time.Now().UTC().Add(3 * time.Hour)
	// The data source keeps the configuration non-empty once the mute is gone.
	const keep = "data \"monitorable_test_me\" \"keep\" {}\n"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: keep + muteConfigFor(id, "", "deploy"),
				Check:  resource.TestCheckResourceAttr(muteAddr, "active", "true"),
			},
			{
				Config: keep,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(muteAddr, plancheck.ResourceActionDestroy),
						fakeMuteCheck(func() { accFake.SetMuted(id, &other, "someone else") }),
					},
				},
				Check: func(*terraform.State) error {
					srv, err := testAccClient(t).GetServer(context.Background(), id)
					if err != nil {
						return err
					}
					if !srv.AlertsMuted || srv.AlertsMutedReason != "someone else" {
						return fmt.Errorf("server %d: muted=%v reason=%q after destroy; the dashboard's mute must survive",
							id, srv.AlertsMuted, srv.AlertsMutedReason)
					}
					return nil
				},
			},
		},
	})
}

// muteIsOurs decides whether Delete unmutes.
func TestMuteIsOurs(t *testing.T) {
	u := time.Date(2026, 11, 5, 6, 0, 0, 123456789, time.UTC)
	at := func(d time.Duration) *time.Time { v := u.Add(d); return &v }
	srv := func(muted bool, until *time.Time, reason string) *client.Server {
		return &client.Server{AlertsMuted: muted, AlertsMutedUntil: until, AlertsMutedReason: reason}
	}
	cases := []struct {
		name   string
		until  *time.Time
		reason string
		s      *client.Server
		want   bool
	}{
		{"same window", at(0), "deploy", srv(true, at(0), "deploy"), true},
		{"rounded to the microsecond", at(0), "deploy", srv(true, at(-789), "deploy"), true},
		{"indefinite, null reason", nil, "", srv(true, nil, ""), true},
		{"not muted", at(0), "deploy", srv(false, nil, ""), false},
		{"another window", at(0), "deploy", srv(true, at(time.Hour), "deploy"), false},
		{"another reason", at(0), "deploy", srv(true, at(0), "someone else"), false},
		{"window where state is indefinite", nil, "", srv(true, at(0), ""), false},
		{"indefinite where state has a window", at(0), "", srv(true, nil, ""), false},
	}
	for _, tc := range cases {
		if got := muteIsOurs(tc.until, tc.reason, tc.s); got != tc.want {
			t.Errorf("%s: muteIsOurs = %v, want %v", tc.name, got, tc.want)
		}
	}
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
				// Wait for the window to pass. Nothing is changed on the fake: like the
				// backend it computes expiry when the server is read, so the API now
				// reports the server unmuted.
				PreConfig: func() { time.Sleep(time.Until(until.Add(time.Second))) },
				Config:    cfg,
				PlanOnly:  true, // empty: PlanOnly fails on any change
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

// R21: once the window has passed the resource is inert. A mute someone else
// sets on the server afterwards is theirs: Read leaves until and reason alone,
// the plan stays empty, and destroying the resource does not lift it.
func TestServerMuteCompletedWindowIsInert(t *testing.T) {
	skipIfRealEndpoint(t)
	id := seedServer(t, "mute-inert")
	until := time.Now().UTC().Truncate(time.Second).Add(15 * time.Second)
	cfg := muteConfigFor(id, until.Format(time.RFC3339), "deploy")
	foreignUntil := time.Now().UTC().Add(3 * time.Hour)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			srv, err := testAccClient(t).GetServer(context.Background(), id)
			if err != nil {
				return err
			}
			if !srv.AlertsMuted || srv.AlertsMutedReason != "other" {
				return fmt.Errorf("server %d: muted=%v reason=%q after destroy; the dashboard's mute must survive",
					id, srv.AlertsMuted, srv.AlertsMutedReason)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: cfg, Check: resource.TestCheckResourceAttr(muteAddr, "active", "true")},
			{
				PreConfig: func() {
					time.Sleep(time.Until(until.Add(time.Second)))
					accFake.SetMuted(id, &foreignUntil, "other")
				},
				Config:   cfg,
				PlanOnly: true, // the foreign mute does not plan a replacement
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					// active mirrors the API; until and reason are still ours.
					resource.TestCheckResourceAttr(muteAddr, "active", "true"),
					resource.TestCheckResourceAttr(muteAddr, "until", until.Format(time.RFC3339)),
					resource.TestCheckResourceAttr(muteAddr, "reason", "deploy"),
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
					accFake.SetMuted(mustParseInt64(t, serverID), nil, "")
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
					accFake.SetMuted(mustParseInt64(t, serverID), nil, "")
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
					u := time.Now().Add(3 * time.Hour)
					accFake.SetMuted(mustParseInt64(t, serverID), &u, "someone else")
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
		"Destroying the resource unmutes the server. Declare at most one `monitorable_server_mute` per server."
	if s.Description != desc || s.MarkdownDescription != desc {
		t.Errorf("description = %q", s.Description)
	}
}

// Read's branch selection, including the one-minute clock-skew grace (R21).
func TestMuteReadAction(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }
	cases := []struct {
		name       string
		until      *time.Time
		apiMuted   bool
		want       muteRead
		wantReason string
	}{
		{"indefinite, muted", nil, true, readAdopt, "adopt the API's values"},
		{"indefinite, lifted in the dashboard", nil, false, readRemove, "re-mute on the next apply"},
		{"window open, muted", at(time.Hour), true, readAdopt, "adopt"},
		{"window open, lifted early", at(time.Hour), false, readRemove, "re-mute"},
		{"window ends in 30s, API already unmuted", at(30 * time.Second), false, readInert, "inside the grace: completed"},
		{"window ends in exactly the grace, API unmuted", at(muteGrace), false, readInert, "grace boundary is inclusive"},
		{"window ends in 61s, API unmuted", at(muteGrace + time.Second), false, readRemove, "outside the grace: lifted early"},
		{"window ends in 30s, API still muted", at(30 * time.Second), true, readAdopt, "still open: adopt"},
		{"window passed, API unmuted", at(-time.Second), false, readInert, "completed"},
		{"window ends now, API unmuted", at(0), false, readInert, "until == now has passed"},
		{"window passed, someone else muted", at(-time.Hour), true, readInert, "inert: their mute is theirs"},
	}
	for _, tc := range cases {
		if got := muteReadAction(tc.until, tc.apiMuted, now); got != tc.want {
			t.Errorf("%s: action = %d, want %d (%s)", tc.name, got, tc.want, tc.wantReason)
		}
	}
}

// The until modifier uses the same microsecond tolerance as Read.
func TestUntilModifierTolerance(t *testing.T) {
	base := time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)
	cases := []struct {
		name         string
		plan, state  string
		wantReplaced bool
	}{
		{"same instant, other offset", "2026-11-05T07:00:00+01:00", "2026-11-05T06:00:00Z", false},
		{"nanoseconds apart", base.Add(400 * time.Nanosecond).Format(time.RFC3339Nano), base.Format(time.RFC3339Nano), false},
		{"a second apart", base.Add(time.Second).Format(time.RFC3339Nano), base.Format(time.RFC3339Nano), true},
	}
	for _, tc := range cases {
		raw := tftypes.NewValue(tftypes.String, "x")
		resp := &planmodifier.StringResponse{}
		requiresReplaceIfInstantChanges{}.PlanModifyString(context.Background(), planmodifier.StringRequest{
			State:      tfsdk.State{Raw: raw},
			Plan:       tfsdk.Plan{Raw: raw},
			PlanValue:  types.StringValue(tc.plan),
			StateValue: types.StringValue(tc.state),
		}, resp)
		if resp.RequiresReplace != tc.wantReplaced {
			t.Errorf("%s: RequiresReplace = %v, want %v", tc.name, resp.RequiresReplace, tc.wantReplaced)
		}
	}
}
