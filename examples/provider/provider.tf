terraform {
  required_providers {
    cloudflarecontrols = {
      source  = "elephant-evergreen/cloudflarecontrols"
      version = "= 0.0.1-alpha.1"
    }
  }
}

provider "cloudflarecontrols" {}
