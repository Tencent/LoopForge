## What changed

Describe the user problem and the resulting behavior.

## Scope

- [ ] Platform core / API / runner
- [ ] Public adapter / deployment
- [ ] Classic
- [ ] Portable
- [ ] Shared behavior contract
- [ ] Installer / CLI
- [ ] Documentation only

## Compatibility

List affected hosts and any migration or backward-compatibility impact.

## Validation

List commands that were actually run and mark anything blocked or not run.

```text
go test -p=2 ./... -count=1 -timeout=180s
go vet ./...
python3 scripts/check-public.py
bash local/scripts/validate.sh
bash local/scripts/smoke-install.sh
bash local/scripts/e2e.sh
bash scripts/scan-secrets.sh
```

## Safety

- [ ] I did not include credentials, internal endpoints, production data, or organization-specific infrastructure.
- [ ] I preserved user files and unrelated changes.
- [ ] I updated generated Classic host bundles when applicable.
- [ ] I documented third-party content and licensing changes when applicable.
