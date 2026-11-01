import { useQuery } from '@tanstack/react-query'

import { fetchUiConfig, uiConfigKey } from '@/lib/api'

export const useUiConfig = () =>
  useQuery({
    queryKey: uiConfigKey,
    queryFn: fetchUiConfig,
    staleTime: 5 * 60_000,
    retry: (count: number, err: Error) => !(err as { status?: number }).status && count < 2,
  })
