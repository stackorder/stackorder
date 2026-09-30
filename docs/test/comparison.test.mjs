import assert from 'node:assert/strict'
import { test } from 'node:test'
import { read } from './pages.mjs'

const page = read('docs/guide/comparison.md')
const intro = read('docs/guide/introduction.md')

function column(markdown, pick) {
  return markdown
    .slice(markdown.search(/^\|\s*\|/m))
    .split('\n')
    .slice(2)
    .filter((line) => line.startsWith('|'))
    .map((line) => pick(line.split('|').slice(1, -1)).trim())
}

const rows = (markdown) => column(markdown, (cells) => cells[0])
const stackorder = (markdown) => column(markdown, (cells) => cells.at(-1))

test('the comparison page has one row-by-row section per table row, in order', () => {
  const start = page.indexOf('## Row by row')
  const rowByRow = page.slice(start, page.indexOf('\n## ', start))
  const headings = [...rowByRow.matchAll(/^### (.+)$/gm)].map((m) => m[1].trim())
  assert.deepEqual(headings, rows(page))
})

test('the introduction table has the same rows and Stackorder column as the comparison page', () => {
  assert.deepEqual(rows(intro), rows(page))
  assert.deepEqual(stackorder(intro), stackorder(page))
})
