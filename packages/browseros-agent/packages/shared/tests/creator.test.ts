import { describe, expect, it } from 'bun:test'
import {
  CREATOR,
  PRODUCT,
  OWNERSHIP,
  BRANDING,
  buildCreatorSystemPrompt,
  getModelAttributionResponse,
} from '../src/constants/creator'

describe('Creator Identity Module', () => {
  describe('CREATOR', () => {
    it('has correct creator name', () => {
      expect(CREATOR.name).toBe('Lakshy Kumar')
    })

    it('has correct class', () => {
      expect(CREATOR.class).toBe('8A')
    })

    it('has correct school', () => {
      expect(CREATOR.school).toBe('PM SHRI Kendriya Vidyalaya No. 2 Kota')
    })

    it('has correct role', () => {
      expect(CREATOR.role).toContain('Creator and Developer')
      expect(CREATOR.role).toContain('Bharat Browser')
    })
  })

  describe('PRODUCT', () => {
    it('has correct product name', () => {
      expect(PRODUCT.name).toBe('Bharat Browser')
    })

    it('has correct assistant name', () => {
      expect(PRODUCT.assistantName).toBe('Bharat AI')
    })
  })

  describe('OWNERSHIP', () => {
    it('has correct owner', () => {
      expect(OWNERSHIP.owner).toBe('Lakshy Kumar')
    })

    it('includes creator details in entity string', () => {
      expect(OWNERSHIP.entity).toContain('Lakshy Kumar')
      expect(OWNERSHIP.entity).toContain('Class 8A')
      expect(OWNERSHIP.entity).toContain('PM SHRI Kendriya Vidyalaya No. 2 Kota')
    })
  })

  describe('BRANDING', () => {
    it('assistantIdentity contains assistant name', () => {
      expect(BRANDING.assistantIdentity).toContain('Bharat AI')
      expect(BRANDING.assistantIdentity).toContain('Bharat Browser')
    })

    it('shortResponse contains creator name and school', () => {
      expect(BRANDING.shortResponse).toContain('Lakshy Kumar')
      expect(BRANDING.shortResponse).toContain('PM SHRI Kendriya Vidyalaya')
    })

    it('standardResponse is more detailed than shortResponse', () => {
      expect(BRANDING.standardResponse.length).toBeGreaterThan(
        BRANDING.shortResponse.length,
      )
      expect(BRANDING.standardResponse).toContain('continuously improving')
    })

    it('extendedResponse contains goal information', () => {
      expect(BRANDING.extendedResponse).toContain('goal')
      expect(BRANDING.extendedResponse).toContain('privacy-focused')
    })

    it('ownershipResponse matches ownership entity', () => {
      expect(BRANDING.ownershipResponse).toBe(OWNERSHIP.entity)
    })
  })

  describe('buildCreatorSystemPrompt', () => {
    it('returns a non-empty string wrapped in creator_identity tag', () => {
      const prompt = buildCreatorSystemPrompt()
      expect(prompt.length).toBeGreaterThan(0)
      expect(prompt).toContain('<creator_identity>')
      expect(prompt).toContain('</creator_identity>')
    })

    it('contains assistant name Bharat AI', () => {
      const prompt = buildCreatorSystemPrompt()
      expect(prompt).toContain('Bharat AI')
      expect(prompt).toContain('Bharat Browser')
    })

    it('contains creator name and school', () => {
      const prompt = buildCreatorSystemPrompt()
      expect(prompt).toContain('Lakshy Kumar')
      expect(prompt).toContain('PM SHRI Kendriya Vidyalaya No. 2 Kota')
    })

    it('instructs AI to use identity for creator questions', () => {
      const prompt = buildCreatorSystemPrompt()
      expect(prompt).toContain('creator-related questions')
      expect(prompt).toContain('who created you')
    })

    it('distinguishes browser creator from AI model provider', () => {
      const prompt = buildCreatorSystemPrompt()
      expect(prompt).toContain('Do not fabricate')
      expect(prompt).toContain('underlying AI model')
    })
  })

  describe('getModelAttributionResponse', () => {
    it('returns base response without provider', () => {
      const response = getModelAttributionResponse()
      expect(response).toContain('Bharat AI')
      expect(response).toContain('Bharat Browser')
      expect(response).toContain('Lakshy Kumar')
      expect(response).toContain('external AI models')
    })

    it('includes provider name when provided', () => {
      const response = getModelAttributionResponse('OpenAI')
      expect(response).toContain('OpenAI')
      expect(response).toContain('Lakshy Kumar')
    })
  })
})
