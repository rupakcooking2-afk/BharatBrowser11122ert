import { z } from 'zod'
import { defineTool, textResult } from './framework'
import { wrapUntrusted } from './trust-boundary'

export const diff = defineTool({
  name: 'diff',
  description:
    "Show what changed on the page since the last snapshot/diff - a cheap way to see an action's effect without re-dumping the whole tree.",
  input: z.object({ page: z.number().int() }),
  annotations: { readOnlyHint: true },
  handler: async (args, ctx) => {
    const d = await ctx.session.observe(args.page).diff()
    if (!d.changed) return textResult('no change since last snapshot')
    const origin =
      d.afterUrl ?? ctx.session.pages.getInfo(args.page)?.url ?? 'unknown'
    if (d.urlChanged) {
      return textResult(
        `URL changed; returning full current snapshot instead of a diff:\n${wrapUntrusted(d.text || '(empty page)', origin)}`,
        {
          added: d.added,
          removed: d.removed,
          urlChanged: true,
          beforeUrl: d.beforeUrl,
          afterUrl: d.afterUrl,
        },
      )
    }
    return textResult(wrapUntrusted(d.text, origin), {
      added: d.added,
      removed: d.removed,
    })
  },
})
