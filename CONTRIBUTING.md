# Contributing

English | [简体中文](CONTRIBUTING.zh-CN.md)

Thanks for contributing to LoopForge.

## Development workflow

1. Start changes from a feature branch.
2. Keep templates generic; do not introduce organisation-, repository-, or cloud-specific information.
3. Do not commit credentials, local permission configurations, or production data.
4. First indicate whether a change belongs to Classic, Portable, or shared behaviour contracts. Portable only modifies `skills/`; Classic `.cursor/.claude` files are not edited directly — run `build-classic-hosts.py --write`.
5. When modifying shared workflow contracts, describe the impact on both editions separately; do not copy runtime files between them.
6. Run `bash scripts/validate.sh` and `bash scripts/smoke-install.sh` before submitting.

## Pull request requirements

- Describe the problem the change solves and the applicable runtime.
- List behaviour changes, compatibility impact, and verification results.
- When adding third-party content, include source, licence, and modification notes.
- Do not put internal deployment or operational capabilities into core templates; use a separate private extension.

Commit messages should follow Conventional Commits, e.g. `feat: add workflow checkpoint validation`.
