# Repository instructions

- Write public documentation, examples, diagnostics, and commit messages in English.
- Follow the [public information rules](CONTRIBUTING.md#public-information) for public content.
- Use Go, the Terraform Plugin Framework, and the Cloudflare Go SDK. Run `mise run check` before publication.
- Keep test identities synthetic. Ordinary tests use fixtures and never change a Cloudflare account.
- Keep credentials, state, saved plans, and machine-specific paths out of commits and logs.
- Preserve remote IDs and issued values when moving supported state. Verify an upgrade with representative configuration and state before changing the public contract.
- Add code comments only on request. Preserve license and attribution notices.
- Explain the reason for each commit and include an AI co-author trailer when applicable.
