import { randomUUID } from 'node:crypto'
import { join } from 'node:path'
import { getToolOutputDir, writeToolOutputFile } from '../../lib/browseros-dir'

function sanitizeSegment(value: string): string {
  const sanitized = value.replace(/[^a-z0-9_-]+/gi, '-').replace(/^-+|-+$/g, '')
  return sanitized || 'browser-tool-output'
}

export async function writeTempToolOutputFile(args: {
  toolName: string
  extension: string
  content: string
}): Promise<string> {
  const outputDir = await getToolOutputDir()
  const toolName = sanitizeSegment(args.toolName)
  const extension = sanitizeSegment(args.extension) || 'txt'
  const filePath = join(
    outputDir,
    `${toolName}-${Date.now()}-${randomUUID()}.${extension}`,
  )

  await writeToolOutputFile(filePath, args.content)
  return filePath
}
