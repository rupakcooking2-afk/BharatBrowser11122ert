import type { ProtocolApi } from '@browseros/cdp-protocol/protocol-api'
import { findCursorHits } from '../observer/cursor-augment'
import type { AXNode } from './ax-types'
import {
  INTERACTIVE_ROLES,
  NAMED_CONTENT_ROLES,
  ROOT_ROLES,
  SKIP_ROLES,
} from './roles'

export type { AXNode } from './ax-types'

export function buildEnhancedTree(nodes: AXNode[]): string[] {
  const nodeMap = new Map<string, AXNode>()
  for (const node of nodes) nodeMap.set(node.nodeId, node)

  const lines: string[] = []

  // biome-ignore lint/complexity/noExcessiveCognitiveComplexity: tree-walking with multiple node types is inherently complex
  function walk(nodeId: string, depth: number): void {
    const node = nodeMap.get(nodeId)
    if (!node) return

    const role = node.ignored
      ? undefined
      : (node.role?.value as string | undefined)
    if (!role || SKIP_ROLES.has(role)) {
      if (node.childIds)
        for (const childId of node.childIds) walk(childId, depth)
      return
    }

    const name = typeof node.name?.value === 'string' ? node.name.value : ''
    const value = typeof node.value?.value === 'string' ? node.value.value : ''
    const isInteractive = INTERACTIVE_ROLES.has(role)
    const isNamedContent = NAMED_CONTENT_ROLES.has(role) && name.length > 0
    const hasId =
      (isInteractive || isNamedContent) && node.backendDOMNodeId !== undefined

    const indent = '  '.repeat(depth)
    let line: string

    if (hasId) {
      line = `${indent}[${node.backendDOMNodeId}] ${role}`
    } else {
      line = `${indent}- ${role}`
    }

    if (name) line += ` "${name}"`
    if (
      value &&
      (role === 'textbox' || role === 'searchbox' || role === 'textarea')
    )
      line += ` value="${value}"`
    const props = extractProps(node)
    if (props) line += ` ${props}`

    lines.push(line)

    if (node.childIds)
      for (const childId of node.childIds) walk(childId, depth + 1)
  }

  const roots = nodes.filter((n) => ROOT_ROLES.has(roleOf(n)))
  if (roots.length === 0 && nodes[0]?.childIds) {
    for (const childId of nodes[0].childIds) walk(childId, 0)
  } else {
    for (const root of roots) {
      if (root.childIds) for (const childId of root.childIds) walk(childId, 0)
    }
  }

  return lines
}

export interface CursorInteractiveElement {
  backendNodeId: number
  text: string
  reasons: string[]
}

export async function findCursorInteractiveElements(
  session: ProtocolApi,
): Promise<CursorInteractiveElement[]> {
  const hits = await findCursorHits(session)
  const results: CursorInteractiveElement[] = []

  for (const [backendNodeId, reasons] of hits) {
    const text = await getNodeText(session, backendNodeId)
    if (text) results.push({ backendNodeId, text, reasons })
  }

  return results
}

async function getNodeText(
  session: ProtocolApi,
  backendNodeId: number,
): Promise<string> {
  try {
    const resolved = await session.DOM.resolveNode({ backendNodeId })
    const objectId = resolved.object?.objectId
    if (!objectId) return ''

    const result = await session.Runtime.callFunctionOn({
      objectId,
      functionDeclaration:
        'function(){return ((this.textContent||"").trim().slice(0,100)||(this.getAttribute("aria-label")||"").trim());}',
      returnByValue: true,
    })

    return typeof result.result?.value === 'string' ? result.result.value : ''
  } catch {
    return ''
  }
}

export interface LinkNode {
  backendDOMNodeId: number
  text: string
}

export function extractLinkNodes(nodes: AXNode[]): LinkNode[] {
  const nodeMap = new Map<string, AXNode>()
  for (const node of nodes) nodeMap.set(node.nodeId, node)

  const links: LinkNode[] = []

  function walk(nodeId: string): void {
    const node = nodeMap.get(nodeId)
    if (!node) return

    const role = node.ignored
      ? undefined
      : (node.role?.value as string | undefined)

    if (role === 'link' && node.backendDOMNodeId !== undefined) {
      const text = typeof node.name?.value === 'string' ? node.name.value : ''
      links.push({ backendDOMNodeId: node.backendDOMNodeId, text })
    }

    if (node.childIds) for (const childId of node.childIds) walk(childId)
  }

  const roots = nodes.filter((n) => ROOT_ROLES.has(roleOf(n)))
  if (roots.length === 0 && nodes[0]?.childIds) {
    for (const childId of nodes[0].childIds) walk(childId)
  } else {
    for (const root of roots) {
      if (root.childIds) for (const childId of root.childIds) walk(childId)
    }
  }

  return links
}

function extractProps(node: AXNode): string {
  const parts: string[] = []
  if (!node.properties) return ''

  for (const prop of node.properties) {
    if (prop.name === 'checked' && prop.value.value === true)
      parts.push('checked')
    if (prop.name === 'checked' && prop.value.value === 'mixed')
      parts.push('indeterminate')
    if (prop.name === 'disabled' && prop.value.value === true)
      parts.push('disabled')
    if (prop.name === 'expanded' && prop.value.value === true)
      parts.push('expanded')
    if (prop.name === 'expanded' && prop.value.value === false)
      parts.push('collapsed')
    if (prop.name === 'required' && prop.value.value === true)
      parts.push('required')
    if (prop.name === 'selected' && prop.value.value === true)
      parts.push('selected')
    if (prop.name === 'level') parts.push(`level=${prop.value.value}`)
  }

  return parts.length > 0 ? `(${parts.join(', ')})` : ''
}

function roleOf(node: AXNode): string {
  return typeof node.role?.value === 'string' ? node.role.value : ''
}
