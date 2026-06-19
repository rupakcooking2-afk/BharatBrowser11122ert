import { describe, expect, it } from 'bun:test'
import type { LlmProviderConfig } from '@/lib/llm-providers/types'
import type {
  HarnessAdapterDescriptor,
  HarnessAgent,
  HarnessAgentAdapter,
} from '@/modules/agents/agent-harness-types'
// Relative value import: `bun test` resolves tsconfig paths from the package
// root, where `@/` is undefined — only erased `import type` works via `@/`.
import { buildSidepanelChatTargets } from '../../modules/chat/sidepanel-chat-targets'
import { isAdapterHidden, visibleAdapters } from './adapter-visibility'

function makeAdapter(id: HarnessAgentAdapter): HarnessAdapterDescriptor {
  return {
    id,
    name: id,
    defaultModelId: 'model',
    defaultReasoningEffort: 'medium',
    modelControl: 'best-effort',
    models: [],
    reasoningEfforts: [],
  }
}

function makeAgent(id: string, adapter: HarnessAgentAdapter): HarnessAgent {
  return {
    id,
    name: id,
    adapter,
    permissionMode: 'approve-all',
    sessionKey: 'session',
    createdAt: 0,
    updatedAt: 0,
  }
}

function makeProvider(id: string): LlmProviderConfig {
  return {
    id,
    type: 'browseros',
    name: id,
    modelId: 'model',
    supportsImages: false,
    contextWindow: 1000,
    temperature: 0.2,
    createdAt: 0,
    updatedAt: 0,
  }
}

describe('isAdapterHidden', () => {
  it('hides hermes when the alpha capability is disabled', () => {
    expect(isAdapterHidden('hermes', false)).toBe(true)
  })

  it('shows hermes when the alpha capability is enabled', () => {
    expect(isAdapterHidden('hermes', true)).toBe(false)
  })

  it('shows claude and codex regardless of the Hermes capability', () => {
    expect(isAdapterHidden('claude', false)).toBe(false)
    expect(isAdapterHidden('codex', false)).toBe(false)
  })
})

describe('visibleAdapters', () => {
  const adapters = [
    makeAdapter('claude'),
    makeAdapter('hermes'),
    makeAdapter('codex'),
  ]

  it('drops hermes descriptors when the alpha capability is disabled', () => {
    const result = visibleAdapters(adapters, false)
    expect(result.map((adapter) => adapter.id)).toEqual(['claude', 'codex'])
  })

  it('keeps hermes descriptors when the alpha capability is enabled', () => {
    const result = visibleAdapters(adapters, true)
    expect(result.map((adapter) => adapter.id)).toEqual([
      'claude',
      'hermes',
      'codex',
    ])
  })
})

describe('buildSidepanelChatTargets adapter visibility', () => {
  it('omits acp targets for hermes-backed agents when alpha is disabled', () => {
    const targets = buildSidepanelChatTargets({
      providers: [],
      adapters: [
        makeAdapter('claude'),
        makeAdapter('codex'),
        makeAdapter('hermes'),
      ],
      agents: [
        makeAgent('a', 'claude'),
        makeAgent('b', 'hermes'),
        makeAgent('c', 'codex'),
      ],
      hermesAgentSupported: false,
    })
    expect(
      targets
        .filter((target) => target.kind === 'acp')
        .map((target) => target.id),
    ).toEqual(['a', 'c'])
  })

  it('keeps acp targets for hermes-backed agents when alpha is enabled', () => {
    const targets = buildSidepanelChatTargets({
      providers: [],
      adapters: [
        makeAdapter('claude'),
        makeAdapter('codex'),
        makeAdapter('hermes'),
      ],
      agents: [
        makeAgent('a', 'claude'),
        makeAgent('b', 'hermes'),
        makeAgent('c', 'codex'),
      ],
      hermesAgentSupported: true,
    })
    expect(
      targets
        .filter((target) => target.kind === 'acp')
        .map((target) => target.id),
    ).toEqual(['a', 'b', 'c'])
  })

  it('keeps one llm target per provider', () => {
    const targets = buildSidepanelChatTargets({
      providers: [makeProvider('p1'), makeProvider('p2')],
      adapters: [],
      agents: [],
    })
    expect(
      targets
        .filter((target) => target.kind === 'llm')
        .map((target) => target.id),
    ).toEqual(['p1', 'p2'])
  })
})
