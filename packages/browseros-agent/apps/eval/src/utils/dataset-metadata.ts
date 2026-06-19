import type { TaskDatasetMetadata, TaskInputMetadata } from '../types'

/** Extract dataset-derived metadata (website/difficulty/challenge_type/similar_to)
 * that we want to surface in the viewer. AGI SDK puts these under
 * `metadata.additional`; the website also lives at `metadata.website`. */
export function extractDatasetMetadata(
  input: TaskInputMetadata | undefined,
): TaskDatasetMetadata | undefined {
  if (!input) return undefined

  const additional = input.additional ?? {}
  const result: TaskDatasetMetadata = {}

  const website = pickString(input.website, additional.website)
  if (website) result.website = website

  const difficulty = pickString(additional.difficulty)
  if (difficulty) result.difficulty = difficulty

  const challengeType = pickString(additional.challenge_type)
  if (challengeType) result.challenge_type = challengeType

  const similarTo = pickString(additional.similar_to)
  if (similarTo) result.similar_to = similarTo

  return Object.keys(result).length > 0 ? result : undefined
}

function pickString(...candidates: unknown[]): string | undefined {
  for (const value of candidates) {
    if (typeof value === 'string' && value.length > 0) return value
  }
  return undefined
}
