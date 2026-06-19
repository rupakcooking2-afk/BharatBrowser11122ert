export type FrameId = string

export interface RefEntry {
  ref: string
  backendNodeId: number
  role: string
  name: string
  /** Occurrence index of (frameId, role, name) — disambiguates duplicates on re-resolution. */
  nth: number
  /** undefined ⇒ main frame. */
  frameId: FrameId | undefined
}

/**
 * Allocates stable `eN` handles for actionable nodes within one snapshot. All frames of a
 * page share a single RefMap, so refs are globally unique and the agent never sees frame ids.
 *
 * Each entry records (role, name, nth) scoped to its frame so a ref survives DOM churn: when the
 * cached backendNodeId goes stale, the resolver re-queries that frame's AX tree by role+name+nth.
 */
export class RefMap {
  readonly byRef = new Map<string, RefEntry>()
  private nextRefNum = 1
  private readonly nthCounter = new Map<string, number>()

  mint(node: {
    backendNodeId: number
    role: string
    name: string
    frameId?: FrameId
  }): string {
    const key = `${node.frameId ?? ''}\u0000${node.role}\u0000${node.name}`
    const nth = this.nthCounter.get(key) ?? 0
    this.nthCounter.set(key, nth + 1)

    const ref = `e${this.nextRefNum++}`
    this.byRef.set(ref, {
      ref,
      backendNodeId: node.backendNodeId,
      role: node.role,
      name: node.name,
      nth,
      frameId: node.frameId,
    })
    return ref
  }

  get(ref: string): RefEntry | undefined {
    return this.byRef.get(ref)
  }

  get size(): number {
    return this.byRef.size
  }
}
