import { spawn } from 'node:child_process'
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'

export const name = 'loopforge-dsh'
export const inject = ['commands', 'tools']

const SKILL_ROOT = '.dsh/skills/devflow'
const WORKFLOW = 'scripts/workflow_state.py'
const SAFE_SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/u
const APPROVAL_STAGES = new Set(['REQUIREMENT', 'DESIGN', 'REVIEW', 'TEST'])
const MAX_RESULT_FILES = 40

function cwdOf(owner) {
  const agent = owner?.agent
  const session = agent?.session
  const service = agent?.ctx?.workingDirectory
    ?? agent?.ctx?.get?.('workingDirectory')
    ?? owner?.ctx?.workingDirectory
    ?? owner?.ctx?.get?.('workingDirectory')
  const candidate = typeof service?.get === 'function' && session
    ? service.get(session)
    : session?.header?.cwd
    ?? process.env.DSH_PROJECT_ROOT
    ?? process.cwd()
  return resolve(candidate)
}

function run(argv, cwd, signal) {
  return new Promise((resolvePromise, reject) => {
    if (signal?.aborted) {
      reject(signal.reason instanceof Error ? signal.reason : new Error('LoopForge command aborted'))
      return
    }
    let settled = false
    const child = spawn(argv[0], argv.slice(1), { cwd, stdio: ['ignore', 'pipe', 'pipe'] })
    let stdout = ''
    let stderr = ''
    child.stdout.on('data', chunk => { stdout += chunk })
    child.stderr.on('data', chunk => { stderr += chunk })
    const abort = () => {
      if (!child.killed) child.kill('SIGTERM')
    }
    signal?.addEventListener('abort', abort, { once: true })
    child.on('error', error => {
      if (settled) return
      settled = true
      signal?.removeEventListener('abort', abort)
      reject(error)
    })
    child.on('close', code => {
      if (settled) return
      settled = true
      signal?.removeEventListener('abort', abort)
      if (code === 0) resolvePromise(stdout.trim())
      else if (signal?.aborted) {
        reject(signal.reason instanceof Error ? signal.reason : new Error('LoopForge command aborted'))
      } else reject(new Error(stderr.trim() || `LoopForge command exited with ${code}`))
    })
  })
}

function scriptPath(cwd) {
  const path = join(cwd, SKILL_ROOT, WORKFLOW)
  if (!existsSync(path)) {
    throw new Error('LoopForge Skill is not installed in this project; run `loopforge skills install dsh` first.')
  }
  return path
}

function stateFiles(cwd) {
  const root = join(cwd, 'artifacts')
  if (!existsSync(root)) return []
  const files = []
  for (const entry of readdirSync(root)) {
    const path = join(root, entry, 'workflow-state.json')
    if (existsSync(path) && statSync(path).isFile()) files.push(path)
  }
  return files.sort((a, b) => statSync(b).mtimeMs - statSync(a).mtimeMs)
}

function readState(cwd, slug) {
  if (slug !== undefined && !SAFE_SLUG.test(slug)) {
    throw new Error('Task slug must contain only letters, digits, hyphens, and underscores.')
  }
  const paths = stateFiles(cwd)
  const path = slug ? join(cwd, 'artifacts', slug, 'workflow-state.json') : paths[0]
  if (!path || !existsSync(path)) return undefined
  return { path, state: JSON.parse(readFileSync(path, 'utf8')) }
}

function renderState(record) {
  if (!record) return 'No LoopForge task found in this project.'
  const { state, path } = record
  const completed = Object.entries(state.stages ?? {})
    .filter(([, stage]) => stage?.status === 'completed')
    .map(([stage]) => stage)
  return [
    `Task: ${state.task_slug ?? 'unknown'}`,
    `Status: ${state.status ?? 'unknown'}`,
    `Size: ${state.size_class ?? 'unknown'}`,
    `Artifacts: ${path}`,
    completed.length ? `Completed stages: ${completed.join(', ')}` : '',
    state.awaiting_approval ? `Awaiting approval: ${state.awaiting_approval}` : '',
    state.next_stage ? `Next stage: ${state.next_stage}` : '',
  ].filter(Boolean).join('\n')
}

function resultFiles(cwd, slug) {
  const root = join(cwd, 'artifacts', slug)
  if (!existsSync(root)) return []
  const files = []
  const visit = directory => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name)
      if (entry.isDirectory()) visit(path)
      else if (entry.isFile()) files.push(path.slice(root.length + 1))
    }
  }
  visit(root)
  return files.sort().slice(0, MAX_RESULT_FILES)
}

