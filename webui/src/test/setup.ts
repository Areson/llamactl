import '@testing-library/jest-dom'
import { afterEach, beforeEach, vi } from 'vitest'

// Create a working localStorage implementation for tests
// This ensures localStorage works in both CLI and VSCode test runner
class LocalStorageMock implements Storage {
  private store: Map<string, string> = new Map()

  get length(): number {
    return this.store.size
  }

  clear(): void {
    this.store.clear()
  }

  getItem(key: string): string | null {
    return this.store.get(key) ?? null
  }

  key(index: number): string | null {
    return Array.from(this.store.keys())[index] ?? null
  }

  removeItem(key: string): void {
    this.store.delete(key)
  }

  setItem(key: string, value: string) {
    this.store.set(key, value)
  }
}

// Replace global localStorage
global.localStorage = new LocalStorageMock()

// EventSource is not available in jsdom; provide a minimal mock so the SSE
// client can be exercised in tests. Listeners are stored so tests can fire them.
class EventSourceMock {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2
  readyState = EventSourceMock.CLOSED
  onerror: ((e: Event) => void) | null = null
  private listeners: Map<string, Set<EventListener>> = new Map()

  addEventListener(type: string, listener: EventListener) {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set())
    this.listeners.get(type)!.add(listener)
  }

  removeEventListener(type: string, listener: EventListener) {
    this.listeners.get(type)?.delete(listener)
  }

  close() {
    this.readyState = EventSourceMock.CLOSED
  }

  // Test helper: simulate the server sending an event.
  dispatchEvent(type: string, data?: string) {
    this.listeners.get(type)?.forEach((listener) => listener({ data: data ?? '' } as MessageEvent))
  }
}

// Expose the mock on globalThis so the SSE client picks it up.
;(globalThis as any).EventSource = EventSourceMock
// Also attach a reference for tests to access.
;(globalThis as any).__EventSourceMock = EventSourceMock

// Create a default fetch mock that handles common API endpoints
const createMockFetch = () => {
  return vi.fn((url: string) => {
    // Handle API endpoints that return JSON
    if (url.includes('/api/v1/')) {
      return Promise.resolve(
        new Response(JSON.stringify({}), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      )
    }
    // Default response for other requests
    return Promise.resolve(
      new Response(null, { status: 200 })
    )
  })
}

// Clean up before each test
beforeEach(() => {
  localStorage.clear()
  // Set up default fetch mock
  global.fetch = createMockFetch() as typeof fetch
})

afterEach(() => {
  localStorage.clear()
  vi.restoreAllMocks()
})

afterEach(() => {
  localStorage.clear()
})