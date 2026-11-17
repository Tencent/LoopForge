import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { apply, inject } from './index.js'

test('the command and status tool use the injected agent working directory', async t => {
  const cwd = mkdtempSync(join(tmpdir(), 'loopforge-dsh-'))
  t.after(() => rmSync(cwd, { recursive: true, force: true }))
  const artifacts = join(cwd, 'artifacts', 'fixture')
  mkdirSync(artifacts, { recursive: true })
  writeFileSync(join(artifacts, 'workflow-state.json'), JSON.stringify({ task_slug: 'fixture', status: 'running' }))
  const agent = { id: 'test-session' }
  let command, tool
  apply({
    commands: { register: definition => { command = definition } },
    tools: { register: definition => { tool = definition } },
    get workingDirectory() {
      assert.ok(inject.includes('workingDirectory'))
      return { ensure: async owner => {
        assert.equal(owner, agent)
        return cwd
      } }
    },
  })
  assert.match((await command.handler({ agent, rawInput: 'status fixture' })).text, /Task: fixture/)
  assert.equal((await tool.execute({ slug: 'fixture' }, { agent })).task_slug, 'fixture')
})
