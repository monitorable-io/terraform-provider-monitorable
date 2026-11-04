package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/provider"
)

// version is set at release time: -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	debugFlag := flag.Bool("debug", false, "run the provider with support for debuggers such as delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/monitorable-io/monitorable",
		Debug:   *debugFlag,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
