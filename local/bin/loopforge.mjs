#!/usr/bin/env node

import { spawnSync } from "node:child_process"
import { fileURLToPath } from "node:url"
import path from "node:path"

const packageRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..")
const minimumPython = [3, 8]
const candidates = process.platform === "win32"
  ? [
      { command: "py", prefix: ["-3"] },
      { command: "python", prefix: [] },
      { command: "python3", prefix: [] },
    ]
  : [
      { command: "python3", prefix: [] },
      { command: "python", prefix: [] },
    ]

function detectPython() {
  for (const candidate of candidates) {
    const result = spawnSync(
      candidate.command,
      [...candidate.prefix, "--version"],
      { encoding: "utf8", windowsHide: true },
    )
    if (result.status !== 0) continue
    const versionText = `${result.stdout ?? ""}\n${result.stderr ?? ""}`
    const match = versionText.match(/Python\s+(\d+)\.(\d+)/)
    if (!match) continue
    const version = [Number(match[1]), Number(match[2])]
    if (
      version[0] > minimumPython[0]
      || (version[0] === minimumPython[0] && version[1] >= minimumPython[1])
    ) {
      return candidate
    }
  }
  return null
}

const python = detectPython()
if (!python) {
  console.error(
    "ERROR: loopforge requires Python 3.8 or newer. "
    + "Install Python and ensure python3, python, or py is available on PATH.",
  )
  process.exit(1)
}

const sourceRoot = path.join(packageRoot, "src")
const env = {
  ...process.env,
  DEVFLOW_ASSET_ROOT: packageRoot,
  PYTHONPATH: process.env.PYTHONPATH
    ? `${sourceRoot}${path.delimiter}${process.env.PYTHONPATH}`
    : sourceRoot,
  PYTHONUTF8: process.env.PYTHONUTF8 ?? "1",
}
const result = spawnSync(
  python.command,
  [...python.prefix, "-m", "devflow_cli.cli", ...process.argv.slice(2)],
  { cwd: process.cwd(), env, stdio: "inherit", windowsHide: true },
)

if (result.error) {
  console.error(`ERROR: failed to start loopforge: ${result.error.message}`)
  process.exit(1)
}
if (result.signal) {
  process.kill(process.pid, result.signal)
}
process.exit(result.status ?? 1)
