import type { ThroughputStats, ThroughputStatus, ThroughputRecord } from '@/types/throughput'
import { instancesApi } from '@/lib/api'

// ThroughputService provides a live "generating" indicator (from the llama.cpp
// /slots endpoint, polled) plus the most recent throughput history (from the
// backend /stats endpoint, which parses llama.cpp's per-request timing log
// lines). Polling is adaptive: faster while a slot is actively generating,
// slower when idle. This mirrors the healthService pattern.
//
// Why two sources:
//  - /slots is the only LIVE view of "is it generating right now", but it does
//    not expose tokens/sec (timings are null there).
//  - /stats gives accurate per-request tok/s, but only for completed requests.
// Together: real-time active state + accurate recent speed.
class ThroughputService {
  private intervals = new Map<string, NodeJS.Timeout>()
  private callbacks = new Map<string, Set<(state: ThroughputSnapshot) => void>>()
  private snapshots = new Map<string, ThroughputSnapshot>()
  private readonly CACHE_TTL = 2000

  // Polling cadence (ms)
  private readonly ACTIVE_INTERVAL = 2000   // while a slot is generating
  private readonly IDLE_INTERVAL = 15000    // when idle (still refreshing history)
  private readonly STABLE_INTERVAL = 60000  // long-idle: only keep history fresh

  async snapshot(instanceName: string): Promise<ThroughputSnapshot> {
    const cached = this.snapshots.get(instanceName)
    if (cached && Date.now() - cached.timestamp < this.CACHE_TTL) {
      return cached
    }

    // Determine live generating status from /slots (best-effort).
    let status: ThroughputStatus = 'unknown'
    let activeSlots = 0
    try {
      const slots = await instancesApi.getSlots(instanceName)
      activeSlots = slots.filter((s) => s.is_processing).length
      status = activeSlots > 0 ? 'generating' : 'idle'
    } catch {
      status = 'unknown' // not running or endpoint unavailable
    }

    // Fetch recent throughput history (best-effort; empty if log missing).
    let stats: ThroughputStats | null = null
    try {
      stats = await instancesApi.getStats(instanceName, 50)
    } catch {
      stats = null
    }

    const snap: ThroughputSnapshot = {
      status,
      activeSlots,
      records: stats?.records ?? [],
      aggregates: stats?.aggregates ?? EMPTY_AGG,
      timestamp: Date.now(),
    }
    this.snapshots.set(instanceName, snap)
    return snap
  }

  subscribe(instanceName: string, cb: (state: ThroughputSnapshot) => void): () => void {
    if (!this.callbacks.has(instanceName)) {
      this.callbacks.set(instanceName, new Set())
    }
    const set = this.callbacks.get(instanceName)!
    set.add(cb)

    if (set.size === 1) {
      this.start(instanceName)
    }

    return () => {
      const s = this.callbacks.get(instanceName)
      if (s) {
        s.delete(cb)
        if (s.size === 0) {
          this.stop(instanceName)
          this.callbacks.delete(instanceName)
          this.snapshots.delete(instanceName)
        }
      }
    }
  }

  refresh(instanceName: string): void {
    void this.snapshot(instanceName).then((snap) => this.notify(instanceName, snap)).catch(() => {})
  }

  private start(instanceName: string): void {
    if (this.intervals.has(instanceName)) return
    this.refresh(instanceName)
    this.schedule(instanceName, this.IDLE_INTERVAL)
  }

  private schedule(instanceName: string, delay: number): void {
    this.stop(instanceName)
    const t = setInterval(async () => {
      let snap: ThroughputSnapshot
      try {
        snap = await this.snapshot(instanceName)
      } catch {
        return
      }
      this.notify(instanceName, snap)
      const next = snap.status === 'generating' ? this.ACTIVE_INTERVAL
        : snap.aggregates.count === 0 ? this.STABLE_INTERVAL
        : this.IDLE_INTERVAL
      if (next !== delay) {
        this.schedule(instanceName, next)
      }
    }, delay)
    this.intervals.set(instanceName, t)
  }

  private stop(instanceName: string): void {
    const t = this.intervals.get(instanceName)
    if (t) {
      clearInterval(t)
      this.intervals.delete(instanceName)
    }
  }

  private notify(instanceName: string, snap: ThroughputSnapshot): void {
    this.callbacks.get(instanceName)?.forEach((cb) => cb(snap))
  }

  destroy(): void {
    this.intervals.forEach((t) => clearInterval(t))
    this.intervals.clear()
    this.callbacks.clear()
    this.snapshots.clear()
  }
}

export const throughputService = new ThroughputService()

// A single polled view of an instance's throughput state.
export interface ThroughputSnapshot {
  status: ThroughputStatus
  activeSlots: number
  records: ThroughputRecord[]
  aggregates: ThroughputStats['aggregates']
  timestamp: number
}

const EMPTY_AGG: ThroughputStats['aggregates'] = {
  count: 0,
  avg_gen_tokens_per_second: 0,
  min_gen_tokens_per_second: 0,
  max_gen_tokens_per_second: 0,
  p95_gen_tokens_per_second: 0,
  avg_gen_per_token_ms: 0,
}
