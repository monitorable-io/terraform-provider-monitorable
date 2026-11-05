package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client/fake"
)

const channelsDSAddr = "data.monitorable_notification_channels.test"

func TestNotificationChannelsDataSource(t *testing.T) {
	_, prov := newTestFake(t, fake.Options{})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: prov + "data \"monitorable_notification_channels\" \"test\" {\n}\n",
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(channelsDSAddr, "channels.#", "2"),
				resource.TestCheckResourceAttr(channelsDSAddr, "channels.0.id", "1"),
				resource.TestCheckResourceAttr(channelsDSAddr, "channels.0.type", "email"),
				resource.TestCheckResourceAttr(channelsDSAddr, "channels.0.name", "Ops"),
				resource.TestCheckResourceAttr(channelsDSAddr, "channels.0.enabled", "true"),
				resource.TestCheckResourceAttr(channelsDSAddr, "channels.1.id", "2"),
				resource.TestCheckResourceAttr(channelsDSAddr, "channels.1.type", "slack"),
				resource.TestCheckResourceAttr(channelsDSAddr, "channels.1.name", "Alerts"),
			),
		}},
	})
}
