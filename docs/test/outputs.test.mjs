import assert from 'node:assert/strict'
import { test } from 'node:test'
import { read, section, sentences } from './pages.mjs'

const httpStatus = {
  StatusBadRequest: 400,
  StatusUnauthorized: 401,
  StatusForbidden: 403,
  StatusNotFound: 404,
  StatusConflict: 409,
  StatusUnprocessableEntity: 422,
  StatusLocked: 423,
  StatusInternalServerError: 500,
  StatusServiceUnavailable: 503,
}

const constants = (source, prefix) =>
  new Map([...source.matchAll(new RegExp(`^\\s+(${prefix}\\w*)\\s*=\\s*"([a-z_]+)"`, 'gm'))].map((m) => [m[1], m[2]]))

const status = (name) => {
  assert.ok(name in httpStatus, `no number for http.${name}`)
  return httpStatus[name]
}

const funcBody = (source, signature) => {
  const start = source.indexOf(signature)
  assert.ok(start >= 0, signature)
  const end = source.indexOf('\nfunc ', start + 1)
  return source.slice(start, end < 0 ? source.length : end)
}

function sentinelAnswers(api) {
  const codes = constants(api, 'code')
  const helpers = new Map(
    [...api.matchAll(/^func (\w+)\(message string\) \*apiError \{\s*return newError\(http\.(Status\w+), (code\w+),/gm)].map(
      (m) => [m[1], [status(m[2]), codes.get(m[3])]],
    ),
  )
  const answers = new Map()
  for (const m of funcBody(api, 'func toAPIError(').matchAll(
    /case errors\.Is\(err, principal\.(Err\w+)\):\s*return (?:newError\(http\.(Status\w+), (code\w+)|(\w+)\()/g,
  )) {
    if (answers.has(m[1])) continue
    const answer = m[4] ? helpers.get(m[4]) : [status(m[2]), codes.get(m[3])]
    assert.ok(answer?.[1], `principal.${m[1]} maps to no code`)
    answers.set(m[1], answer)
  }
  return answers
}

function outputsErrors() {
  const types = read('api/v1/types.go')
  const service = read('internal/runs/outputs.go')
  const api = read('internal/api/errors.go')
  const errCodes = constants(types, 'ErrCode')
  const codes = new Map([...errCodes, ...constants(service, 'code')])
  const kinds = new Map(
    [...api.matchAll(/errors\.Is\(e\.Kind, principal\.(Err\w+)\):\s*\n\s*status = http\.(Status\w+)/g)].map((m) => [
      m[1],
      status(m[2]),
    ]),
  )
  const uses = [
    ...service.matchAll(/Kind: principal\.(Err\w+), Code: (?:v1\.)?(\w+)/g),
    ...service.matchAll(/\{stateread\.\w+, principal\.(Err\w+), v1\.(ErrCode\w+),/g),
  ]
  const answers = new Map()
  for (const [, kind, name] of uses) {
    const code = codes.get(name)
    assert.ok(code, `${name} is not a code constant`)
    assert.ok(kinds.has(kind), `principal.${kind} has no status in codedError`)
    assert.equal(answers.get(code) ?? kinds.get(kind), kinds.get(kind), `${code} answers one status`)
    answers.set(code, kinds.get(kind))
  }
  for (const code of errCodes.values()) {
    assert.ok(answers.has(code), `v1 code ${code} is never answered by StackOutputs`)
  }
  const sentinels = sentinelAnswers(api)
  const generic = new Map()
  for (const [, sentinel] of service.matchAll(/principal\.Wrap\(principal\.(Err\w+),/g)) {
    assert.ok(sentinels.has(sentinel), `principal.${sentinel} has no answer in toAPIError`)
    const [want, code] = sentinels.get(sentinel)
    generic.set(code, want)
  }
  return { errCodes: [...errCodes.values()], answers, generic, codes, sentinels, service }
}

test('the API reference lists every code a read of stack outputs answers, with its status', () => {
  const { answers, generic, sentinels } = outputsErrors()
  const errors = section(read('docs/reference/api.md'), 'stack-outputs-errors')
  const intro = errors.trim().split('\n')[0]
  const rows = new Map([...errors.matchAll(/^\| (\d{3}) \| `([a-z_]+)` \|/gm)].map((m) => [m[2], Number(m[1])]))
  for (const [code, want] of answers) {
    assert.equal(rows.get(code), want, `status of ${code}`)
  }
  for (const [code, want] of generic) {
    assert.ok(rows.get(code) === want || intro.includes(`\`${want} ${code}\``), `${want} ${code} is listed`)
  }
  for (const code of rows.keys()) {
    assert.ok(answers.has(code) || generic.has(code), `${code} is never answered by StackOutputs`)
  }
  const [unprocessable, code] = sentinels.get('ErrUnprocessable')
  if (!generic.has(code)) {
    assert.match(errors, new RegExp(`never answers the generic \`${unprocessable} ${code}\``))
  }
})

test('the API reference says which refusals of a read of stack outputs are audited', () => {
  const { answers, generic, codes, sentinels, service } = outputsErrors()
  const stackOutputs = funcBody(service, 'func (s *Service) StackOutputs(')
  const audit = stackOutputs.indexOf('s.auditOutputsRefused(')
  assert.ok(audit >= 0, 'StackOutputs audits refusals')
  const beforeAudit = stackOutputs.slice(0, audit)
  const unaudited = new Set([
    ...[...beforeAudit.matchAll(/Code: (?:v1\.)?(\w+)/g)].map((m) => codes.get(m[1])),
    ...[...service.matchAll(/\{stateread\.\w+, principal\.Err\w+, v1\.(ErrCode\w+),/g)].map((m) => codes.get(m[1])),
    ...[...funcBody(service, 'func (s *Service) stateError(').matchAll(/principal\.Wrap\(principal\.(Err\w+),/g)].map(
      (m) => sentinels.get(m[1])[1],
    ),
  ])
  const reasons = funcBody(service, 'func refusalReason(')
  const audited = new Set([
    ...[...reasons.matchAll(/\{principal\.Err\w+, (?:"([a-z_]+)"|(code\w+))\}/g)].map((m) => m[1] ?? codes.get(m[2])),
    ...[...answers.keys()].filter((code) => !unaudited.has(code)),
  ])
  for (const code of [...answers.keys(), ...generic.keys()]) {
    assert.ok(audited.has(code) !== unaudited.has(code), `${code} is either audited or not`)
  }
  const named = (pattern) => {
    const found = sentences(section(read('docs/reference/api.md'), 'stack-outputs-errors')).filter((s) => pattern.test(s))
    assert.equal(found.length, 1, String(pattern))
    return [...found[0].matchAll(/`([a-z_]+)`/g)].map((m) => m[1]).filter((code) => code !== 'outputs_refused')
  }
  assert.deepEqual(named(/are audited as `outputs_refused`/).sort(), [...audited].sort())
  assert.deepEqual(named(/not audited/).sort(), [...unaudited].sort())
})

test('troubleshooting has a row for every v1 code of a read of stack outputs', () => {
  const { errCodes, answers } = outputsErrors()
  const rows = section(read('docs/operations/troubleshooting.md'), 'shared-outputs')
  for (const code of errCodes) {
    assert.match(rows, new RegExp(`^\\| \`${code}\` \\(${answers.get(code)}\\) \\|`, 'm'), code)
  }
})

test('the shared outputs page states the limits of a state read', () => {
  const source = read('internal/stateread/stateread.go')
  const value = (name, pattern) => {
    const m = source.match(new RegExp(`^\\s+${name}\\s*=\\s*${pattern}$`, 'm'))
    assert.ok(m, `${name} in stateread.go`)
    return m[1]
  }
  const limits = section(read('docs/configuration/outputs.md'), 'server')
  for (const cell of [
    `${value('MaxObjectBytes', '(\\d+) << 20')} MiB`,
    `${value('MaxOutputsBytes', '(\\d+) << 20')} MiB`,
    `${value('Timeout', '(\\d+) \\* time\\.Second')} s`,
    `${value('MaxWait', '(\\d+) \\* time\\.Second')} s`,
    value('MaxConcurrent', '(\\d+)'),
  ]) {
    assert.match(limits, new RegExp(`^\\| [^|]+ \\| ${cell} \\|`, 'm'), cell)
  }
})

test('the shared outputs page names the server variables that turn the feature on', () => {
  const server = section(read('docs/configuration/outputs.md'), 'server')
  for (const name of ['EnvStateBuckets', 'EnvStateRoleARN']) {
    const variable = read('internal/server/config.go').match(new RegExp(`^\\s+${name}\\s*=\\s*"([A-Z_]+)"`, 'm'))[1]
    assert.match(server, new RegExp(`^\\| \`${variable}\` \\|`, 'm'), variable)
  }
})
