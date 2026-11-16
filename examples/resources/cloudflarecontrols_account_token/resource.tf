resource "cloudflarecontrols_account_token" "dns_reader" {
  account_id = "00000000000000000000000000000000"
  name       = "Read DNS for one zone"
  expires_on = "2030-01-01T00:00:00Z"

  policies = [{
    effect = "allow"
    permission_groups = [{
      id = "11111111111111111111111111111111"
    }]
    resources = jsonencode({
      "com.cloudflare.api.account.zone.22222222222222222222222222222222" = "*"
    })
  }]
}
