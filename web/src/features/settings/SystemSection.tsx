import { useBroker } from '@/features/devices/queries'
import { useUiConfig } from '@/features/config/queries'
import { cn } from '@/lib/utils'

function Item({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd dir="auto" className="text-sm break-words">
        {children}
      </dd>
    </div>
  )
}

export function SystemSection() {
  const broker = useBroker()
  const cfg = useUiConfig()
  const b = broker.data
  const since = b?.since ? new Date(b.since) : null
  return (
    <section aria-labelledby="sys-h" className="flex flex-col gap-4">
      <h2 id="sys-h" className="text-lg font-semibold">
        System
      </h2>
      <div className="rounded-lg border p-4">
        <h3 className="mb-3 text-sm font-medium">MQTT broker</h3>
        {broker.isPending ? (
          <p role="status" className="text-sm text-muted-foreground">
            Loading broker status…
          </p>
        ) : broker.error || !b ? (
          <p role="alert" className="text-sm text-destructive">
            Could not load broker status.
          </p>
        ) : (
          <dl className="grid gap-3 sm:grid-cols-2">
            <Item label="State">
              <span className="inline-flex items-center gap-2" data-testid="system-broker-state">
                <span
                  aria-hidden="true"
                  className={cn(
                    'size-2.5 rounded-full',
                    b.connected ? 'bg-green-600 dark:bg-green-400' : 'bg-red-600 dark:bg-red-400',
                  )}
                />
                {b.connected ? 'Connected' : 'Disconnected'}
              </span>
            </Item>
            <Item label="Host">
              <span className="font-mono">{b.host || 'Unknown'}</span>
            </Item>
            <Item label="Since">
              {since && Number.isFinite(since.getTime()) ? since.toLocaleString() : 'Unknown'}
            </Item>
            <Item label="Last error">{b.last_error || 'None'}</Item>
          </dl>
        )}
      </div>
      <div className="rounded-lg border p-4">
        <h3 className="mb-3 text-sm font-medium">Map</h3>
        {cfg.data ? (
          <dl className="grid gap-3 sm:grid-cols-2">
            <Item label="Provider">{cfg.data.map.provider || 'Unknown'}</Item>
            <Item label="Attribution">{cfg.data.map.attribution || 'None'}</Item>
          </dl>
        ) : cfg.error ? (
          <p role="alert" className="text-sm text-destructive">
            Could not load the map configuration.
          </p>
        ) : (
          <p role="status" className="text-sm text-muted-foreground">
            Loading…
          </p>
        )}
      </div>
      <p className="text-sm text-muted-foreground">
        These values come from the server configuration and cannot be changed here.
      </p>
    </section>
  )
}
