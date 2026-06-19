/**
 * @license
 * Copyright 2025 BrowserOS
 */

import { describe, expect, it } from 'bun:test'
import { AdapterHealthChecker } from '../../../src/lib/agents/adapters/health'

describe('AdapterHealthChecker', () => {
  it('reports Hermes through host adapter detection', async () => {
    const health = await new AdapterHealthChecker({
      detectHostAdapter: async (adapter) => {
        expect(adapter).toBe('hermes')
        return {
          healthy: true,
          checkedAt: 1234,
          readiness: 'ready',
          installState: 'installed',
          nativeCliState: 'present',
          authState: 'not-applicable',
          version: 'hermes 1.0.0',
          adapterLaunchSource: 'host-cli',
          packageCacheState: 'unknown',
        }
      },
    }).getHealth('hermes')

    expect(health).toMatchObject({
      healthy: true,
      checkedAt: 1234,
      readiness: 'ready',
      installState: 'installed',
      authState: 'not-applicable',
      adapterLaunchSource: 'host-cli',
    })
  })
})
