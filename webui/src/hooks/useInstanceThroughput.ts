import { useState, useEffect } from 'react'
import type { InstanceStatus } from '@/types/instance'
import { throughputService, type ThroughputSnapshot } from '@/lib/throughputService'

// useInstanceThroughput subscribes to live throughput state for a single
// instance: the real-time "generating" flag plus the most recent throughput
// history (tok/s, ms/tok, prompt speed). Returns undefined until the first
// snapshot arrives.
export function useInstanceThroughput(
  instanceName: string,
  instanceStatus: InstanceStatus,
): ThroughputSnapshot | undefined {
  const [snap, setSnap] = useState<ThroughputSnapshot | undefined>()

  useEffect(() => {
    const unsubscribe = throughputService.subscribe(instanceName, setSnap)
    return unsubscribe
  }, [instanceName])

  // Nudge a refresh when the instance transitions to an active state.
  useEffect(() => {
    if (instanceStatus === 'running' || instanceStatus === 'restarting') {
      throughputService.refresh(instanceName)
    }
  }, [instanceName, instanceStatus])

  return snap
}
