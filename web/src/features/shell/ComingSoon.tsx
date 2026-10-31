import { Hourglass } from 'lucide-react'

export function ComingSoon({ title }: { title: string }) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed p-10 text-center">
      <Hourglass aria-hidden="true" className="size-10 text-muted-foreground" />
      <h1 className="text-xl font-semibold">{title}</h1>
      <p className="text-sm text-muted-foreground">Coming soon</p>
    </div>
  )
}
