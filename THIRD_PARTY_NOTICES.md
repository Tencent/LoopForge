# Third-Party Notices

## Superpowers

Portions of the following paths are derived from the Superpowers project:

- `local/.codebuddy/skills/brainstorming/`
- `local/.codebuddy/skills/writing-plans/`
- `local/.codex/skills/superpowers/brainstorming/`
- `local/.cursor/skills/brainstorming/`
- `local/.cursor/skills/writing-plans/`
- `local/.claude/skills/brainstorming/`
- `local/.claude/skills/writing-plans/`

Reviewed upstream version: `v5.1.0`

Upstream project: <https://github.com/obra/superpowers>

The local copies are vendored and may contain compatibility adaptations for this workflow.

MIT License

Copyright (c) 2025 Jesse Vincent

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## Platform editor distributions

- `web/monaco/`: Monaco Editor, MIT, Microsoft. The original `LICENSE` and `ThirdPartyNotices.txt` are included.
- `web/vscode-web/`: Code-OSS web distribution from `vscode-web@1.91.1`, based on Microsoft Visual Studio Code. The Code-OSS MIT license and upstream third-party notices are included in that directory. This is not the proprietary Microsoft-branded Visual Studio Code distribution.
- `go.mod` / `go.sum`: Go dependencies; complete original module licenses and notices are bundled in `third_party/go/THIRD_PARTY_NOTICES.txt`. Refresh with `python3 scripts/collect-go-notices.py --write` after reviewing dependency changes. Binary distributions must retain this bundle.

## Executor CLI packages

The Docker build downloads pinned Codex, Claude Code and CodeBuddy CLI packages from their provider registries. These packages and their bundled dependencies retain the licenses/notices shipped by their providers; they are not relicensed under LoopForge's MIT license. Provider accounts, credentials and service terms apply separately. The installed packages retain their original license/readme files in the image's npm package directories.

The platform source was contributed by the repository owner from the recorded upstream master and is distributed under the root MIT license.
