import { describe, expect, it } from 'bun:test'
import { z } from 'zod'
import type { BrowserSession } from '../../../src/browser/core/session'
import {
  defineTool,
  errorResult,
  executeTool,
} from '../../../src/tools/browser/framework'
import { registerBrowserTools } from '../../../src/tools/browser/register'

type RegisteredHandler = (args: Record<string, unknown>) => Promise<{
  content: unknown
  isError?: boolean
  structuredContent?: unknown
}>

function createFakeServer() {
  const handlers = new Map<string, RegisteredHandler>()

  return {
    handlers,
    server: {
      registerTool(
        name: string,
        _config: { description: string; inputSchema?: unknown },
        handler: RegisteredHandler,
      ) {
        handlers.set(name, handler)
      },
    },
  }
}

function textOf(result: { content?: unknown }): string {
  if (!Array.isArray(result.content)) return ''
  return result.content
    .filter(
      (item): item is { type: 'text'; text: string } =>
        typeof item === 'object' &&
        item !== null &&
        'type' in item &&
        item.type === 'text' &&
        'text' in item &&
        typeof item.text === 'string',
    )
    .map((item) => item.text)
    .join('\n')
}

describe('browser tool framework post-actions', () => {
  it('runs compact ToolResponse post-actions after the handler', async () => {
    const events: string[] = []
    const postActionTool = defineTool({
      name: 'post_action_test',
      description: 'Test post-action execution.',
      input: z.object({ page: z.number().int() }),
      handler: async (args, _ctx, response) => {
        events.push('handler')
        response.text('handler output')
        response.data({ page: args.page, url: 'https://example.com/post' })
        response.includeSnapshot(args.page)
      },
    })
    const session = {
      observe: (page: number) => ({
        snapshot: async () => {
          events.push(`snapshot:${page}`)
          return { text: '- button "Submit" [ref=e1]' }
        },
      }),
      pages: {
        getInfo: () => ({ url: 'https://example.com/post' }),
        getTabId: () => 1234,
      },
    } as unknown as BrowserSession

    const result = await executeTool(postActionTool, { page: 7 }, { session })
    const text = textOf(result)

    expect(events).toEqual(['handler', 'snapshot:7'])
    expect(result.isError).toBeFalsy()
    expect(result.structuredContent).toEqual({
      page: 7,
      url: 'https://example.com/post',
    })
    expect(result.metadata).toEqual({ tabId: 1234 })
    expect(text.indexOf('handler output')).toBeLessThan(
      text.indexOf('--- Additional context'),
    )
    expect(text).toContain('[Page 7 snapshot]')
    expect(text).toContain('[UNTRUSTED_PAGE_CONTENT')
    expect(text).toContain('- button "Submit" [ref=e1]')
    expect(text).toContain('[END_UNTRUSTED_PAGE_CONTENT')
  })

  it('keeps compact error results from running undeclared post-actions', async () => {
    const errorTool = defineTool({
      name: 'error_test',
      description: 'Test error result handling.',
      input: z.object({ page: z.number().int() }),
      handler: async () => errorResult('error_test: nope'),
    })
    const session = {
      observe: () => ({
        snapshot: async () => {
          throw new Error('snapshot should not run')
        },
      }),
      pages: {
        getTabId: () => undefined,
      },
    } as unknown as BrowserSession

    const result = await executeTool(errorTool, { page: 2 }, { session })

    expect(result.isError).toBe(true)
    expect(textOf(result)).toBe('error_test: nope')
  })

  it('attaches navigate snapshots through the shared post-action path', async () => {
    const fake = createFakeServer()
    const calls: string[] = []
    let currentUrl = 'https://example.com/before'
    const session = {
      nav: () => ({
        goto: async (url: string) => calls.push(`goto:${url}`),
      }),
      observe: (page: number) => ({
        snapshot: async () => {
          calls.push(`snapshot:${page}`)
          return { text: '- heading "Arrived" [ref=e1]' }
        },
      }),
      pages: {
        getInfo: () => {
          calls.push('getInfo')
          return { url: currentUrl }
        },
        refresh: async () => {
          calls.push('refresh')
          currentUrl = 'https://example.com/after'
          return { url: currentUrl }
        },
        getTabId: () => undefined,
      },
    } as unknown as BrowserSession

    registerBrowserTools(fake.server as never, session)

    const result = await fake.handlers.get('navigate')?.({
      page: 9,
      action: 'url',
      url: 'https://example.com/before',
    })
    const text = textOf(result ?? {})

    expect(result?.isError).toBeFalsy()
    expect(result?.structuredContent).toEqual({
      page: 9,
      url: 'https://example.com/after',
    })
    expect(calls).toEqual([
      'goto:https://example.com/before',
      'refresh',
      'snapshot:9',
      'getInfo',
    ])
    expect(text).toContain('navigated (url) -> https://example.com/after')
    expect(text).toContain('origin=https://example.com/after')
    expect(text.indexOf('navigated (url)')).toBeLessThan(
      text.indexOf('--- Additional context'),
    )
    expect(text).toContain('[Page 9 snapshot]')
    expect(text).toContain('[UNTRUSTED_PAGE_CONTENT')
    expect(text).toContain('- heading "Arrived" [ref=e1]')
  })

  it('does not snapshot navigate validation errors', async () => {
    const fake = createFakeServer()
    const calls: string[] = []
    const session = {
      nav: () => ({
        goto: async () => calls.push('goto'),
      }),
      observe: () => ({
        snapshot: async () => calls.push('snapshot'),
      }),
      pages: {
        getTabId: () => undefined,
      },
    } as unknown as BrowserSession

    registerBrowserTools(fake.server as never, session)

    const result = await fake.handlers.get('navigate')?.({
      page: 9,
      action: 'url',
    })

    expect(result?.isError).toBe(true)
    expect(result?.content).toEqual([
      { type: 'text', text: 'navigate: url is required for action="url".' },
    ])
    expect(calls).toEqual([])
  })
})
