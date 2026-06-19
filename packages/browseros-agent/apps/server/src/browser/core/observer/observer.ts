import type { ProtocolApi } from '@browseros/cdp-protocol/protocol-api'
import type { FrameId } from '../connection'
import type { PageManager } from '../pages'
import { diffSnapshotObservations, type SnapshotDiff } from '../snapshot/diff'
import { RefMap } from '../snapshot/refs'
import { renderSnapshot } from '../snapshot/render'
import { fetchAxTree } from './ax-tree'
import { findCursorHits } from './cursor-augment'
import type { FrameRegistry } from './frames'
import { type ResolvedElement, resolveRefEntry } from './resolve'

const MAX_FRAME_DEPTH = 5
const MAX_STABLE_CAPTURE_ATTEMPTS = 3

export interface SnapshotResult {
  text: string
  refs: RefMap
  url: string
}

/** Per-page snapshot, ref, and diff state for one BrowserOS page id. */
export class Observer {
  private baseline?: { text: string; url: string }
  private refs = new RefMap()

  constructor(
    private readonly pages: PageManager,
    private readonly frames: FrameRegistry,
    private readonly pageId: number,
  ) {}

  async snapshot(): Promise<SnapshotResult> {
    const result = await this.capture()
    this.commit(result)
    return result
  }

  async diff(): Promise<SnapshotDiff> {
    const before = this.baseline
    const result = await this.capture()
    this.commit(result)
    return diffSnapshotObservations(before, result)
  }

  get lastRefs(): RefMap {
    return this.refs
  }

  /** Resolve a ref from the last snapshot to a live element, routed to its frame's session. */
  async resolveRef(ref: string): Promise<ResolvedElement> {
    const entry = this.refs.get(ref)
    if (!entry) {
      throw new Error(`Unknown ref ${ref}; take a new snapshot.`)
    }
    await this.pages.getSession(this.pageId)
    const { session, axParams } = this.frames.resolveFrameTarget(
      this.pageId,
      entry.frameId,
    )
    return resolveRefEntry(session, entry, axParams)
  }

  private async capture(): Promise<SnapshotResult> {
    const pageSession = await this.pages.getSession(this.pageId)
    for (let attempt = 0; attempt < MAX_STABLE_CAPTURE_ATTEMPTS; attempt++) {
      const beforeUrl = await this.readCurrentUrl(pageSession.session)
      const refs = new RefMap()
      const text = await this.captureFrame(undefined, refs, 0, new Set())
      const afterUrl = await this.readCurrentUrl(pageSession.session)
      if (!knownUrlsDiffer(beforeUrl, afterUrl))
        return { text, refs, url: afterUrl }
    }
    throw new Error('Page URL changed during snapshot capture; retry.')
  }

  /** Render a frame, then splice each child iframe's rendered tree under its `- iframe` line. */
  private async captureFrame(
    frameId: FrameId | undefined,
    refs: RefMap,
    baseDepth: number,
    visited: Set<FrameId>,
  ): Promise<string> {
    if (frameId !== undefined) {
      if (visited.has(frameId)) return ''
      visited.add(frameId)
    }

    const { session, axParams } = this.frames.resolveFrameTarget(
      this.pageId,
      frameId,
    )
    const nodes = await fetchAxTree(session, axParams)
    const cursorHits = await findCursorHits(session).catch(
      () => new Map<number, string[]>(),
    )
    const { text, iframes } = renderSnapshot(nodes, {
      refs,
      frameId,
      baseDepth,
      cursorHits,
    })
    if (iframes.length === 0 || baseDepth >= MAX_FRAME_DEPTH) return text

    const lines = text.split('\n')
    // Splice bottom-up so earlier line indices stay valid as we insert.
    for (const stitch of [...iframes].reverse()) {
      const childFrameId = await resolveChildFrameId(
        session,
        stitch.backendNodeId,
      )
      if (!childFrameId) continue
      const childText = await this.captureFrame(
        childFrameId,
        refs,
        stitch.depth + 1,
        visited,
      ).catch(() => '')
      if (childText) lines.splice(stitch.lineIndex + 1, 0, childText)
    }
    return lines.join('\n')
  }

  private commit(result: SnapshotResult): void {
    this.baseline = { text: result.text, url: result.url }
    this.refs = result.refs
  }

  private async readCurrentUrl(session: ProtocolApi): Promise<string> {
    return readCurrentUrl(session, async () => {
      const refreshed = await this.pages.refresh(this.pageId)
      return refreshed?.url
    })
  }
}

/** Reads the live main-frame URL, falling back to the tab registry during teardown. */
async function readCurrentUrl(
  session: ProtocolApi,
  fallback: () => Promise<string | undefined>,
): Promise<string> {
  try {
    const result = await session.Page.getFrameTree()
    const frame = result.frameTree.frame
    if (frame.url) return `${frame.url}${frame.urlFragment ?? ''}`
  } catch {}
  try {
    return (await fallback()) || 'unknown'
  } catch {
    return 'unknown'
  }
}

function knownUrlsDiffer(beforeUrl: string, afterUrl: string): boolean {
  return (
    beforeUrl !== 'unknown' && afterUrl !== 'unknown' && beforeUrl !== afterUrl
  )
}

/** Resolve an iframe element to the frameId of its embedded document. */
async function resolveChildFrameId(
  session: ProtocolApi,
  backendNodeId: number,
): Promise<FrameId | undefined> {
  try {
    const described = await session.DOM.describeNode({
      backendNodeId,
      depth: 1,
    })
    const node = described.node as {
      contentDocument?: { frameId?: string }
      frameId?: string
    }
    return node.contentDocument?.frameId ?? node.frameId
  } catch {
    return undefined
  }
}
