# Public adaptation scope

This repository contains the self-hosted platform and LoopForge Local. Detailed source synchronization records are maintained privately by the maintainers, not distributed with the public source. Deployment secrets, production data and private infrastructure are excluded.

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
| Language | Translation mechanism/dictionary with public UI copy; Chinese/English preference |
| Theme | Upstream light/dark styles; preference applied before first paint and persisted |
| LoopForge Local | Built-in workflows, host integrations, installers and tests under `local/`; guides under `docs/local/`; project policies and history at the repository root |

Legacy organization-specific tracker and authentication protocol code remains as optional compatibility code with example endpoints. Its Web entry points (external tracker sync fields, TAPD import/push) stay hidden by default. The built-in public hosting API adapter targets github.com. GitHub Enterprise, GitLab, Gitea and other Git hosts can be bound for clone and push through [Git SSH](deployment.md#git-repositories-over-ssh); hosting API actions need their own adapter or MCP. Public defaults never install enterprise MCP servers or contact corporate identity/market services.

The source-only master does not carry its deployed database seed content. Public SOP definitions are therefore explicit public catalog data, not a claim to contain private production projects or prompts. Behavior and execution are shared with master; operator assets, credentials and private integrations are deployment data.

The translation layer excludes code editors, logs, Markdown, contenteditable areas and user text marked `data-i18n-skip`. The English dictionary can be extended independently. Newly added upstream interface copy needs a translation review during synchronization.
