import { Globe, Sparkles } from 'lucide-react'
import type { FC } from 'react'

export const AboutPage: FC = () => {
  return (
    <div className="fade-in slide-in-from-bottom-5 animate-in space-y-6 duration-500">
      <div className="rounded-xl border border-border bg-card p-6 shadow-sm transition-all hover:shadow-md">
        <div className="flex items-start gap-4">
          <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl bg-[var(--accent-orange)]/10">
            <Globe className="h-6 w-6 text-[var(--accent-orange)]" />
          </div>
          <div className="flex-1">
            <h2 className="mb-1 font-semibold text-xl">About Bharat Browser</h2>
            <p className="text-muted-foreground text-sm">
              An intelligent, privacy-focused, AI-powered browser
            </p>
          </div>
        </div>
      </div>

      <div className="rounded-xl border border-border/60 bg-gradient-to-br from-card via-card/95 to-card/90 p-6 shadow-sm backdrop-blur-sm transition-all hover:shadow-md">
        <div className="mb-6 flex items-center gap-3">
          <Sparkles className="h-5 w-5 text-[var(--accent-orange)]" />
          <h3 className="font-semibold text-base">Creator</h3>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="rounded-lg bg-muted/30 p-4">
            <p className="mb-1 font-medium text-muted-foreground text-xs tracking-wide uppercase">Creator Name</p>
            <p className="font-semibold text-sm">Lakshy Kumar</p>
          </div>
          <div className="rounded-lg bg-muted/30 p-4">
            <p className="mb-1 font-medium text-muted-foreground text-xs tracking-wide uppercase">Class</p>
            <p className="font-semibold text-sm">8A</p>
          </div>
          <div className="rounded-lg bg-muted/30 p-4 sm:col-span-2">
            <p className="mb-1 font-medium text-muted-foreground text-xs tracking-wide uppercase">School</p>
            <p className="font-semibold text-sm">PM SHRI Kendriya Vidyalaya No. 2 Kota</p>
          </div>
          <div className="rounded-lg bg-muted/30 p-4 sm:col-span-2">
            <p className="mb-1 font-medium text-muted-foreground text-xs tracking-wide uppercase">Role</p>
            <p className="font-semibold text-sm">Creator & Developer of Bharat Browser</p>
          </div>
        </div>
      </div>

      <div className="rounded-xl border border-border/60 bg-gradient-to-br from-card via-card/95 to-card/90 p-6 shadow-sm backdrop-blur-sm transition-all hover:shadow-md">
        <h3 className="mb-3 font-semibold text-base">About</h3>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Bharat Browser was created to provide an intelligent, privacy-focused,
          AI-powered browsing experience. It combines modern browsing with
          advanced automation, productivity features, and AI assistance.
        </p>
      </div>
    </div>
  )
}
