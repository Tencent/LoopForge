# Contributing

LoopForge has a platform at the repository root and a complementary local workflow edition in `local/`. Read the applicable `AGENTS.md` before changing either.

For platform changes, preserve upstream engine, repository and handler boundaries. Keep identity, provider and deployment adaptations explicit. Add a regression test for a behavioral fix and document schema or runner-protocol compatibility changes.

```bash
go test -p=2 ./... -count=1 -timeout=180s
go vet ./...
python3 scripts/check-public.py
bash scripts/scan-secrets.sh
```

Community contributions do not require private source manifests or hash updates. Maintainers keep source synchronization records locally, separate from the public CI checks. See [the maintenance policy](docs/upstream-sync.md).

Run the [public browser acceptance suite](e2e/public/README.md) for authentication, preferences or execution changes. Paid model and external knowledge tests require explicit operator configuration; state passed, failed, skipped and not-run checks accurately.

For LoopForge Local, work inside `local/` and follow [local agent instructions](local/AGENTS.md). Identify whether a change affects Classic, Portable or shared behavior contracts. Keep these workflow assets independent from the platform and each other. Change Portable assets in `local/skills/`; rebuild generated Classic Cursor/Claude bundles with `python3 scripts/build-classic-hosts.py --write` from `local/`. Keep workflow templates generic; optional tools under `local/tools/` are distributed separately.

Run local checks from `local/`:

```bash
python3 scripts/build-classic-hosts.py --check
bash scripts/validate.sh
bash scripts/smoke-install.sh
bash scripts/smoke-npm.sh
bash scripts/smoke-curl-install.sh
bash scripts/e2e.sh
bash scripts/test-observability.sh
bash scripts/scan-secrets.sh
```

The [local E2E suite](local/tests/e2e/README.md) uses a fake host and recorded outputs, without model credentials. Describe the impact on each affected edition. Preserve existing permission behavior and user-file protection. Commit messages should follow Conventional Commits.

Public pull requests must not contain credentials, private infrastructure, employee access defaults or production datasets. Preserve existing user files. Keep source attribution and third-party licenses. Describe the problem, resulting behavior and the validation actually performed in the PR.
