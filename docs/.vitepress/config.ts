import { defineConfig, type DefaultTheme } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'
import { inlineCodeVerbatim, repositoryLinks, taskLists } from './markdown'

const repository = 'https://github.com/stackorder/stackorder'
const site = 'https://docs.stackorder.io'
const website = 'https://stackorder.io'
const globe =
  '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><g fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="M2 12h20M12 2a15 15 0 0 1 0 20M12 2a15 15 0 0 0 0 20"/></g></svg>'
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
      { text: 'Architecture differences', link: '/guide/comparison' },
    ],
  },
]

const configuration: DefaultTheme.SidebarItem[] = [
  {
    text: 'Configuration',
    items: [
      { text: 'stackorder.yaml', link: '/configuration/stackorder-yaml' },
      { text: '.stackorder.yaml', link: '/configuration/stack-yaml' },
      { text: 'Stack instances', link: '/configuration/instances' },
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
    items: [
      { text: 'Contributing', link: '/contributing' },
      { text: 'Changelog', link: '/changelog' },
    ],
  },
]

export default withMermaid(
  defineConfig({
    title: 'Stackorder',
    titleTemplate: ':title | Stackorder docs',
    description:
      'Open-source Terraform and OpenTofu orchestration on GitHub Actions: pull request plans, applies in dependency order, drift detection. Self-hosted.',
    lang: 'en-US',
    base,
    cleanUrls: true,
    lastUpdated: true,
    head: [
      ['link', { rel: 'icon', href: `${base}favicon.ico`, sizes: '32x32' }],
      ['link', { rel: 'icon', href: `${base}favicon.svg`, type: 'image/svg+xml' }],
      ['link', { rel: 'apple-touch-icon', href: `${base}apple-touch-icon.png` }],
      ['link', { rel: 'manifest', href: `${base}site.webmanifest` }],
      ['meta', { name: 'theme-color', content: '#2F3E46' }],
      ['meta', { property: 'og:type', content: 'website' }],
      ['meta', { property: 'og:site_name', content: 'Stackorder' }],
      ['meta', { property: 'og:image', content: `${site}/og-image.png` }],
      ['meta', { property: 'og:image:width', content: '1200' }],
      ['meta', { property: 'og:image:height', content: '630' }],
      ['meta', { property: 'og:image:alt', content: 'stackorder: Terraform and OpenTofu orchestration on GitHub Actions' }],
      ['meta', { name: 'twitter:card', content: 'summary_large_image' }],
    ],
    sitemap: { hostname: site },
    transformHead({ pageData, title, description }) {
      if (pageData.isNotFound) return
      const url = `${site}/${pageData.relativePath.replace(/(^|\/)index\.md$/, '$1').replace(/\.md$/, '')}`
      return [
        ['link', { rel: 'canonical', href: url }],
        ['meta', { property: 'og:url', content: url }],
        ['meta', { property: 'og:title', content: title }],
        ['meta', { property: 'og:description', content: description }],
      ]
    },
    markdown: {
      config(md) {
        md.use(repositoryLinks)
        md.use(taskLists)
        md.use(inlineCodeVerbatim)
      },
    },
    themeConfig: {
      logo: { light: '/lockup-light.svg', dark: '/lockup-dark.svg', alt: 'stackorder' },
      siteTitle: false,
      nav: [
        { text: 'Guide', link: '/guide/introduction', activeMatch: '^/guide/' },
        { text: 'Configuration', link: '/configuration/stackorder-yaml', activeMatch: '^/configuration/' },
        { text: 'Reference', link: '/reference/cli', activeMatch: '^/reference/' },
        { text: 'Operations', link: '/operations/deploy-aws', activeMatch: '^/operations/' },
        { text: 'Design', link: '/design/', activeMatch: '^/(design/|contributing)' },
        { text: 'Changelog', link: '/changelog', activeMatch: '^/changelog' },
      ],
      sidebar: {
        '/guide/': guide,
        '/configuration/': configuration,
        '/reference/': reference,
        '/operations/': operations,
        '/design/': design,
        '/contributing': design,
        '/changelog': design,
      },
      outline: [2, 3],
      search: { provider: 'local' },
      socialLinks: [
        { icon: { svg: globe }, link: website, ariaLabel: 'stackorder.io website' },
        { icon: 'github', link: repository },
      ],
      editLink: {
        pattern: `${repository}/edit/main/docs/:path`,
        text: 'Edit this page on GitHub',
      },
      footer: {
        message: `Released under the <a href="${repository}/blob/main/LICENSE">Apache-2.0 License</a>. Project website: <a href="${website}">stackorder.io</a>.`,
      },
    },
    vite: {
      optimizeDeps: {
        include: ['mermaid > fastdom', 'mermaid > fastdom/extensions/fastdom-promised.js'],
      },
    },
    mermaidPlugin: { class: 'mermaid' },
  }),
)
