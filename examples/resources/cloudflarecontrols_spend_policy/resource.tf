resource "cloudflarecontrols_spend_policy" "workers_alert" {
  account_id         = "00000000000000000000000000000000"
  name               = "Workers spend alert"
  product_id         = "workers"
  action             = "notification"
  enabled            = true
  threshold_cents    = 2500
  notification_email = "billing@example.com"
}
