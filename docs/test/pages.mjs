import { readFileSync, readdirSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

export const docsRoot = fileURLToPath(new URL('..', import.meta.url))
export const repoRoot = join(docsRoot, '..')

export function read(path) {
  return readFileSync(join(repoRoot, path), 'utf8')
}

export function docsPages() {
  const pages = []
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (entry.name.startsWith('.') || entry.name === 'node_modules') continue
      const path = join(dir, entry.name)
      if (entry.isDirectory()) walk(path)
      else if (entry.name.endsWith('.md')) pages.push(relative(repoRoot, path))
    }
  }
  walk(docsRoot)
  return pages.sort()
}

export function section(markdown, anchor) {
  const lines = markdown.split('\n')
  const start = lines.findIndex((line) => /^#+ /.test(line) && line.includes(`{#${anchor}}`))
  if (start < 0) throw new Error(`no heading with {#${anchor}}`)
  const level = lines[start].match(/^#+/)[0].length
  const rest = lines.slice(start + 1)
  const end = rest.findIndex((line) => {
    const m = line.match(/^(#+) /)
    return m && m[1].length <= level
  })
  return rest.slice(0, end < 0 ? rest.length : end).join('\n')
}

export function sentences(markdown) {
  return markdown
    .replace(/```[\s\S]*?```/g, '')
    .split(/\n\s*\n/)
    .flatMap((paragraph) => paragraph.replace(/\s+/g, ' ').split(/(?<=[.;])\s+/))
}
