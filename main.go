package main

import (
	"context"
	"log"

	"github.com/elephant-evergreen/terraform-provider-cloudflarecontrols/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

var version = "dev"

func main() {
	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/elephant-evergreen/cloudflarecontrols",
	})
	if err != nil {
		log.Fatal(err)
	}
}
