# Maintenance and source synchronization

LoopForge is maintained as a public core with deployment-specific adapters. Community changes are reviewed and tested in this repository; contributors do not need access to private source repositories or audit manifests.

## Public contribution checks

- Platform: Go tests and vet, API and runner contracts, fresh installation, browser acceptance, public-content checks and secret scanning.
- Local: generated host consistency, installation and packaging tests, Classic / Portable behavior contracts and secret scanning.
- Compatibility: document schema migrations and breaking API / runner changes. Keep existing installation commands and package names compatible.
- Preserve third-party licenses and notices, contributor attribution and regression tests.

See [Contributing](../CONTRIBUTING.md) for commands and [Deployment](deployment.md) for supported access boundaries.

## Maintainer-only records

Detailed source revisions, file hashes, adaptation reasons and excluded-file inventories are local maintenance records. They are not committed to the public repository, included in Docker build contexts or required by public CI. Maintainers keep a private backup and use local audits when synchronizing changes.

Port shared fixes with their full dependency chain, resolve conflicts using adapter and compatibility tests, and never copy a deployment repository over the public checkout. Do not export credentials, production data or private infrastructure. Audit labels must reflect the code actually synchronized; changing a recorded revision alone is not synchronization.

Platform source changes and Local package releases are separate. Merging a platform PR does not publish a new npm package or create a release tag.
