import { z } from 'zod'
import { defineTool, textResult } from './framework'
import { writeTempToolOutputFile } from './output-file'
import { wrapUntrusted } from './trust-boundary'

const LARGE_SNAPSHOT_WORD_THRESHOLD = 5_000
const LARGE_SNAPSHOT_CHAR_THRESHOLD = 50_000

function countWords(text: string): number {
  const trimmed = text.trim()
  return trimmed ? trimmed.split(/\s+/).length : 0
}

export const snapshot = defineTool({
  name: 'snapshot',
  description:
    'Capture the page as an indented accessibility tree. Each actionable element carries a stable [ref=eN] you pass to `act`. Iframe content is stitched in inline. Re-snapshot after navigation or large changes (refs are invalidated). This is the start of the loop: snapshot -> act -> (reads back a diff).',
  input: z.object({
    page: z.number().int().describe('Page id from `tabs` or `navigate`.'),
  }),
  annotations: { readOnlyHint: true },
  handler: async (args, ctx) => {
    const { text } = await ctx.session.observe(args.page).snapshot()
    const origin = ctx.session.pages.getInfo(args.page)?.url ?? 'unknown'
    const snapshotText = text || '(empty page)'
    const wordCount = countWords(text)
    const wrappedSnapshot = wrapUntrusted(snapshotText, origin)
    const contentLength = wrappedSnapshot.length

    if (
      wordCount > LARGE_SNAPSHOT_WORD_THRESHOLD ||
      snapshotText.length > LARGE_SNAPSHOT_CHAR_THRESHOLD
    ) {
      const path = await writeTempToolOutputFile({
        toolName: 'snapshot',
        extension: 'md',
        content: wrappedSnapshot,
      })

      return textResult(
        [
          `Large snapshot (${wordCount} words, ${contentLength} chars) saved to: ${path}`,
          'Read the file for the full snapshot and refs.',
        ].join('\n'),
        {
          page: args.page,
          path,
          contentLength,
          wordCount,
          writtenToFile: true,
        },
      )
    }

    return textResult(wrappedSnapshot, {
      page: args.page,
    })
  },
})
