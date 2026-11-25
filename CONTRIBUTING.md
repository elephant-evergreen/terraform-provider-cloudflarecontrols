# Contributing

## Public information

This repository is public. Public documentation, examples, diagnostics,
issues, pull requests (including descriptions and comments), commit messages,
and release notes must not contain:

- private repository names or internal project names;
- hostnames, LAN addresses, or SSIDs;
- per-device update or verification history.

Use synthetic values in examples and fixtures. When an example or fixture
needs a network address, use one from the RFC 5737 documentation ranges, such
as `192.0.2.10`.

Document supported and tested versions in their own section. Keep per-device
update and verification history out of public content.

For Cloudflare, use synthetic account IDs, zone IDs, API tokens, and
Terraform state in tests and documentation. Do not include real account or
zone identifiers, API tokens, Terraform state, or saved plans.
