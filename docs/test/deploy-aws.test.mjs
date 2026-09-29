import assert from 'node:assert/strict'
import { test } from 'node:test'
import { read, section, sentences } from './pages.mjs'

const page = read('docs/operations/deploy-aws.md')

function blocks(hcl, kind) {
  const found = new Map()
  for (const m of hcl.matchAll(new RegExp(`^${kind} "([a-z0-9_]+)" \\{\\n([\\s\\S]*?)^\\}`, 'gm'))) {
    const body = m[2]
    const attr = (name) => body.match(new RegExp(`^  ${name}\\s*=\\s*(.*)$`, 'm'))?.[1].trim()
    const description = attr('description')
    found.set(m[1], {
      description: description && JSON.parse(description),
      type: attr('type')?.replace(/\(\{$/, ''),
      default: attr('default'),
      sensitive: attr('sensitive') === 'true',
    })
  }
  return found
}

function rows(markdown) {
  const found = new Map()
  for (const line of markdown.split('\n')) {
    const m = line.match(/^\| `([a-z0-9_]+)` \| (.*) \|$/)
    if (m) found.set(m[1], m[2].split(' | '))
  }
  return found
}

const plain = (text) => text.replaceAll('`', '')

test('the inputs tables list every variable with its type, default and description', () => {
  const variables = blocks(read('deploy/terraform/variables.tf'), 'variable')
  const documented = rows(section(page, 'inputs').split('### What the module passes')[0])
  assert.deepEqual([...documented.keys()].sort(), [...variables.keys()].sort())
  for (const [name, v] of variables) {
    const [type, def, description] = documented.get(name)
    assert.equal(type, `\`${v.type}\`${v.sensitive ? ' (sensitive)' : ''}`, `type of ${name}`)
    assert.equal(def, v.default === undefined ? 'required' : `\`${v.default}\``, `default of ${name}`)
    assert.equal(plain(description), v.description, `description of ${name}`)
  }
})

test('the outputs table lists every output with its description', () => {
  const outputs = blocks(read('deploy/terraform/outputs.tf'), 'output')
  const documented = rows(section(page, 'outputs'))
  assert.deepEqual([...documented.keys()].sort(), [...outputs.keys()].sort())
  for (const [name, o] of outputs) {
    assert.equal(plain(documented.get(name)[0]), o.description, `description of ${name}`)
  }
})

test('the page does not claim the tables are generated', () => {
  assert.doesNotMatch(section(page, 'inputs'), /\bgenerated from\b/)
})

test('the health check section says a database outage makes ECS replace tasks', () => {
  const healthCheck = section(page, 'health-check')
  for (const sentence of sentences(healthCheck)) {
    assert.doesNotMatch(sentence, /database outage does not/, sentence)
  }
  assert.match(healthCheck, /database outage still makes ECS replace tasks/)
})
