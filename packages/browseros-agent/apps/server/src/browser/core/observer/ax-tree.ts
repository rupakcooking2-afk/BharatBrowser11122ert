import type { ProtocolApi } from '@browseros/cdp-protocol/protocol-api'
import type { AXNode } from '../snapshot/ax-types'

/** Fetches a session's full accessibility tree (main frame). Frame stitching layers on later. */
export async function fetchAxTree(
  session: ProtocolApi,
  params: { frameId?: string } = {},
): Promise<AXNode[]> {
  const result = await session.Accessibility.getFullAXTree(params)
  return (result.nodes as AXNode[] | undefined) ?? []
}

export async function fetchLegacyAxTreeWithFrames(
  session: ProtocolApi,
): Promise<AXNode[]> {
  const frameIds = await getFrameIds(session)

  if (frameIds.length <= 1) return fetchAxTree(session)

  const allNodes: AXNode[] = []
  for (const frameId of frameIds) {
    try {
      const nodes = await fetchAxTree(session, { frameId })
      for (const node of nodes) {
        allNodes.push({
          ...node,
          nodeId: `${frameId}:${node.nodeId}`,
          childIds: node.childIds?.map((id) => `${frameId}:${id}`),
        })
      }
    } catch {
      // Cross-origin or detached frames may fail; current legacy snapshots skip them.
    }
  }
  return allNodes
}

async function getFrameIds(session: ProtocolApi): Promise<string[]> {
  try {
    const result = await session.Page.getFrameTree()
    const ids: string[] = []
    type FrameTree = { frame: { id: string }; childFrames?: FrameTree[] }
    function collect(tree: FrameTree): void {
      ids.push(tree.frame.id)
      for (const child of tree.childFrames ?? []) collect(child)
    }
    collect(result.frameTree as FrameTree)
    return ids
  } catch {
    return []
  }
}
