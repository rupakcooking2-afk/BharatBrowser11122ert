import type {
  HarnessAdapterDescriptor,
  HarnessAgentAdapter,
} from '@/modules/agents/agent-harness-types'

/**
 * UI visibility gate for VM-backed adapters. Hermes stays in backend
 * catalog/types, but the product exposes it only through an alpha capability.
 */
export function isAdapterHidden(
  adapter: HarnessAgentAdapter,
  hermesAgentSupported: boolean,
): boolean {
  return adapter === 'hermes' && !hermesAgentSupported
}

export function visibleAdapters(
  adapters: HarnessAdapterDescriptor[],
  hermesAgentSupported: boolean,
): HarnessAdapterDescriptor[] {
  return adapters.filter(
    (adapter) => !isAdapterHidden(adapter.id, hermesAgentSupported),
  )
}

export function visibleHarnessAgents<
  T extends { adapter: HarnessAgentAdapter },
>(agents: T[], hermesAgentSupported: boolean): T[] {
  return agents.filter(
    (agent) => !isAdapterHidden(agent.adapter, hermesAgentSupported),
  )
}
