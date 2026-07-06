import { Heart } from 'lucide-react'
import type { FC } from 'react'

export const CreditsPage: FC = () => {
  return (
    <div className="fade-in slide-in-from-bottom-5 animate-in space-y-6 duration-500">
      <div className="rounded-xl border border-border bg-card p-6 shadow-sm transition-all hover:shadow-md">
        <div className="flex items-start gap-4">
          <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl bg-[var(--accent-orange)]/10">
            <Heart className="h-6 w-6 text-[var(--accent-orange)]" />
          </div>
          <div className="flex-1">
            <h2 className="mb-1 font-semibold text-xl">Credits</h2>
            <p className="text-muted-foreground text-sm">
              People behind Bharat Browser
            </p>
          </div>
        </div>
      </div>

      <div className="rounded-xl border border-border/60 bg-gradient-to-br from-card via-card/95 to-card/90 p-6 shadow-sm backdrop-blur-sm transition-all hover:shadow-md">
        <div className="flex flex-col items-center gap-4 py-6 text-center">
          <div className="flex h-16 w-16 items-center justify-center rounded-2xl bg-[var(--accent-orange)]/10">
            <Heart className="h-8 w-8 text-[var(--accent-orange)]" />
          </div>
          <div>
            <p className="mb-1 font-semibold text-lg">Bharat Browser</p>
            <p className="text-muted-foreground text-sm">
              Created by Lakshy Kumar
            </p>
            <p className="text-muted-foreground text-sm">Class 8A</p>
            <p className="text-muted-foreground text-sm">
              PM SHRI Kendriya Vidyalaya No. 2 Kota
            </p>
          </div>
        </div>
      </div>

      <div className="rounded-xl border border-border/60 bg-gradient-to-br from-card via-card/95 to-card/90 p-6 shadow-sm backdrop-blur-sm transition-all hover:shadow-md">
        <p className="text-center text-muted-foreground text-sm italic">
          Thank you for using Bharat Browser.
        </p>
      </div>
    </div>
  )
}
