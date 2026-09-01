import { API_BASE } from '@/lib/api'

type InstanceEventCallback = (event: {
  name: string
  oldStatus: string
  newStatus: string
}) => void

/**
 * InstanceEventsClient wraps an EventSource to the backend SSE endpoint
 * (/api/v1/instances/events). It auto-reconnects with backoff and exposes
 * subscribe/unsubscribe.
 *
 * The client is resilient: if the SSE connection fails (e.g. the backend is
 * an older build without the endpoint), subscribers can fall back to polling.
 */
class InstanceEventsClient {
  private es: EventSource | null = null
  private callbacks: Set<InstanceEventCallback> = new Set()
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private reconnectAttempts = 0
  private disposed = false
  private static readonly MAX_BACKOFF_MS = 30_000
  private static readonly BASE_BACKOFF_MS = 1_000

  get connected(): boolean {
    return this.es?.readyState === EventSource.OPEN
  }

  /**
   * Subscribe to instance events. Returns an unsubscribe func.
   * Starts the EventSource on the first subscriber.
   */
  subscribe(callback: InstanceEventCallback): () => void {
    this.callbacks.add(callback)
    if (this.callbacks.size === 1) {
      this.connect()
    }
    return () => {
      this.callbacks.delete(callback)
      if (this.callbacks.size === 0) {
        this.disconnect()
      }
    }
  }

  private connect(): void {
    if (this.disposed || this.es) return

    // Auth: the api layer stores the key in sessionStorage; EventSource can't
    // set headers, so we pass it as a query param. The backend auth middleware
    // accepts ?key=*** as an alternative to the Authorization header.
    const key = sessionStorage.getItem('llamactl_management_key')
    const params = new URLSearchParams()
    if (key) params.set('api_key', key)
    const qs = params.toString()
    const url = `${API_BASE}/instances/events${qs ? `?${qs}` : ''}`

    this.es = new EventSource(url)

    this.es.addEventListener('status_change', (e: MessageEvent) => {
      this.reconnectAttempts = 0 // successful event resets backoff
      try {
        const data = JSON.parse(e.data) as {
          type: string
          name: string
          oldStatus: string
          newStatus: string
        }
        if (data.type === 'status_change') {
          this.callbacks.forEach((cb) => cb({
            name: data.name,
            oldStatus: data.oldStatus,
            newStatus: data.newStatus,
          }))
        }
      } catch {
        // Malformed event — ignore.
      }
    })

    this.es.addEventListener('connected', () => {
      this.reconnectAttempts = 0
    })

    this.es.onerror = () => {
      // EventSource auto-reconnects for transient errors, but we want to
      // control the backoff and expose a stable "connected" signal.
      if (this.es?.readyState === EventSource.CLOSED) {
        this.scheduleReconnect()
      }
    }
  }

  private scheduleReconnect(): void {
    if (this.disposed) return
    this.disconnect()
    if (this.callbacks.size === 0) return

    const backoff = Math.min(
      InstanceEventsClient.BASE_BACKOFF_MS * 2 ** this.reconnectAttempts,
      InstanceEventsClient.MAX_BACKOFF_MS
    )
    this.reconnectAttempts++
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      this.connect()
    }, backoff)
  }

  private disconnect(): void {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
    if (this.es) {
      this.es.close()
      this.es = null
    }
  }

  destroy(): void {
    this.disposed = true
    this.disconnect()
    this.callbacks.clear()
  }
}

export const instanceEventsClient = new InstanceEventsClient()
