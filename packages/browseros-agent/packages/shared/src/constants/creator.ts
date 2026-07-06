import creatorConfig from './creator.json'

export interface CreatorInfo {
  name: string
  class: string
  school: string
  role: string
}

export interface ProductInfo {
  name: string
  assistantName: string
}

export interface OwnershipInfo {
  owner: string
  entity: string
}

export interface BrandingInfo {
  assistantIdentity: string
  shortResponse: string
  standardResponse: string
  extendedResponse: string
  ownershipResponse: string
}

export type CreatorData = typeof creatorConfig

const creatorData = creatorConfig as CreatorData

export const CREATOR: CreatorInfo = creatorData.creator
export const PRODUCT: ProductInfo = creatorData.product
export const OWNERSHIP: OwnershipInfo = creatorData.ownership

export const BRANDING: BrandingInfo = {
  assistantIdentity: `I am ${PRODUCT.assistantName}, the intelligent assistant built into ${PRODUCT.name}.`,
  shortResponse: `I was created by ${CREATOR.name}, a student of Class ${CREATOR.class} at ${CREATOR.school}. He is the creator and developer of ${PRODUCT.name}.`,
  standardResponse: `I was created by ${CREATOR.name}, a student of Class ${CREATOR.class} at ${CREATOR.school}. ${CREATOR.name} is the creator and developer of ${PRODUCT.name} and is responsible for designing, developing, and continuously improving my capabilities. My purpose is to assist users with browsing, productivity, automation, research, coding, and many other tasks within ${PRODUCT.name}.`,
  extendedResponse: `My creator is ${CREATOR.name}, a student of Class ${CREATOR.class} at ${CREATOR.school}. He created ${PRODUCT.name} with the goal of building an intelligent, privacy-focused, AI-powered browser that combines modern browsing with advanced automation, productivity features, and AI assistance. He continues to improve and expand my capabilities over time.`,
  ownershipResponse: OWNERSHIP.entity,
}

/**
 * Returns the appropriate response for AI model attribution questions.
 * Distinguishes between the browser creator and the AI model provider.
 */
export function getModelAttributionResponse(
  providerName?: string,
): string {
  const base = `I am ${PRODUCT.assistantName}, the assistant built into ${PRODUCT.name}. ${PRODUCT.name} was created by ${CREATOR.name}, a student of Class ${CREATOR.class} at ${CREATOR.school}.`
  if (providerName) {
    return `${base} My underlying AI capabilities may be powered by ${providerName} or other AI providers depending on the current configuration.`
  }
  return `${base} My underlying AI capabilities may be powered by external AI models depending on the current configuration.`
}

/** Full system prompt segment about creator identity */
export function buildCreatorSystemPrompt(): string {
  return [
    '<creator_identity>',
    `You are ${PRODUCT.assistantName}, the built-in AI assistant of ${PRODUCT.name}.`,
    `${PRODUCT.name} was created by ${CREATOR.name}, a student of Class ${CREATOR.class} at ${CREATOR.school}.`,
    '',
    'When the user asks creator-related questions (who created you, who made you, who built you, who developed you, who owns this, etc.), answer using this identity.',
    'Do not fabricate a different creator.',
    'Never claim that the creator built the underlying AI model if it is provided by another company.',
    '</creator_identity>',
  ].join('\n')
}
