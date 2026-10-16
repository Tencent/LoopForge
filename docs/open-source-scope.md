# Public adaptation scope

Source: the `master` commit recorded in `upstream.json`. This is a source export, not a merge of PR #47. Private Git history, CI, deployment environments, certificates, employee defaults, marketplace snapshots and customer efficiency snapshots are excluded.

| Area | Public behavior |
| --- | --- |
| Engine, persistence, runner protocols, gates, artifacts, coordination | Upstream implementation and tests |
| Web workspace and navigation | Upstream master layout |
| Identity | Local password accounts; explicit account provisioning |
| Administrators | No embedded employee allowlist; explicit grants/configuration |
| Repository hosting | GitHub reference normalization, Issue import, PR discovery/merge/state refresh |
| MCP | Explicit installation; public GitHub entry; actor-scoped token binding |
| Catalog | Public backend/frontend/product/bug-fix definitions; real script approval demo |
| Model defaults | CLI default and operator-configured model list; CodeBuddy/Codex/Claude selection in Main Bot and SOP roles, actor-scoped credentials |
| Git author identity | Local account name and a non-deliverable LoopForge email; no inferred corporate affiliation |
| Language | PR #47 translation mechanism/dictionary with new public UI copy; Chinese/English preference |
| Theme | Upstream light/dark styles; preference applied before first paint and persisted |
| LoopForge Local | Built-in workflows, host integrations, installers and tests under `local/`; guides under `docs/local/`; project policies and history at the repository root |

Legacy organization-specific tracker and authentication protocol code remains as optional compatibility code with example endpoints; it is not automatically configured. Public defaults never install enterprise MCP servers or contact corporate identity/market services. GitHub Enterprise and non-GitHub public hosting require additional provider configuration/adapters; the initial public adapter targets github.com.

The source-only master does not carry its deployed database seed content. Public SOP definitions are therefore explicit public catalog data, not a claim to contain private production projects or prompts. Behavior and execution are shared with master; operator assets, credentials and private integrations are deployment data.

The translation layer excludes code editors, logs, Markdown, contenteditable areas and user text marked `data-i18n-skip`. The English dictionary can be extended independently. Newly added upstream interface copy needs a translation review during synchronization.
