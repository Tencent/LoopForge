## What changed

Describe the user problem and the resulting behavior.

## Scope

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
bash scripts/validate.sh
bash scripts/smoke-install.sh
bash scripts/e2e.sh
bash scripts/scan-secrets.sh
```

## Safety

- [ ] I did not include credentials, internal endpoints, production data, or organization-specific infrastructure.
- [ ] I preserved user files and unrelated changes.
- [ ] I updated generated Classic host bundles when applicable.
- [ ] I documented third-party content and licensing changes when applicable.
