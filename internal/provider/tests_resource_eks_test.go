//go:build eks
// +build eks

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccTestsResource_EKS(t *testing.T) {
	repo := "ttl.sh/imagetest" // TODO: Don't push to ttl.sh

	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"imagetest": providerserver.NewProtocol6WithError(&ImageTestProvider{repo: repo}),
		},
		Steps: []resource.TestStep{{Config: `
resource "imagetest_tests" "foo" {
  name   = "foo"
  driver = "eks_with_eksctl"

  drivers = {
    eks_with_eksctl = {
	  enable_ebs = true
    }
  }

  images = {}

  tests = [
    {
      name    = "sample"
	  image   = "cgr.dev/chainguard/helm:latest-dev@sha256:8f39141a214a37997875bba4b77035599bf49fc1cf9b2410411a8b3dacce0ce1"
      content = [{ source = "${path.module}/testdata/TestAccTestsResource_EKS" }]
      cmd     = "/imagetest/eks-basic.sh"
    }
  ]

  // Creating the cluster takes ~15m... 🐌
  timeout = "30m"
}
`,
		}}})
}
