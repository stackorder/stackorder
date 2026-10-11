import assert from 'node:assert/strict'
import { readdirSync } from 'node:fs'
import { join } from 'node:path'
import { test } from 'node:test'
import { docsRoot, read, repoRoot, section } from './pages.mjs'

function yamlKeys(struct) {
  const body = read('api/v1/config.go').match(new RegExp(`^type ${struct} struct \\{\\n([\\s\\S]*?)^\\}`, 'm'))
  assert.ok(body, `type ${struct} in api/v1/config.go`)
  return [...body[1].matchAll(/yaml:"([a-z_]+)/g)].map((m) => m[1])
}

const firstCells = (markdown) => [...markdown.matchAll(/^\| ([^|]+) \|/gm)].map((m) => m[1])

function names(source, prefix) {
  return new Map([...source.matchAll(new RegExp(`^\\s+(${prefix}\\w*)\\s*=\\s*"([A-Z0-9_]+)"`, 'gm'))].map((m) => [m[1], m[2]]))
}

test('the server configuration lists every variable the server reads', () => {
  const config = read('internal/server/config.go')
  const block = config.match(/\/\/ Environment variables LoadConfig reads\.\nconst \(\n([\s\S]*?)\n\)/)
  assert.ok(block, 'the variable block of internal/server/config.go')
  const cells = firstCells(section(read('docs/reference/server-configuration.md'), 'variables'))
  for (const variable of names(block[1], 'Env').values()) {
    assert.ok(
      cells.some((cell) => cell.includes(`\`${variable}\``)),
      `${variable} has a row in the variables table`,
    )
  }
})

test('the pages that list the variables the CLI sets for the tool and the hooks name each one', () => {
  const hookNames = names(read('internal/tf/hooks.go'), 'HookEnv')
  const stackEnv = read('internal/cli/stack.go').match(/^func \(s \*session\) stackEnv\(\)[\s\S]*?^\}/m)
  assert.ok(stackEnv, 'stackEnv in internal/cli/stack.go')
  const toolVars = [...stackEnv[0].matchAll(/env\[tf\.(HookEnv\w+)\]/g)].map((m) => hookNames.get(m[1]))
  assert.ok(toolVars.length > 0 && toolVars.every(Boolean))

  const cli = section(read('docs/reference/cli.md'), 'environment').split('Terraform and OpenTofu run with')[1]
  const hooks = section(read('docs/configuration/workflows.md'), 'hooks')
  const order = section(read('docs/configuration/instances.md'), 'env')
    .split('\n')
    .find((line) => line.startsWith('- **Order.**'))
  for (const variable of toolVars) {
    assert.match(cli, new RegExp(`^\\| \`${variable}\` \\|`, 'm'), `reference/cli.md: ${variable}`)
    assert.ok(order.includes(`\`${variable}\``), `configuration/instances.md, Order: ${variable}`)
  }
  for (const variable of hookNames.values()) {
    assert.match(hooks, new RegExp(`^\\| \`${variable}\` \\|`, 'm'), `configuration/workflows.md: ${variable}`)
  }
})

test('the configuration pages list every key of the configuration files', () => {
  const rootCells = firstCells(read('docs/configuration/stackorder-yaml.md'))
  for (const key of yamlKeys('RepoConfig')) {
    assert.ok(
      rootCells.some((cell) => cell === `\`${key}\`` || cell.startsWith(`\`${key}.`)),
      `stackorder-yaml.md has a row for ${key}`,
    )
  }
  const stackPage = read('docs/configuration/stack-yaml.md')
  const stackCells = firstCells(stackPage)
  for (const key of yamlKeys('StackConfig')) {
    assert.ok(
      stackCells.some((cell) => cell === `\`${key}\`` || cell.startsWith(`\`${key}.`)),
      `stack-yaml.md has a row for ${key}`,
    )
  }
  const overrides = [
    ['docs/configuration/stack-yaml.md', stackPage, /^An entry of the `instances` map may set .*$/m],
    ['docs/configuration/instances.md', read('docs/configuration/instances.md'), /^An override may set .*$/m],
  ]
  for (const [page, markdown, pattern] of overrides) {
    const sentence = markdown.match(pattern)?.[0]
    assert.ok(sentence, `${page} lists what an instance override may set`)
    for (const key of yamlKeys('InstanceConfig')) {
      assert.ok(sentence.includes(`\`${key}\``) || sentence.includes(`\`${key}.`), `${page}: an override may set ${key}`)
    }
  }
})

test('the data model names the first and the latest migration', () => {
  const migrations = readdirSync(join(repoRoot, 'migrations'))
    .filter((name) => name.endsWith('.up.sql'))
    .map((name) => name.replace(/\.up\.sql$/, ''))
    .sort()
  const m = read('docs/reference/data-model.md').match(/`(\d{4}_\w+)` to `(\d{4}_\w+)`/)
  assert.ok(m, 'data-model.md names the range of migrations')
  assert.deepEqual([m[1], m[2]], [migrations[0], migrations.at(-1)])
})

test('the sidebar links every page of the guide, configuration, reference and operations', () => {
  const links = new Set([...read('docs/.vitepress/config.ts').matchAll(/link: '(\/[^']*)'/g)].map((m) => m[1]))
  for (const dir of ['guide', 'configuration', 'reference', 'operations']) {
    for (const file of readdirSync(join(docsRoot, dir)).filter((name) => name.endsWith('.md'))) {
      const link = `/${dir}/${file.replace(/\.md$/, '')}`
      assert.ok(links.has(link), `the sidebar links ${link}`)
    }
  }
})
