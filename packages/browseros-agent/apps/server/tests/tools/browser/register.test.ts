import { describe, expect, it } from 'bun:test'
import {
  existsSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { TOOL_LIMITS } from '@browseros/shared/constants/limits'
import { z } from 'zod'
import {
  CHAT_MODE_ALLOWED_TOOLS,
  LEGACY_CHAT_MODE_ALLOWED_TOOLS,
} from '../../../src/agent/chat-mode'
import {
  buildBrowserToolSet,
  buildLegacyBrowserToolSet,
} from '../../../src/agent/tool-adapter'
import type { Browser } from '../../../src/browser/browser'
import type { BrowserSession } from '../../../src/browser/core/session'
import {
  getToolOutputDir,
  TOOL_OUTPUT_DIR_MODE,
  TOOL_OUTPUT_FILE_MODE,
} from '../../../src/lib/browseros-dir'
import {
  defineTool,
  executeTool,
  textResult,
} from '../../../src/tools/browser/framework'
import { registerBrowserTools } from '../../../src/tools/browser/register'
import { BROWSER_TOOLS } from '../../../src/tools/browser/registry'
import { createReadTool } from '../../../src/tools/filesystem/read'
import { get_page_content as legacyGetPageContent } from '../../../src/tools/legacy/browser/snapshot'
import { executeTool as executeLegacyTool } from '../../../src/tools/legacy/framework'

type RegisteredHandler = (args: Record<string, unknown>) => Promise<{
  content: unknown
  isError?: boolean
  structuredContent?: unknown
}>

function createFakeServer() {
  const handlers = new Map<string, RegisteredHandler>()
  const configs = new Map<
    string,
    { description: string; inputSchema?: unknown }
  >()

  return {
    handlers,
    configs,
    server: {
      registerTool(
        name: string,
        config: { description: string; inputSchema?: unknown },
        handler: RegisteredHandler,
      ) {
        configs.set(name, config)
        handlers.set(name, handler)
      },
    },
  }
}

async function withBrowserosDir<T>(run: () => Promise<T>): Promise<T> {
  const previous = process.env.BROWSEROS_DIR
  const browserosDir = mkdtempSync(join(tmpdir(), 'browseros-output-test-'))
  process.env.BROWSEROS_DIR = browserosDir
  try {
    return await run()
  } finally {
    if (previous === undefined) {
      delete process.env.BROWSEROS_DIR
    } else {
      process.env.BROWSEROS_DIR = previous
    }
    rmSync(browserosDir, { recursive: true, force: true })
  }
}

async function expectBrowserToolOutputPath(
  filePath: string | undefined,
): Promise<void> {
  expect(filePath).toBeTruthy()
  const path = filePath ?? ''
  const outputDir = await getToolOutputDir()
  expect(realpathSync(dirname(path))).toBe(realpathSync(outputDir))
  if (process.platform !== 'win32') {
    expect(statSync(outputDir).mode & 0o777).toBe(TOOL_OUTPUT_DIR_MODE)
    expect(statSync(path).mode & 0o777).toBe(TOOL_OUTPUT_FILE_MODE)
  }
}

describe('registerBrowserTools', () => {
  it('registers the compact browser tool surface', () => {
    const fake = createFakeServer()
    const session = { pages: {} } as unknown as BrowserSession

    registerBrowserTools(fake.server as never, session)

    expect([...fake.handlers.keys()]).toEqual(BROWSER_TOOLS.map((t) => t.name))
    expect(fake.handlers.size).toBe(10)
    expect(fake.configs.get('tabs')?.inputSchema).toBeDefined()
  })

  it('applies scoped defaults when opening a new tab', async () => {
    const fake = createFakeServer()
    const calls: Array<{
      url: string
      opts?: {
        background?: boolean
        hidden?: boolean
        windowId?: number
        tabGroupId?: string
      }
    }> = []
    const session = {
      pages: {
        newPage: async (
          url: string,
          opts?: {
            background?: boolean
            hidden?: boolean
            windowId?: number
            tabGroupId?: string
          },
        ) => {
          calls.push({ url, opts })
          return 42
        },
      },
    } as unknown as BrowserSession

    registerBrowserTools(fake.server as never, session, {
      defaultWindowId: 7,
      defaultTabGroupId: 'group-a',
    })

    const result = await fake.handlers.get('tabs')?.({
      action: 'new',
      url: 'https://example.com',
    })

    expect(result?.isError).toBeFalsy()
    expect(result?.structuredContent).toEqual({ page: 42 })
    expect(calls).toEqual([
      {
        url: 'https://example.com',
        opts: {
          background: true,
          hidden: false,
          windowId: 7,
          tabGroupId: 'group-a',
        },
      },
    ])
  })

  it('runs page-context JavaScript through the page session', async () => {
    const fake = createFakeServer()
    const evaluateCalls: Array<Record<string, unknown>> = []
    const session = {
      pages: {
        getSession: async () => ({
          session: {
            Runtime: {
              evaluate: async (params: Record<string, unknown>) => {
                evaluateCalls.push(params)
                return { result: { value: 'page-value' } }
              },
            },
          },
        }),
        getInfo: () => ({ url: 'https://example.com/run' }),
      },
    } as unknown as BrowserSession

    registerBrowserTools(fake.server as never, session)

    const result = await fake.handlers.get('run')?.({
      page: 3,
      code: 'return document.title',
      timeout: 1234,
    })

    expect(result?.isError).toBeFalsy()
    expect(result?.structuredContent).toEqual({ page: 3, value: 'page-value' })
    expect(result?.content).toEqual([
      expect.objectContaining({
        type: 'text',
        text: expect.stringContaining('[UNTRUSTED_PAGE_CONTENT'),
      }),
    ])
    expect(result?.content).toEqual([
      expect.objectContaining({
        type: 'text',
        text: expect.stringContaining('page-value'),
      }),
    ])
    expect(evaluateCalls).toHaveLength(1)
    expect(evaluateCalls[0]).toMatchObject({
      awaitPromise: true,
      returnByValue: true,
      timeout: 1234,
      userGesture: true,
    })
    expect(String(evaluateCalls[0]?.expression)).toContain(
      'return document.title',
    )
  })

  it('returns a full snapshot when diff sees a URL change', async () => {
    const fake = createFakeServer()
    const session = {
      observe: () => ({
        diff: async () => ({
          changed: true,
          text: '- main\n  - heading "New page"',
          added: 0,
          removed: 0,
          urlChanged: true,
          beforeUrl: 'https://example.com/old',
          afterUrl: 'https://example.com/new',
        }),
      }),
      pages: {
        getInfo: () => ({ url: 'https://example.com/new' }),
      },
    } as unknown as BrowserSession

    registerBrowserTools(fake.server as never, session)

    const result = await fake.handlers.get('diff')?.({ page: 1 })

    expect(result?.isError).toBeFalsy()
    expect(result?.structuredContent).toEqual({
      added: 0,
      removed: 0,
      urlChanged: true,
      beforeUrl: 'https://example.com/old',
      afterUrl: 'https://example.com/new',
    })
    expect(result?.content).toEqual([
      expect.objectContaining({
        type: 'text',
        text: expect.stringContaining('URL changed;'),
      }),
    ])
    expect(result?.content).toEqual([
      expect.objectContaining({
        type: 'text',
        text: expect.stringContaining(
          'returning full current snapshot instead of a diff',
        ),
      }),
    ])
    expect(result?.content).toEqual([
      expect.objectContaining({
        type: 'text',
        text: expect.stringContaining('[UNTRUSTED_PAGE_CONTENT'),
      }),
    ])
    expect(result?.content).toEqual([
      expect.objectContaining({
        type: 'text',
        text: expect.stringContaining('- heading "New page"'),
      }),
    ])
  })

  it('wraps same-url diffs with the observed current URL', async () => {
    const fake = createFakeServer()
    const session = {
      observe: () => ({
        diff: async () => ({
          changed: true,
          text: '+   button "Saved" [ref=e1]\n1 added, 0 removed',
          added: 1,
          removed: 0,
          afterUrl: 'https://example.com/current',
        }),
      }),
      pages: {
        getInfo: () => ({ url: 'https://example.com/stale' }),
      },
    } as unknown as BrowserSession

    registerBrowserTools(fake.server as never, session)

    const result = await fake.handlers.get('diff')?.({ page: 1 })

    expect(result?.isError).toBeFalsy()
    expect(result?.structuredContent).toEqual({ added: 1, removed: 0 })
    expect(result?.content).toEqual([
      expect.objectContaining({
        type: 'text',
        text: expect.stringContaining('origin=https://example.com/current'),
      }),
    ])
    expect(result?.content).toEqual([
      expect.objectContaining({
        type: 'text',
        text: expect.not.stringContaining('origin=https://example.com/stale'),
      }),
    ])
  })

  it('returns a full snapshot when act readback sees a URL change', async () => {
    const fake = createFakeServer()
    const calls: string[] = []
    const session = {
      input: () => ({
        click: async () => calls.push('click'),
      }),
      observe: () => ({
        diff: async () => ({
          changed: true,
          text: '- main\n  - heading "Destination"',
          added: 0,
          removed: 0,
          urlChanged: true,
          beforeUrl: 'https://example.com/start',
          afterUrl: 'https://example.com/destination',
        }),
      }),
      pages: {
        getInfo: () => ({ url: 'https://example.com/destination' }),
      },
    } as unknown as BrowserSession

    registerBrowserTools(fake.server as never, session)

    const result = await fake.handlers.get('act')?.({
      page: 1,
      kind: 'click',
      ref: 'e1',
    })

    expect(result?.isError).toBeFalsy()
    expect(result?.structuredContent).toEqual({
      kind: 'click',
      changed: true,
      urlChanged: true,
      beforeUrl: 'https://example.com/start',
      afterUrl: 'https://example.com/destination',
    })
    expect(result?.content).toEqual([
      expect.objectContaining({
        type: 'text',
        text: expect.stringContaining('page URL changed;'),
      }),
    ])
    expect(result?.content).toEqual([
      expect.objectContaining({
        type: 'text',
        text: expect.stringContaining(
          'returning full current snapshot instead of a diff',
        ),
      }),
    ])
    expect(result?.content).toEqual([
      expect.objectContaining({
        type: 'text',
        text: expect.stringContaining('[UNTRUSTED_PAGE_CONTENT'),
      }),
    ])
    expect(result?.content).toEqual([
      expect.objectContaining({
        type: 'text',
        text: expect.stringContaining('- heading "Destination"'),
      }),
    ])
    expect(calls).toEqual(['click'])
  })

  it('caps page-context JavaScript timeouts', async () => {
    const fake = createFakeServer()
    const evaluateCalls: Array<Record<string, unknown>> = []
    const session = {
      pages: {
        getSession: async () => ({
          session: {
            Runtime: {
              evaluate: async (params: Record<string, unknown>) => {
                evaluateCalls.push(params)
                return { result: { value: 'ok' } }
              },
            },
          },
        }),
        getInfo: () => ({ url: 'https://example.com/run' }),
      },
    } as unknown as BrowserSession

    registerBrowserTools(fake.server as never, session)

    const result = await fake.handlers.get('run')?.({
      page: 3,
      code: 'return true',
      timeout: 120_000,
    })

    expect(result?.isError).toBeFalsy()
    expect(evaluateCalls[0]?.timeout).toBe(30_000)
  })

  it('caps large read results and writes the full content to a BrowserOS output file', async () => {
    await withBrowserosDir(async () => {
      const fake = createFakeServer()
      const largeText = 'x'.repeat(
        TOOL_LIMITS.INLINE_PAGE_CONTENT_MAX_CHARS + 1,
      )
      const session = {
        pages: {
          getSession: async () => ({
            session: {
              Runtime: {
                evaluate: async () => ({ result: { value: largeText } }),
              },
            },
          }),
          getInfo: () => ({ url: 'https://example.com' }),
        },
      } as unknown as BrowserSession

      registerBrowserTools(fake.server as never, session)

      const result = await fake.handlers.get('read')?.({
        page: 1,
        format: 'text',
      })

      expect(result?.isError).toBeFalsy()
      expect(result?.structuredContent).toMatchObject({
        page: 1,
        format: 'text',
        contentLength: largeText.length,
        writtenToFile: true,
      })
      expect(result?.content).toEqual(
        expect.arrayContaining([
          expect.objectContaining({
            type: 'text',
            text: expect.stringContaining('Content truncated'),
          }),
        ]),
      )
      const data = result?.structuredContent as { path?: string } | undefined
      await expectBrowserToolOutputPath(data?.path)
      expect(readFileSync(data?.path ?? '', 'utf8')).toBe(largeText)
    })
  })

  it('keeps small snapshots inline with the existing structured content', async () => {
    const fake = createFakeServer()
    const session = {
      observe: () => ({
        snapshot: async () => ({ text: '- button "Save" [ref=e1]' }),
      }),
      pages: {
        getInfo: () => ({ url: 'https://example.com/small' }),
      },
    } as unknown as BrowserSession

    registerBrowserTools(fake.server as never, session)

    const result = await fake.handlers.get('snapshot')?.({ page: 2 })

    expect(result?.isError).toBeFalsy()
    expect(result?.structuredContent).toEqual({ page: 2 })
    expect(result?.content).toEqual([
      expect.objectContaining({
        type: 'text',
        text: expect.stringContaining('[UNTRUSTED_PAGE_CONTENT'),
      }),
    ])
    expect(result?.content).toEqual([
      expect.objectContaining({
        type: 'text',
        text: expect.stringContaining('- button "Save" [ref=e1]'),
      }),
    ])
    expect(JSON.stringify(result?.structuredContent)).not.toContain('path')
  })

  it('writes very large snapshots to a BrowserOS output markdown file', async () => {
    await withBrowserosDir(async () => {
      const fake = createFakeServer()
      const largeSnapshot = Array.from(
        { length: 5001 },
        (_, i) => `node-${i}`,
      ).join(' ')
      const session = {
        observe: () => ({
          snapshot: async () => ({ text: largeSnapshot }),
        }),
        pages: {
          getInfo: () => ({ url: 'https://example.com/large' }),
        },
      } as unknown as BrowserSession
      registerBrowserTools(fake.server as never, session)

      const result = await fake.handlers.get('snapshot')?.({ page: 4 })

      expect(result?.isError).toBeFalsy()
      const data = result?.structuredContent as
        | {
            page: number
            path: string
            contentLength: number
            wordCount: number
            writtenToFile: boolean
          }
        | undefined
      expect(data).toMatchObject({
        page: 4,
        wordCount: 5001,
        writtenToFile: true,
      })
      const savedPath = data?.path
      await expectBrowserToolOutputPath(savedPath)
      expect(savedPath?.endsWith('.md')).toBe(true)
      expect(result?.content).toEqual([
        expect.objectContaining({
          type: 'text',
          text: expect.stringContaining('Large snapshot'),
        }),
      ])
      expect(result?.content).toEqual([
        expect.objectContaining({
          type: 'text',
          text: expect.stringContaining(savedPath ?? ''),
        }),
      ])
      expect(existsSync(savedPath ?? '')).toBe(true)

      const savedContent = readFileSync(savedPath ?? '', 'utf8')
      expect(savedContent).toContain('[UNTRUSTED_PAGE_CONTENT')
      expect(savedContent).toContain('[END_UNTRUSTED_PAGE_CONTENT')
      expect(savedContent).toContain('node-0')
      expect(savedContent).toContain('node-5000')
      expect(data?.contentLength).toBe(savedContent.length)
    })
  })

  it('writes very long unbroken snapshots to a BrowserOS output markdown file', async () => {
    await withBrowserosDir(async () => {
      const fake = createFakeServer()
      const largeSnapshot = 'x'.repeat(50_001)
      const session = {
        observe: () => ({
          snapshot: async () => ({ text: largeSnapshot }),
        }),
        pages: {
          getInfo: () => ({ url: 'https://example.com/long-token' }),
        },
      } as unknown as BrowserSession
      registerBrowserTools(fake.server as never, session)

      const result = await fake.handlers.get('snapshot')?.({ page: 5 })
      const data = result?.structuredContent as
        | {
            path: string
            wordCount: number
            writtenToFile: boolean
          }
        | undefined

      expect(result?.isError).toBeFalsy()
      expect(data).toMatchObject({
        wordCount: 1,
        writtenToFile: true,
      })
      const savedPath = data?.path
      expect(result?.content).toEqual([
        expect.objectContaining({
          type: 'text',
          text: expect.stringContaining(savedPath ?? ''),
        }),
      ])
      await expectBrowserToolOutputPath(savedPath)
      expect(readFileSync(savedPath ?? '', 'utf8')).toContain(largeSnapshot)
    })
  })

  it('returns read errors for page-side exceptions', async () => {
    const fake = createFakeServer()
    const session = {
      pages: {
        getSession: async () => ({
          session: {
            Runtime: {
              evaluate: async () => ({
                exceptionDetails: { text: 'SyntaxError: invalid selector' },
              }),
            },
          },
        }),
      },
    } as unknown as BrowserSession

    registerBrowserTools(fake.server as never, session)

    const result = await fake.handlers.get('read')?.({
      page: 1,
      format: 'text',
      selector: '[',
    })

    expect(result?.isError).toBe(true)
    expect(result?.content).toEqual([
      { type: 'text', text: 'read: SyntaxError: invalid selector' },
    ])
  })

  it('routes compact tools through browser session APIs', async () => {
    const fake = createFakeServer()
    const calls: string[] = []
    const input = {
      click: async () => calls.push('click'),
      clickAt: async () => calls.push('clickAt'),
      type: async () => calls.push('type'),
      typeAt: async () => calls.push('typeAt'),
      fill: async () => calls.push('fill'),
      press: async () => calls.push('press'),
      hover: async () => calls.push('hover'),
      hoverAt: async () => calls.push('hoverAt'),
      selectOption: async () => calls.push('selectOption'),
      scroll: async () => calls.push('scroll'),
      dragAt: async () => calls.push('dragAt'),
    }
    const session = {
      nav: () => ({
        goto: async () => calls.push('goto'),
        back: async () => calls.push('back'),
        forward: async () => calls.push('forward'),
        reload: async () => calls.push('reload'),
      }),
      observe: () => ({
        snapshot: async () => {
          calls.push('snapshot')
          return { text: '- button "Save" [ref=e1]' }
        },
        diff: async () => {
          calls.push('diff')
          return { changed: true, text: '+ saved', added: 1, removed: 0 }
        },
      }),
      input: () => input,
      pages: {
        getInfo: () => ({ url: 'https://example.com' }),
        refresh: async () => ({ url: 'https://example.com' }),
        getSession: async () => ({
          session: {
            Runtime: {
              evaluate: async () => ({ result: { value: true } }),
            },
            Page: {
              captureScreenshot: async () => ({ data: 'png-data' }),
            },
          },
        }),
      },
    } as unknown as BrowserSession

    registerBrowserTools(fake.server as never, session)

    expect(
      await fake.handlers.get('navigate')?.({
        page: 1,
        action: 'url',
        url: 'https://example.com',
      }),
    ).toMatchObject({ isError: undefined })
    expect(await fake.handlers.get('snapshot')?.({ page: 1 })).toMatchObject({
      structuredContent: { page: 1 },
    })
    expect(await fake.handlers.get('diff')?.({ page: 1 })).toMatchObject({
      structuredContent: { added: 1, removed: 0 },
    })
    expect(
      await fake.handlers.get('grep')?.({
        page: 1,
        pattern: 'save',
        over: 'ax',
      }),
    ).toMatchObject({ structuredContent: { count: 1 } })
    expect(await fake.handlers.get('screenshot')?.({ page: 1 })).toMatchObject({
      content: [{ type: 'image', data: 'png-data', mimeType: 'image/png' }],
    })
    expect(
      await fake.handlers.get('wait')?.({
        page: 1,
        for: 'selector',
        value: '#ready',
      }),
    ).toMatchObject({ structuredContent: { matched: true } })
    expect(
      await fake.handlers.get('act')?.({
        page: 1,
        kind: 'click',
        ref: 'e1',
      }),
    ).toMatchObject({ structuredContent: { kind: 'click', changed: true } })
    expect(calls).toEqual([
      'goto',
      'snapshot',
      'snapshot',
      'diff',
      'snapshot',
      'click',
      'diff',
    ])
  })
})

describe('buildBrowserToolSet', () => {
  it('builds the compact browser tool surface', () => {
    const session = { pages: {} } as unknown as BrowserSession
    const tools = buildBrowserToolSet(session)

    expect(tools.tabs).toBeDefined()
    expect(tools.new_page).toBeUndefined()
    expect(Object.keys(tools)).toEqual(BROWSER_TOOLS.map((t) => t.name))
  })

  it('builds the legacy browser tool surface', () => {
    const tools = buildLegacyBrowserToolSet({} as never)

    expect(tools.new_page).toBeDefined()
    expect(tools.get_bookmarks).toBeDefined()
    expect(tools.browseros_info).toBeDefined()
    expect(tools.tabs).toBeUndefined()
    expect(Object.keys(tools).length).toBeGreaterThan(50)
  })

  it('writes legacy large page content to BrowserOS output files readable by filesystem_read', async () => {
    await withBrowserosDir(async () => {
      const largeText = `${'legacy output token\n'.repeat(4)}${'x'.repeat(
        TOOL_LIMITS.INLINE_PAGE_CONTENT_MAX_CHARS + 1,
      )}`
      const browser = {
        contentAsMarkdown: async () => largeText,
        getTabIdForPage: () => undefined,
      } as unknown as Browser

      const result = await executeLegacyTool(
        legacyGetPageContent,
        { page: 1 },
        { browser, directories: {} },
        AbortSignal.timeout(1_000),
      )
      const data = result.structuredContent as { path?: string } | undefined
      expect(result.isError).toBeUndefined()
      const savedPath = data?.path
      await expectBrowserToolOutputPath(savedPath)

      const readTool = createReadTool(process.cwd())
      const readResult = (await readTool.execute?.(
        { path: savedPath as string },
        {
          toolCallId: 'read-legacy-output',
          messages: [],
        } as never,
      )) as { text?: string; isError?: boolean }
      expect(readResult.isError).toBeUndefined()
      expect(readResult.text).toContain('legacy output token')
    })
  })

  it('uses legacy tool names for legacy chat-mode filtering', () => {
    const legacyTools = buildLegacyBrowserToolSet({} as never)
    const chatTools = Object.fromEntries(
      Object.entries(legacyTools).filter(([name]) =>
        LEGACY_CHAT_MODE_ALLOWED_TOOLS.has(name),
      ),
    )

    expect(Object.keys(chatTools).sort()).toEqual([
      'evaluate_script',
      'get_page_content',
      'list_pages',
      'scroll',
      'take_snapshot',
    ])
    expect(chatTools.tabs).toBeUndefined()
  })

  it('allows chat mode to list tabs without allowing tab mutation', async () => {
    expect(CHAT_MODE_ALLOWED_TOOLS.has('tabs')).toBe(true)
    const calls: string[] = []
    const session = {
      pages: {
        list: async () => [
          { pageId: 1, url: 'https://example.com', title: 'Example' },
        ],
        newPage: async () => {
          calls.push('newPage')
          return 2
        },
      },
    } as unknown as BrowserSession
    const tools = buildBrowserToolSet(session, { readOnly: true })

    const listResult = await tools.tabs.execute?.({ action: 'list' }, {
      abortSignal: new AbortController().signal,
    } as never)
    const newResult = await tools.tabs.execute?.(
      { action: 'new', url: 'https://example.com' },
      { abortSignal: new AbortController().signal } as never,
    )

    expect(listResult).toMatchObject({ isError: false })
    expect(newResult).toMatchObject({ isError: true })
    expect(calls).toEqual([])
  })

  it('propagates AI SDK abort signals into browser tools', async () => {
    const session = { pages: {} } as unknown as BrowserSession
    const tools = buildBrowserToolSet(session)
    const controller = new AbortController()
    controller.abort(new Error('cancelled'))

    let caught: unknown
    try {
      await tools.wait.execute?.({ page: 1, for: 'time', value: '1000' }, {
        abortSignal: controller.signal,
      } as never)
    } catch (error) {
      caught = error
    }

    expect(caught).toBeInstanceOf(Error)
    expect((caught as Error).message).toBe('cancelled')
  })

  it('stops awaiting in-flight browser tools when aborted', async () => {
    const slowTool = defineTool({
      name: 'slow',
      description: 'Slow test tool.',
      input: z.object({}),
      handler: async () => {
        await new Promise(() => {})
        return textResult('done')
      },
    })
    const controller = new AbortController()

    const pending = executeTool(
      slowTool,
      {},
      {
        session: {} as BrowserSession,
        signal: controller.signal,
      },
    )
    controller.abort(new Error('cancelled'))

    await expect(pending).rejects.toThrow('cancelled')
  })
})
