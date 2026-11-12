# NLForge Hermes plugin

This native Hermes Agent plugin adds `/loopforge` controls for a project that
already has the NLForge Hermes Skill installed:

```text
/loopforge start <request>
/loopforge status [slug]
/loopforge result [slug]
/loopforge approve [REQUIREMENT|DESIGN|REVIEW|TEST]
```

The plugin calls the project's `.hermes/skills/devflow/scripts/workflow_state.py`
contract. It stores state under `artifacts/<slug>/` and does not read or modify
`~/.hermes/config.yaml`, profile credentials, or the profile skill directory.

## Local installation

From a LoopForge checkout, validate the plugin first:

```bash
hermes plugins doctor ./local/hermes-plugin
```

For a local Hermes profile, copy the directory to the profile's plugin folder
and restart Hermes. The copy is intentionally explicit so an existing plugin
directory is never overwritten by NLForge:

```bash
plugin_dir="${HERMES_HOME:-$HOME/.hermes}/plugins/loopforge"
if [ -e "$plugin_dir" ]; then
  echo "Refusing to overwrite existing Hermes plugin: $plugin_dir" >&2
  exit 1
fi
mkdir -p "$(dirname "$plugin_dir")"
cp -a ./local/hermes-plugin "$plugin_dir"
```

When this repository is published, install the same directory through Hermes'
normal Git plugin flow (`hermes plugins install <owner>/<repo>`), then enable it
with `hermes plugins enable loopforge`.

To update a published installation, use `hermes plugins update loopforge`. To
disable it without deleting files, use `hermes plugins disable loopforge`; to
remove it, disable it first and then run `hermes plugins remove loopforge`.
For a local copied installation, remove only the exact directory you copied
after disabling the plugin.

Run Hermes from the project root after installing the Skill and trust it:

```bash
loopforge skills install hermes
hermes skills trust .
hermes
```

The plugin is optional. `/devflow` remains the canonical workflow entrypoint.
