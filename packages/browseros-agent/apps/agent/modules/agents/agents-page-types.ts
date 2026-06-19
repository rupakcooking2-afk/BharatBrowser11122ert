import type { HarnessAgentAdapter } from './agent-harness-types'

export type CreateAgentRuntime = HarnessAgentAdapter

export interface ProviderOption {
  id: string
  type: string
  name: string
  modelId: string
  baseUrl?: string
  apiKey?: string
}

export interface AgentListItem {
  key: string
  agentId: string
  name: string
  source: 'agent-harness'
  runtimeLabel: string
  modelLabel: string
  detail: string
  canChat: boolean
  canDelete: boolean
}
