import assert from 'node:assert/strict'
import { test } from 'node:test'
import { docsPages, read, section, sentences } from './pages.mjs'

function reservedExtraEnvironmentKeys() {
  const variables = read('deploy/terraform/variables.tf')
  const block = variables.match(/variable "extra_environment" \{[\s\S]*?\n\}/)[0]
  const list = block.match(/!contains\(\[([\s\S]*?)\], k\)/)[1]
  return [...list.matchAll(/"([A-Z0-9_]+)"/g)].map((m) => m[1])
}

test('the module reserves STACKORDER_METRICS_TOKEN and has a metrics_token input', () => {
  const variables = read('deploy/terraform/variables.tf')
  assert.ok(reservedExtraEnvironmentKeys().includes('STACKORDER_METRICS_TOKEN'))
  assert.match(variables, /variable "metrics_token" \{/)
})

test('no page tells readers to pass a module-managed variable through extra_environment', () => {
  const reserved = reservedExtraEnvironmentKeys()
  for (const page of docsPages()) {
    for (const sentence of sentences(read(page))) {
      if (!sentence.includes('extra_environment') || /reject/.test(sentence)) continue
      for (const key of reserved) {
        assert.ok(!sentence.includes(key), `${page}: "${sentence}" puts ${key} in extra_environment`)
      }
    }
  }
})

test('no page says the module lacks an input for the metrics token', () => {
  for (const page of docsPages()) {
    for (const sentence of sentences(read(page))) {
      if (!/module/i.test(sentence) || !/token|METRICS_TOKEN/i.test(sentence)) continue
      assert.doesNotMatch(sentence, /\bno (dedicated )?(input|place)\b/i, `${page}: "${sentence}"`)
    }
  }
})

test('security hardening points module users at metrics_token and its secret', () => {
  const metrics = section(read('docs/operations/security-hardening.md'), 'metrics')
  assert.doesNotMatch(metrics, /extra_environment/)
  assert.match(metrics, /`metrics_token`/)
  assert.match(metrics, /`metrics_token_secret_arn`/)
})
