import type { TokenUsage } from '../types'

export function emptyTokenUsage(): TokenUsage {
  return {
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_creation_tokens: 0,
  }
}

export function hasAnyTokenUsage(usage: TokenUsage): boolean {
  return (
    usage.input_tokens > 0 ||
    usage.output_tokens > 0 ||
    usage.cache_read_tokens > 0 ||
    usage.cache_creation_tokens > 0
  )
}

/** Add usage from a Vercel AI SDK step (onStepFinish/result.usage) into the
 * running task total. Field names changed across AI SDK versions, so we read
 * both snake_case and camelCase variants defensively. */
export function addTokenUsageFromAiSdkStep(
  target: TokenUsage,
  step: { usage?: unknown } | null | undefined,
): void {
  const usage = step?.usage
  if (!usage || typeof usage !== 'object') return
  const raw = usage as Record<string, unknown>

  target.input_tokens += readNumber(
    raw,
    'inputTokens',
    'input_tokens',
    'promptTokens',
    'prompt_tokens',
  )
  target.output_tokens += readNumber(
    raw,
    'outputTokens',
    'output_tokens',
    'completionTokens',
    'completion_tokens',
  )
  target.cache_read_tokens += readNumber(
    raw,
    'cacheReadInputTokens',
    'cache_read_input_tokens',
    'cachedInputTokens',
    'cached_input_tokens',
  )
  target.cache_creation_tokens += readNumber(
    raw,
    'cacheCreationInputTokens',
    'cache_creation_input_tokens',
  )
}

function readNumber(
  source: Record<string, unknown>,
  ...keys: string[]
): number {
  for (const key of keys) {
    const value = source[key]
    if (typeof value === 'number' && Number.isFinite(value)) return value
  }
  return 0
}
