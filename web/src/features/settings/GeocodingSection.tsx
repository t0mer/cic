import { ApiError } from '@/lib/api'
import { commandErrorMessage } from '@/lib/errors'
import { useUiConfig } from '@/features/config/queries'
import { useNow } from '@/lib/now'
import { useGeocodeUsage } from './queries'
import type { GeocodeUsage } from '@/lib/types'

const pct = (n: number) => `${Math.round(n * 100)}%`

function Notice({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed p-4" data-testid="geocode-state">
      <p className="font-medium">{title}</p>
      <p className="mt-1 text-sm text-muted-foreground">{children}</p>
    </div>
  )
}

function Usage({ u }: { u: GeocodeUsage }) {
  const now = useNow()
  const cap = Math.max(0, u.daily_cap)
  const used = Math.max(0, u.calls_today)
  const ratio = cap > 0 ? Math.min(1, used / cap) : 0
  const blocked =
    u.blocked_until && Date.parse(u.blocked_until) / 1000 > now ? u.blocked_until : null
  return (
    <div className="flex flex-col gap-4">
      {blocked ? (
        <div
          role="alert"
          className="rounded-md border border-amber-500/60 bg-amber-50 p-3 text-sm text-amber-950 dark:bg-amber-950/40 dark:text-amber-100"
        >
          Lookups are paused until {new Date(blocked).toLocaleString()}.
        </div>
      ) : null}
      <div className="flex flex-col gap-2 rounded-lg border p-4">
        <div className="flex items-baseline justify-between gap-2">
          <h3 id="geo-meter" className="text-sm font-medium">
            Lookups today
          </h3>
          <span className="text-sm tabular-nums">
            {used} / {cap}
          </span>
        </div>
        <div
          role="meter"
          aria-labelledby="geo-meter"
          aria-valuemin={0}
          aria-valuemax={cap}
          aria-valuenow={Math.min(used, cap)}
          aria-valuetext={`${used} of ${cap} lookups used`}
          className="h-3 overflow-hidden rounded-full bg-muted"
        >
          <div
            className={ratio >= 0.9 ? 'h-full bg-amber-600' : 'h-full bg-primary'}
            style={{ width: `${ratio * 100}%` }}
          />
        </div>
        <dl className="mt-2 grid grid-cols-2 gap-3 text-sm">
          <div>
            <dt className="text-xs text-muted-foreground">Remaining</dt>
            <dd className="font-medium tabular-nums">
              {u.remaining === null ? 'Unknown' : u.remaining}
            </dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">Cache hit ratio</dt>
            <dd className="font-medium tabular-nums">{pct(u.cache_hit_ratio)}</dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">Cache hits today</dt>
            <dd className="font-medium tabular-nums">{u.cache_hits_today}</dd>
          </div>
        </dl>
      </div>
      <p className="text-sm text-muted-foreground">
        Place names come from a geocoding provider whose free tier is shared by every lookup this
        server makes. Results are cached, and lookups stop for the day once the cap is reached.
      </p>
    </div>
  )
}

export function GeocodingSection() {
  const cfg = useUiConfig()
  const enabled = cfg.data?.geocode_enabled === true
  const usage = useGeocodeUsage(enabled)

  let body: React.ReactNode
  if (cfg.isPending)
    body = (
      <div role="status" className="h-24 animate-pulse rounded-lg bg-accent">
        <span className="sr-only">Loading</span>
      </div>
    )
  else if (!enabled)
    body = (
      <Notice title="Geocoding is off">
        Place names are not looked up. Set a server geocoding key in the server configuration to
        turn it on.
      </Notice>
    )
  else if (usage.isPending)
    body = (
      <div role="status" className="h-24 animate-pulse rounded-lg bg-accent">
        <span className="sr-only">Loading usage</span>
      </div>
    )
  else if (usage.error instanceof ApiError && usage.error.status === 404)
    body = (
      <Notice title="Geocoding not available">
        This server does not report geocoding usage yet.
      </Notice>
    )
  else if (usage.error)
    body = (
      <p role="alert" className="text-sm text-destructive">
        Could not load usage: {commandErrorMessage(usage.error)}
      </p>
    )
  else if (usage.data && !usage.data.enabled)
    body = (
      <Notice title="Geocoding is off">
        Place names are not looked up. Set a server geocoding key in the server configuration to
        turn it on.
      </Notice>
    )
  else if (usage.data) body = <Usage u={usage.data} />

  return (
    <section aria-labelledby="geo-h" className="flex flex-col gap-4">
      <h2 id="geo-h" className="text-lg font-semibold">
        Geocoding
      </h2>
      {body}
    </section>
  )
}
