/**
 * @license
 * Copyright 2025 BrowserOS
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

import type {
  PrepareAcpxAgentContextInput,
  PreparedAcpxAgentContext,
} from '../acpx/agent-adapter'
import {
  finishBrowserosManagedContext,
  prepareBrowserosManagedContext,
} from '../acpx/agent-common'
import { ensureHermesAgentHomeHostDir } from './hermes-paths'

/** Prepares Hermes as a host process with a per-agent HERMES_HOME. */
export async function prepareHermesContext(
  input: PrepareAcpxAgentContextInput,
): Promise<PreparedAcpxAgentContext> {
  const common = await prepareBrowserosManagedContext(input)
  const hermesAgentHome = await ensureHermesAgentHomeHostDir({
    browserosDir: input.browserosDir,
    agentId: input.agent.id,
  })

  return finishBrowserosManagedContext({
    ...common,
    commandEnv: {
      HERMES_HOME: hermesAgentHome,
    },
  })
}
