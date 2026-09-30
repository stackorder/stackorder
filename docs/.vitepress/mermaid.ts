import type { MermaidConfig } from 'mermaid'

const slate = '#2f3e46'
const sand = '#f4f1de'
const terracotta = '#f2a28a'
const clay = '#a4472f'
const neutral50 = '#f9f8ef'
const neutral200 = '#d5d5c7'
const neutral300 = '#b7bab0'
const neutral400 = '#9a9f9a'
const neutral600 = '#626d6f'
const neutral700 = '#48555a'
const neutral900 = '#1a2930'
const neutral950 = '#0c1a21'

const fontFamily = "'Inter Variable', Inter, ui-sans-serif, system-ui, sans-serif"

export const mermaidConfig: MermaidConfig = {
  startOnLoad: false,
  securityLevel: 'loose',
  theme: 'base',
  fontFamily,
  fontSize: 14,
  flowchart: { nodeSpacing: 28, rankSpacing: 36, padding: 8, diagramPadding: 4, subGraphTitleMargin: { top: 4, bottom: 8 } },
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
}

const light = {
  darkMode: false,
  fontFamily,
  background: '#ffffff',
  primaryColor: sand,
  primaryTextColor: slate,
  primaryBorderColor: slate,
  secondaryColor: neutral50,
  secondaryTextColor: slate,
  secondaryBorderColor: neutral400,
  tertiaryColor: neutral50,
  tertiaryTextColor: slate,
  tertiaryBorderColor: neutral300,
  mainBkg: sand,
  nodeBorder: slate,
  nodeTextColor: slate,
  textColor: slate,
  titleColor: slate,
  lineColor: neutral600,
  clusterBkg: neutral50,
  clusterBorder: neutral300,
  edgeLabelBackground: '#ffffff',
  noteBkgColor: neutral50,
  noteTextColor: slate,
  noteBorderColor: clay,
  actorBkg: sand,
  actorBorder: slate,
  actorTextColor: slate,
  actorLineColor: neutral400,
  signalColor: slate,
  signalTextColor: slate,
  labelBoxBkgColor: sand,
  labelBoxBorderColor: slate,
  labelTextColor: slate,
  loopTextColor: slate,
  activationBkgColor: neutral200,
  activationBorderColor: slate,
  sequenceNumberColor: '#ffffff',
  transitionColor: neutral600,
  transitionLabelColor: slate,
  stateLabelColor: slate,
  specialStateColor: slate,
  innerEndBackground: slate,
  compositeBackground: neutral50,
  compositeTitleBackground: sand,
  altBackground: neutral50,
  attributeBackgroundColorOdd: '#ffffff',
  attributeBackgroundColorEven: neutral50,
}

const dark = {
  darkMode: true,
  fontFamily,
  background: neutral900,
  primaryColor: slate,
  primaryTextColor: sand,
  primaryBorderColor: neutral300,
  secondaryColor: neutral950,
  secondaryTextColor: sand,
  secondaryBorderColor: neutral600,
  tertiaryColor: neutral950,
  tertiaryTextColor: sand,
  tertiaryBorderColor: neutral700,
  mainBkg: slate,
  nodeBorder: neutral300,
  nodeTextColor: sand,
  textColor: sand,
  titleColor: sand,
  lineColor: neutral300,
  clusterBkg: neutral950,
  clusterBorder: neutral700,
  edgeLabelBackground: neutral900,
  noteBkgColor: slate,
  noteTextColor: sand,
  noteBorderColor: terracotta,
  actorBkg: slate,
  actorBorder: neutral300,
  actorTextColor: sand,
  actorLineColor: neutral600,
  signalColor: sand,
  signalTextColor: sand,
  labelBoxBkgColor: slate,
  labelBoxBorderColor: neutral300,
  labelTextColor: sand,
  loopTextColor: sand,
  activationBkgColor: neutral700,
  activationBorderColor: neutral300,
  sequenceNumberColor: neutral900,
  transitionColor: neutral300,
  transitionLabelColor: sand,
  stateLabelColor: sand,
  specialStateColor: sand,
  innerEndBackground: sand,
  compositeBackground: neutral950,
  compositeTitleBackground: slate,
  altBackground: neutral950,
  attributeBackgroundColorOdd: neutral900,
  attributeBackgroundColorEven: slate,
}

export function mermaidThemeVariables(isDark: boolean): MermaidConfig['themeVariables'] {
  return isDark ? dark : light
}
