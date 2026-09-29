import { defineConfig, type DefaultTheme } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'
import { inlineCodeVerbatim, repositoryLinks, taskLists } from './markdown'

const repository = 'https://github.com/stackorder/stackorder'
const base = process.env.DOCS_BASE || '/'

const guide: DefaultTheme.SidebarItem[] = [
  {
    text: 'Guide',
    items: [
      { text: 'Introduction', link: '/guide/introduction' },
      { text: 'How it works', link: '/guide/how-it-works' },
      { text: 'Concepts', link: '/guide/concepts' },
      { text: 'Getting started', link: '/guide/getting-started' },
      { text: 'Local demo', link: '/guide/local-demo' },
      { text: 'Comparison', link: '/guide/comparison' },
    ],
  },
]

const configuration: DefaultTheme.SidebarItem[] = [
  {
    text: 'Configuration',
    items: [
      { text: 'stackorder.yaml', link: '/configuration/stackorder-yaml' },
      { text: '.stackorder.yaml', link: '/configuration/stack-yaml' },
      { text: 'Workflows', link: '/configuration/workflows' },
      { text: 'Environments and authorization', link: '/configuration/environments-and-authorization' },
      { text: 'Cross-repo dependencies', link: '/configuration/cross-repo' },
      { text: 'Drift detection', link: '/configuration/drift' },
    ],
  },
]

const reference: DefaultTheme.SidebarItem[] = [
  {
    text: 'Reference',
    items: [
      { text: 'CLI', link: '/reference/cli' },
      { text: 'Exit codes', link: '/reference/exit-codes' },
      { text: 'API', link: '/reference/api' },
      { text: 'Server configuration', link: '/reference/server-configuration' },
      { text: 'GitHub App', link: '/reference/github-app' },
      { text: 'Actions and workflows', link: '/reference/actions' },
      { text: 'Metrics and tracing', link: '/reference/metrics' },
      { text: 'Data model', link: '/reference/data-model' },
      { text: 'Security model', link: '/reference/security-model' },
    ],
  },
]

const operations: DefaultTheme.SidebarItem[] = [
  {
    text: 'Operations',
    items: [
      { text: 'Deploy on AWS', link: '/operations/deploy-aws' },
      { text: 'Deploy as a container', link: '/operations/deploy-container' },
      { text: 'Upgrades and backups', link: '/operations/upgrades-and-backups' },
      { text: 'Security hardening', link: '/operations/security-hardening' },
      { text: 'Troubleshooting', link: '/operations/troubleshooting' },
    ],
  },
]

const design: DefaultTheme.SidebarItem[] = [
  {
    text: 'Design',
    items: [
      { text: 'Design document', link: '/design/' },
      { text: 'Roadmap', link: '/design/roadmap' },
      { text: 'Architecture contract', link: '/design/architecture' },
    ],
  },
  {
    text: 'Project',
    items: [{ text: 'Contributing', link: '/contributing' }],
  },
]

export default withMermaid(
  defineConfig({
    title: 'Stackorder',
    description: 'Lightweight Terraform and OpenTofu orchestration on GitHub Actions.',
    lang: 'en-US',
    base,
    cleanUrls: true,
    lastUpdated: true,
    head: [
      ['link', { rel: 'icon', type: 'image/svg+xml', href: `${base}favicon.svg` }],
      ['meta', { name: 'theme-color', content: '#0f766e' }],
    ],
    markdown: {
      config(md) {
        md.use(repositoryLinks)
        md.use(taskLists)
        md.use(inlineCodeVerbatim)
      },
    },
    themeConfig: {
      logo: { src: '/logo.svg', alt: 'Stackorder' },
      nav: [
        { text: 'Guide', link: '/guide/introduction', activeMatch: '^/guide/' },
        { text: 'Configuration', link: '/configuration/stackorder-yaml', activeMatch: '^/configuration/' },
        { text: 'Reference', link: '/reference/cli', activeMatch: '^/reference/' },
        { text: 'Operations', link: '/operations/deploy-aws', activeMatch: '^/operations/' },
        { text: 'Design', link: '/design/', activeMatch: '^/(design/|contributing)' },
      ],
      sidebar: {
        '/guide/': guide,
        '/configuration/': configuration,
        '/reference/': reference,
        '/operations/': operations,
        '/design/': design,
        '/contributing': design,
      },
      outline: [2, 3],
      search: { provider: 'local' },
      socialLinks: [{ icon: 'github', link: repository }],
      editLink: {
        pattern: `${repository}/edit/main/docs/:path`,
        text: 'Edit this page on GitHub',
      },
      footer: {
        message: `Released under the <a href="${repository}/blob/main/LICENSE">Apache-2.0 License</a>.`,
      },
    },
    mermaid: {
      theme: 'neutral',
      fontFamily: 'Inter, ui-sans-serif, system-ui, sans-serif',
      fontSize: 14,
      flowchart: { nodeSpacing: 28, rankSpacing: 36, padding: 8, diagramPadding: 4 },
      sequence: {
        wrap: true,
        width: 110,
        actorMargin: 16,
        boxMargin: 6,
        messageMargin: 30,
        mirrorActors: false,
        diagramMarginX: 8,
        diagramMarginY: 8,
      },
      state: { nodeSpacing: 28, rankSpacing: 36 },
      er: { minEntityWidth: 70, minEntityHeight: 34, entityPadding: 8, nodeSpacing: 30, rankSpacing: 60 },
    },
    mermaidPlugin: { class: 'mermaid' },
  }),
)
