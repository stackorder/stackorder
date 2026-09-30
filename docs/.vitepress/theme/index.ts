import type { Theme } from 'vitepress'
import DefaultTheme from 'vitepress/theme-without-fonts'
import '@fontsource/lato/400.css'
import '@fontsource/lato/700.css'
import '@fontsource-variable/inter/wght.css'
import '@fontsource-variable/inter/wght-italic.css'
import '@fontsource-variable/jetbrains-mono/wght.css'
import Mermaid from './Mermaid.vue'
import Screenshot from './Screenshot.vue'
import './custom.css'

export default {
  extends: DefaultTheme,
  enhanceApp({ app }) {
    app.component('Mermaid', Mermaid)
    app.component('Screenshot', Screenshot)
  },
} satisfies Theme
