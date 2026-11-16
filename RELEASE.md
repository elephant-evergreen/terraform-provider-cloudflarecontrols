# Release Cloudflare Account Controls

Build and exercise the candidate locally before tagging. Use
`v0.0.1-alpha.1` for the initial opt-in release. Increment the prerelease
number for subsequent trials and use `v0.0.1` when the first generally
selectable version is ready.

1. Run `mise run check`. Exercise a new configuration and an account-token
   state move with fixture credentials. Keep owner credentials and states
   out of repository artifacts.
2. Add `release-notes/<tag>.md`. Note any prerequisites for existing resources
   and what was verified with fixtures or a real account.
3. Merge the reviewed changes. After Check passes on main, create an
   annotated tag on that exact commit and push the tag.
4. The Release workflow verifies and signs four builds, uploads the signed
   assets to a draft, checks the remote digests, and publishes the release.
5. Verify the exact version installs from Terraform Registry in an empty
   directory. On first publication, enable the Terraform Cloud GitHub App
   for the repository and register it in the public namespace. Rerun the
   tagged workflow if Registry indexing finishes later.

The release job uses the repository's `GPG_PRIVATE_KEY` Actions secret. The
corresponding public key in `keys/release.asc` must be registered with the
Terraform Registry namespace. The Check workflow has no signing secret.
Use a new tag for changes to an already published release.

[Terraform Registry publishing](https://developer.hashicorp.com/terraform/registry/providers/publishing)
