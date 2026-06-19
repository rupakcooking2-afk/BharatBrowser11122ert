import { beforeAll, describe, expect, it, mock } from 'bun:test'
import { type ComponentProps, createElement, type FC } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

type LabelProps = ComponentProps<'label'>

type SwitchProps = ComponentProps<'button'> & {
  checked?: boolean
  onCheckedChange?: (checked: boolean) => void
}

const BROWSEROS_PREFS = {
  MCP_PORT: 'browseros.server.mcp_port',
  PROVIDERS: 'browseros.providers',
  THIRD_PARTY_LLM_PROVIDERS: 'browseros.third_party_llm.providers',
  PROXY_PORT: 'browseros.server.proxy_port',
  SERVER_PORT: 'browseros.server.server_port',
  ALLOW_REMOTE_MCP: 'browseros.server.allow_remote_in_mcp',
  RESTART_SERVER: 'browseros.server.restart_requested',
  SHOW_LLM_CHAT: 'browseros.show_llm_chat',
  SHOW_TOOLBAR_LABELS: 'browseros.show_toolbar_labels',
  VERTICAL_TABS_ENABLED: 'browseros.vertical_tabs_enabled',
  INSTALL_ID: 'browseros.metrics_install_id',
} as const

const Feature = {
  ALPHA_FEATURES_SUPPORT: 'ALPHA_FEATURES_SUPPORT',
  OPENAI_COMPATIBLE_SUPPORT: 'OPENAI_COMPATIBLE_SUPPORT',
  MANAGED_MCP_SUPPORT: 'MANAGED_MCP_SUPPORT',
  PERSONALIZATION_SUPPORT: 'PERSONALIZATION_SUPPORT',
  CUSTOMIZATION_SUPPORT: 'CUSTOMIZATION_SUPPORT',
  WORKSPACE_FOLDER_SUPPORT: 'WORKSPACE_FOLDER_SUPPORT',
  PROXY_SUPPORT: 'PROXY_SUPPORT',
  PREVIOUS_CONVERSATION_ARRAY: 'PREVIOUS_CONVERSATION_ARRAY',
  NEWTAB_CHAT_SUPPORT: 'NEWTAB_CHAT_SUPPORT',
  VERTICAL_TABS_SUPPORT: 'VERTICAL_TABS_SUPPORT',
  CHATGPT_PRO_SUPPORT: 'CHATGPT_PRO_SUPPORT',
  GITHUB_COPILOT_SUPPORT: 'GITHUB_COPILOT_SUPPORT',
  QWEN_CODE_SUPPORT: 'QWEN_CODE_SUPPORT',
  CREDITS_SUPPORT: 'CREDITS_SUPPORT',
  AGENT_HARNESS_SUPPORT: 'AGENT_HARNESS_SUPPORT',
  HERMES_AGENT_SUPPORT: 'HERMES_AGENT_SUPPORT',
} as const

type FeatureValue = (typeof Feature)[keyof typeof Feature]
type CapabilitiesState = {
  browserOSVersion: number[] | null
  serverVersion: number[] | null
}

function compareVersionAtLeast(
  version: number[] | null,
  minimum: number[],
): boolean {
  if (!version) return false
  const maxLength = Math.max(version.length, minimum.length)
  for (let i = 0; i < maxLength; i++) {
    const actual = version[i] ?? 0
    const expected = minimum[i] ?? 0
    if (actual > expected) return true
    if (actual < expected) return false
  }
  return true
}

function resolveStaticFeatureSupport({
  isDevelopment,
  alphaFeaturesEnabled,
  requiresDevelopmentFlag = false,
  requiresAlphaFlag = false,
}: {
  isDevelopment: boolean
  alphaFeaturesEnabled: boolean
  requiresDevelopmentFlag?: boolean
  requiresAlphaFlag?: boolean
}): boolean | null {
  if (requiresDevelopmentFlag) return isDevelopment
  if (isDevelopment) return true
  if (requiresAlphaFlag) return alphaFeaturesEnabled
  return null
}

function resolveFeatureStaticSupport({
  feature,
  isDevelopment,
  alphaFeaturesEnabled,
}: {
  feature: FeatureValue
  isDevelopment: boolean
  alphaFeaturesEnabled: boolean
}): boolean | null {
  if (feature === Feature.HERMES_AGENT_SUPPORT) {
    return isDevelopment
  }
  if (feature === Feature.ALPHA_FEATURES_SUPPORT) {
    return alphaFeaturesEnabled
  }
  return isDevelopment ? true : null
}

function checkFeatureSupport(
  state: CapabilitiesState,
  feature: FeatureValue,
): boolean {
  if (feature === Feature.AGENT_HARNESS_SUPPORT) {
    return compareVersionAtLeast(state.browserOSVersion, [0, 46, 0, 0])
  }
  if (feature === Feature.HERMES_AGENT_SUPPORT) {
    return true
  }
  return false
}

const browserOSAdapter = {
  getBrowserosVersion: async () => null,
  getPref: async (name: string) =>
    new Promise<{ value: unknown } | null>((resolve) => {
      const getPref = globalThis.chrome?.browserOS?.getPref
      if (getPref) {
        getPref(name, resolve)
        return
      }
      resolve(name === BROWSEROS_PREFS.MCP_PORT ? { value: 9105 } : null)
    }),
  setPref: async () => true,
}

mock.module('sonner', () => ({
  toast: { error: () => {} },
}))

mock.module('@/components/ui/label', () => ({
  Label: ({ children, ...props }: LabelProps) =>
    createElement('label', props, children),
}))

mock.module('@/components/ui/switch', () => ({
  Switch: ({
    checked: _checked,
    onCheckedChange: _onCheckedChange,
    ...props
  }: SwitchProps) =>
    createElement('button', { type: 'button', role: 'switch', ...props }),
}))

mock.module('@/lib/browseros/adapter', () => ({
  BrowserOSAdapter: {
    getInstance: () => browserOSAdapter,
  },
  getBrowserOSAdapter: () => browserOSAdapter,
}))

mock.module('@/lib/browseros/prefs', () => ({
  BROWSEROS_PREFS,
}))

mock.module('@/lib/browseros/capabilities', () => ({
  Capabilities: {
    getStaticSupport: () => null,
    supports: async () => false,
    getBrowserOSVersion: async () => null,
    getServerVersion: async () => null,
    initialize: async () => {},
    reset: () => {},
  },
  Feature,
  checkFeatureSupport,
  resolveFeatureStaticSupport,
  resolveStaticFeatureSupport,
}))

let ToolbarSettingsCard: FC

beforeAll(async () => {
  ToolbarSettingsCard = (await import('./ToolbarSettingsCard'))
    .ToolbarSettingsCard
})

function renderCard() {
  return renderToStaticMarkup(createElement(ToolbarSettingsCard))
}

describe('ToolbarSettingsCard', () => {
  it('renders supported toolbar settings without the unsupported Hub control', () => {
    const html = renderCard()

    expect(html).toContain('Show Chat Button')
    expect(html).toContain('Show Button Labels')
    expect(html).not.toContain('Show Hub Button')
    expect(html).not.toContain('show-llm-hub')
  })
})
