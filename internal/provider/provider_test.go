package provider

import (
	"context"
	"testing"

	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

func TestProviderMetadata(t *testing.T) {
	p := New("1.2.3")()
	resp := &fwprovider.MetadataResponse{}
	p.Metadata(context.Background(), fwprovider.MetadataRequest{}, resp)
	if resp.TypeName != "monitorable" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "monitorable")
	}
	if resp.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", resp.Version, "1.2.3")
	}
}

func TestProviderServes(t *testing.T) {
	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatalf("NewProtocol6WithError: %v", err)
	}
	if server == nil {
		t.Fatal("protocol 6 server is nil")
	}
}
