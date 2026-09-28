---
title: Contributing
editLink: false
outline: [2, 3]
---

<!--@include: ../CONTRIBUTING.md-->

## Working on this site {#docs-site}

The documentation lives in `docs/` and is built with VitePress. The Markdown pages are the source; `docs/.vitepress/config.ts` holds the navigation and sidebars.

```sh
cd docs
npm ci
npm run docs:dev      # local server with hot reload
npm run docs:build    # static build into docs/.vitepress/dist
npm run docs:preview  # serve the built site
```

- The build fails on dead internal links. Link to pages by path, such as `/reference/cli#exit-codes`.
- Diagrams are fenced `mermaid` code blocks, rendered in the browser.
- The [architecture contract](/design/architecture) and this page include `ARCHITECTURE.md` and `CONTRIBUTING.md` from the repository root. Edit those files, not the pages that include them.
- Document what the code and the architecture contract define. Never document a flag, variable, endpoint or configuration key that does not exist.
- Commits to the site use the scope `site`: `docs(site): …`.

## Publishing {#publishing}

`.github/workflows/docs.yml` builds the site and deploys it to GitHub Pages on every push to `main` that touches `docs/`, `ARCHITECTURE.md` or `CONTRIBUTING.md`, and on manual dispatch.

1. In the repository settings, under **Pages**, set the source to **GitHub Actions**.
2. Push to `main` or run the **docs** workflow by hand.

Without a custom domain the site is served from `https://<owner>.github.io/<repo>/`, and the workflow builds it with the base path `/<repo>/`.

### Custom domain {#custom-domain}

1. Add `docs/public/CNAME` containing the domain, such as `docs.example.com`, and push it to `main`. When the file exists, the workflow builds the site with the base path `/`.
2. At your DNS provider, point the domain at GitHub Pages: a `CNAME` record to `<owner>.github.io` for a subdomain, or GitHub's Pages `A` and `AAAA` records for an apex domain.
3. In the repository settings, under **Pages**, enter the same domain as the custom domain, wait for the DNS check, and turn on **Enforce HTTPS**.

With a workflow-based deployment, GitHub takes the custom domain from the Pages settings, not from the file, so step 3 is required. The `CNAME` file is what switches the base path.
