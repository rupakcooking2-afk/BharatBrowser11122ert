import { randomUUID } from 'node:crypto'
import type { TokenUsage, UIMessageStreamEvent } from '../../types'

type JsonObject = Record<string, unknown>

export class ClaudeCodeStreamParser {
  private lastText: string | null = null
  private toolCallCount = 0
  private summedUsage: TokenUsage = emptyUsage()
  private resultUsage: TokenUsage | null = null

  pushLine(line: string): UIMessageStreamEvent[] {
    const trimmed = line.trim()
    if (!trimmed) return []

    let parsed: unknown
    try {
      parsed = JSON.parse(trimmed)
    } catch {
      return []
    }

    if (!isObject(parsed)) return []

    if (parsed.type === 'assistant') {
      return this.parseAssistantMessage(parsed)
    }
    if (parsed.type === 'user') {
      return this.parseUserMessage(parsed)
    }
    if (parsed.type === 'result') {
      if (typeof parsed.result === 'string') this.lastText = parsed.result
      const usage = extractUsage(parsed.usage)
      if (usage) this.resultUsage = usage
    }

    return []
  }

  getLastText(): string | null {
    return this.lastText
  }

  getToolCallCount(): number {
    return this.toolCallCount
  }

  /** Final token consumption for the run. Prefers the SDK's aggregate (`result.usage`)
   * when present, otherwise falls back to summing per-message usage. */
  getTokenUsage(): TokenUsage | null {
    if (this.resultUsage) return this.resultUsage
    return hasAnyUsage(this.summedUsage) ? this.summedUsage : null
  }

  private parseAssistantMessage(message: JsonObject): UIMessageStreamEvent[] {
    const content = contentBlocks(message)
    const events: UIMessageStreamEvent[] = []

    const inner = isObject(message.message) ? message.message : message
    const usage = extractUsage(inner.usage)
    if (usage) addUsage(this.summedUsage, usage)

    for (const block of content) {
      if (block.type === 'text' && typeof block.text === 'string') {
        const id = randomUUID()
        this.lastText = block.text
        events.push(
          { type: 'text-start', id },
          { type: 'text-delta', id, delta: block.text },
          { type: 'text-end', id },
        )
      } else if (
        block.type === 'tool_use' &&
        typeof block.id === 'string' &&
        typeof block.name === 'string'
      ) {
        this.toolCallCount++
        events.push({
          type: 'tool-input-available',
          toolCallId: block.id,
          toolName: block.name,
          input: block.input,
        })
      }
    }

    return events
  }

  private parseUserMessage(message: JsonObject): UIMessageStreamEvent[] {
    const content = contentBlocks(message)
    const events: UIMessageStreamEvent[] = []

    for (const block of content) {
      if (
        block.type !== 'tool_result' ||
        typeof block.tool_use_id !== 'string'
      ) {
        continue
      }

      if (block.is_error === true) {
        events.push({
          type: 'tool-output-error',
          toolCallId: block.tool_use_id,
          errorText: stringifyToolContent(block.content),
        })
      } else {
        events.push({
          type: 'tool-output-available',
          toolCallId: block.tool_use_id,
          output: normalizeToolContent(block.content),
        })
      }
    }

    return events
  }
}

export function shouldCaptureScreenshotForTool(toolName: string): boolean {
  if (!toolName.startsWith('mcp__browseros__')) return false
  return !toolName.endsWith('__take_screenshot')
}

function contentBlocks(message: JsonObject): JsonObject[] {
  const inner = isObject(message.message) ? message.message : message
  return Array.isArray(inner.content) ? inner.content.filter(isObject) : []
}

function isObject(value: unknown): value is JsonObject {
  return typeof value === 'object' && value !== null
}

function normalizeToolContent(content: unknown): unknown {
  if (!Array.isArray(content)) return content
  return content.map((item) => {
    if (
      isObject(item) &&
      item.type === 'text' &&
      typeof item.text === 'string'
    ) {
      return item.text
    }
    return item
  })
}

function stringifyToolContent(content: unknown): string {
  const normalized = normalizeToolContent(content)
  if (typeof normalized === 'string') return normalized
  try {
    return JSON.stringify(normalized)
  } catch {
    return String(normalized)
  }
}

function emptyUsage(): TokenUsage {
  return {
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_creation_tokens: 0,
  }
}

function hasAnyUsage(usage: TokenUsage): boolean {
  return (
    usage.input_tokens > 0 ||
    usage.output_tokens > 0 ||
    usage.cache_read_tokens > 0 ||
    usage.cache_creation_tokens > 0
  )
}

function addUsage(target: TokenUsage, addition: TokenUsage): void {
  target.input_tokens += addition.input_tokens
  target.output_tokens += addition.output_tokens
  target.cache_read_tokens += addition.cache_read_tokens
  target.cache_creation_tokens += addition.cache_creation_tokens
}

function readNumber(source: JsonObject, ...keys: string[]): number {
  for (const key of keys) {
    const value = source[key]
    if (typeof value === 'number' && Number.isFinite(value)) return value
  }
  return 0
}

function extractUsage(raw: unknown): TokenUsage | null {
  if (!isObject(raw)) return null
  const usage: TokenUsage = {
    input_tokens: readNumber(raw, 'input_tokens', 'inputTokens'),
    output_tokens: readNumber(raw, 'output_tokens', 'outputTokens'),
    cache_read_tokens: readNumber(
      raw,
      'cache_read_input_tokens',
      'cacheReadInputTokens',
    ),
    cache_creation_tokens: readNumber(
      raw,
      'cache_creation_input_tokens',
      'cacheCreationInputTokens',
    ),
  }
  return hasAnyUsage(usage) ? usage : null
}
