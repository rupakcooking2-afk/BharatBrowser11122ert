import { beforeEach, describe, expect, it, mock } from 'bun:test'
import type {
  KlavisProxyHandle,
  KlavisProxyRef,
} from '../../../src/api/services/klavis/strata-proxy'

interface McpServerCreation {
  handleAtCreate: KlavisProxyHandle | null
}

const serverCreations: McpServerCreation[] = []
const transportInstances: FakeTransport[] = []
const connectCalls: FakeTransport[] = []

class FakeTransport {
  constructor(readonly options: unknown) {
    transportInstances.push(this)
  }

  handleRequest = mock(async () => Response.json({ ok: true }))
}

const createMcpServerSpy = mock((deps: { klavisRef?: KlavisProxyRef }) => {
  serverCreations.push({
    handleAtCreate: deps.klavisRef?.handle ?? null,
  })

  return {
    connect: mock(async (transport: FakeTransport) => {
      connectCalls.push(transport)
    }),
  }
})

mock.module('@hono/mcp', () => ({
  StreamableHTTPTransport: FakeTransport,
}))

mock.module('../../../src/api/services/mcp/mcp-server', () => ({
  createMcpServer: createMcpServerSpy,
}))

const { createMcpRoutes } = await import('../../../src/api/routes/mcp')

beforeEach(() => {
  serverCreations.length = 0
  transportInstances.length = 0
  connectCalls.length = 0
})

function createConnectedHandle(): KlavisProxyHandle {
  return {
    browserosId: 'browseros-user-1',
    tools: [],
    inputSchemas: new Map(),
    callTool: mock(async () => ({ content: [] })),
    close: mock(async () => {}),
  }
}

async function postMcp(app: ReturnType<typeof createMcpRoutes>) {
  return app.request('/', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      jsonrpc: '2.0',
      id: 1,
      method: 'tools/list',
    }),
  })
}

describe('createMcpRoutes', () => {
  it('uses the latest Klavis handle when building each MCP request server', async () => {
    const klavisRef: KlavisProxyRef = { handle: null }
    const app = createMcpRoutes({
      version: '0.0.0-test',
      browser: {} as never,
      browserSession: {} as never,
      klavisRef,
      browserUseNewTools: true,
    })

    const first = await postMcp(app)

    const connectedHandle = createConnectedHandle()
    klavisRef.handle = connectedHandle

    const second = await postMcp(app)

    expect(first.status).toBe(200)
    expect(second.status).toBe(200)
    expect(serverCreations.map((creation) => creation.handleAtCreate)).toEqual([
      null,
      connectedHandle,
    ])
    expect(transportInstances).toHaveLength(2)
    expect(connectCalls).toEqual(transportInstances)
  })
})
