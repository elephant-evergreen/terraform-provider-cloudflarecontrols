# Cloudflare Account Controls

A community Terraform provider for Workers and Durable Objects spending policies
and Cloudflare account-owned API tokens. Use it alongside the [Cloudflare
provider](https://registry.terraform.io/providers/cloudflare/cloudflare/latest/docs).

## Why?

Use the Cloudflare provider for zones, DNS, Workers, and the other account
resources it covers. Add Cloudflare Account Controls for spending policies and
for account tokens that this provider issues or adopts. Both providers can
manage an account token, so keep each token under one provider.

| I want to… | Start here |
| --- | --- |
| Set a spending threshold or import an existing policy | [Spend policy](docs/resources/spend_policy.md) |
| Issue a token with specific permissions or move an existing Terraform-managed token | [Account API token](docs/resources/account_token.md) |
| Install and authenticate | [Provider setup](docs/index.md) |

An enabled spend block can pause product operations across the account. Issued
token values are stored in Terraform state. The resource references describe
what to check before applying either resource.

## Development

Install the pinned tools with `mise install`, then run `mise run check`.
See [the release procedure](RELEASE.md) for signed packages and Registry
verification.
