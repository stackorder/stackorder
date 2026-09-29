import type { MarkdownRenderer } from 'vitepress'

type Token = ReturnType<MarkdownRenderer['parse']>[number]

const sitePages: Record<string, string> = {
  'ARCHITECTURE.md': '/design/architecture',
  'CONTRIBUTING.md': '/contributing',
  'CHANGELOG.md': '/changelog',
  'https://claude.ai/artifact/W3gQnvGu5Fw9DSXApYE766': '/design/',
}

const taskMarker = /^\[([ xX])\]\s+/

export function repositoryLinks(md: MarkdownRenderer): void {
  md.core.ruler.push('stackorder_repository_links', (state) => {
    for (const block of state.tokens) {
      for (const token of block.children ?? []) {
        if (token.type !== 'link_open') continue
        const href = token.attrGet('href')
        if (!href) continue
        const [file, hash] = href.replace(/^\.\//, '').split('#')
        const target = sitePages[file] ?? file.match(/^docs\/(.+)\.md$/)?.[1].replace(/(^|\/)index$/, '$1').replace(/^/, '/')
        if (target) token.attrSet('href', hash ? `${target}#${hash}` : target)
      }
    }
  })
}

export function inlineCodeVerbatim(md: MarkdownRenderer): void {
  const render = md.renderer.rules.code_inline!
  md.renderer.rules.code_inline = (tokens, idx, options, env, self) =>
    render(tokens, idx, options, env, self).replace(/^<code/, '<code v-pre')
}

export function taskLists(md: MarkdownRenderer): void {
  md.core.ruler.push('stackorder_task_lists', (state) => {
    const tokens: Token[] = state.tokens
    for (let i = 2; i < tokens.length; i++) {
      const inline = tokens[i]
      if (inline.type !== 'inline' || tokens[i - 1].type !== 'paragraph_open' || tokens[i - 2].type !== 'list_item_open') continue
      const first = inline.children?.[0]
      const match = first?.type === 'text' ? taskMarker.exec(first.content) : null
      if (!first || !match) continue
      first.content = first.content.slice(match[0].length)
      const checkbox = new state.Token('html_inline', '', 0)
      const checked = match[1] !== ' ' ? ' checked' : ''
      checkbox.content = `<input type="checkbox" class="task-list-item-checkbox" disabled${checked}> `
      inline.children!.unshift(checkbox)
      tokens[i - 2].attrJoin('class', 'task-list-item')
      for (let j = i - 3; j >= 0; j--) {
        if (tokens[j].type === 'bullet_list_open' && tokens[j].level === tokens[i - 2].level - 1) {
          if (!tokens[j].attrGet('class')?.includes('contains-task-list')) tokens[j].attrJoin('class', 'contains-task-list')
          break
        }
      }
    }
  })
}
