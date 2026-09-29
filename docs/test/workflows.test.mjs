import assert from 'node:assert/strict'
import { test } from 'node:test'
import { docsPages, read } from './pages.mjs'

const callerPermissions = {
  plan: ['id-token: write', 'contents: read', 'actions: read', 'checks: write', 'pull-requests: read'],
  run: ['id-token: write', 'contents: read', 'actions: read', 'checks: write'],
}

function callers(markdown) {
  return [...markdown.matchAll(/```ya?ml\n([\s\S]*?)```/g)]
    .map((m) => m[1])
    .flatMap((block) => {
      const m = block.match(/uses: stackorder\/actions\/\.github\/workflows\/(plan|run)\.yml@/)
      return m ? [{ workflow: m[1], block }] : []
    })
}

const pages = ['README.md', ...docsPages().filter((page) => !page.startsWith('docs/design/'))]

test('the README calls the reusable plan workflow', () => {
  assert.ok(callers(read('README.md')).some(({ workflow }) => workflow === 'plan'))
})

for (const page of pages) {
  for (const { workflow, block } of callers(read(page))) {
    test(`${page}: the ${workflow}.yml caller passes server-url and grants the permissions`, () => {
      assert.match(block, /^\s+server-url: /m)
      assert.match(block, /^\s+permissions:$/m)
      for (const permission of callerPermissions[workflow]) {
        assert.match(block, new RegExp(`^\\s+${permission}$`, 'm'), permission)
      }
    })
  }
}

test('the README does not describe the project as unbuilt', () => {
  const readme = read('README.md')
  for (const stale of [/Nothing here is usable/i, /pre-alpha/i, /\(planned\)/i, /^Planned layout/m]) {
    assert.doesNotMatch(readme, stale)
  }
})