function parseInput(rawInput) {
  const input = rawInput.trim()
  const match = /^(start|status|approve|result|help)(?:\s+([\s\S]*))?$/u.exec(input)
  return match ? { command: match[1], value: match[2]?.trim() ?? '' } : { command: 'help', value: '' }
}

async function handle(invocation, ctx) {
  const cwd = cwdOf(invocation)
  const { command, value } = parseInput(invocation.rawInput)
  if (command === 'help') {
    return { kind: 'success', text: 'Usage: /loopforge start <request> | status [slug] | result [slug] | approve [stage] | help' }
  }
  if (command === 'status') return { kind: 'success', text: renderState(readState(cwd, value || undefined)) }
  if (command === 'result') {
    const record = readState(cwd, value || undefined)
    if (!record) return { kind: 'success', text: 'No LoopForge task found in this project.' }
    const files = resultFiles(cwd, record.state.task_slug)
    const listing = files.length
      ? `\n\nArtifact files:\n${files.map(file => `- ${file}`).join('\n')}`
      : '\n\nNo artifact files have been written yet.'
    return { kind: 'success', text: `${renderState(record)}${listing}` }
  }
  const script = scriptPath(cwd)
  if (command === 'approve') {
    const record = readState(cwd)
    if (!record) return { kind: 'error', text: 'No LoopForge task is waiting for approval.' }
    const stage = value || record.state.awaiting_approval
    if (!stage) return { kind: 'error', text: 'No approval stage is pending.' }
    if (!APPROVAL_STAGES.has(stage)) {
      return { kind: 'error', text: `Unknown approval stage: ${stage}. Use REQUIREMENT, DESIGN, REVIEW, or TEST.` }
    }
    const args = ['python3', script, 'approve', '--state', record.path, '--stage', stage]
    if (stage === 'REQUIREMENT') args.push('--user-confirmed')
    await run(args, cwd, invocation.signal)
    return { kind: 'success', text: `Approved ${stage}. Resume the DSH workflow to continue.` }
  }
  if (!value) return { kind: 'error', text: 'A request is required: /loopforge start <request>' }
  const slug = value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 48) || `dsh-${Date.now()}`
  const statePath = join(cwd, 'artifacts', slug, 'workflow-state.json')
  const hasSubagent = typeof ctx?.tools?.get === 'function'
    && ctx.tools.get('subagent', invocation.agent) !== undefined
  const args = [
    'python3', script, 'init', '--project-root', cwd, '--slug', slug,
    '--size', 'medium', '--mode', 'auto', '--coordinator-id', 'dsh-main',
  ]
  if (hasSubagent) {
    args.push('--execution-mode', 'isolated', '--host-adapter', 'dsh')
  } else {
    args.push(
      '--execution-mode', 'single-context',
      '--fallback-reason', 'DSH subagent tool is unavailable in this session',
    )
  }
  const output = await run(args, cwd, invocation.signal)
  const execution = hasSubagent
    ? 'isolated stages via the DSH subagent tool'
    : 'single-context fallback because the DSH subagent tool is unavailable'
  return { kind: 'success', text: `LoopForge task initialized: ${slug}\nRequest: ${value}\nState: ${statePath}\n${output}\nExecution: ${execution}. Follow the devflow Skill to resume the workflow.` }
}

export function apply(ctx) {
  ctx.commands.register({
    name: 'loopforge',
    description: 'Start, inspect, and approve LoopForge Local workflows',
    input: { hint: '[start <request>|status [slug]|result [slug]|approve [stage]|help]' },
    handler: invocation => handle(invocation, ctx),
  })
  ctx.tools.register({
    name: 'loopforge_status',
    description: 'Read the current LoopForge Local workflow state and pending human approval for the current DSH project. Read-only.',
    parameters: {
      type: 'object',
      properties: {
        slug: { type: 'string', description: 'Optional task slug; omit to inspect the most recently updated task.' },
      },
      additionalProperties: false,
    },
    output: {
      schema: {
        type: 'object',
        additionalProperties: false,
        properties: {
          found: { type: 'boolean' },
          status: { type: 'string' },
          task_slug: { type: 'string' },
          awaiting_approval: { type: 'string' },
          state_path: { type: 'string' },
        },
        required: ['found'],
      },
      render: (_args, value) => [{ type: 'text', text: JSON.stringify(value) }],
    },
    execute: async (args, exec) => {
      const record = readState(cwdOf(exec), args?.slug)
      if (!record) return { found: false }
      return {
        found: true,
        status: record.state.status,
        task_slug: record.state.task_slug,
        awaiting_approval: record.state.awaiting_approval ?? '',
        state_path: record.path,
      }
    },
  })
}
