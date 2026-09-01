import { type ReactNode, createContext, useContext, useState, useEffect, useCallback, useRef } from 'react'
import type { CreateInstanceOptions, Instance } from '@/types/instance'
import { instancesApi } from '@/lib/api'
import { useAuth } from '@/contexts/AuthContext'
import { healthService } from '@/lib/healthService'

interface InstancesContextState {
  instances: Instance[]
  loading: boolean
  error: string | null
}

interface InstancesContextActions {
  fetchInstances: () => Promise<void>
  createInstance: (name: string, options: CreateInstanceOptions) => Promise<void>
  updateInstance: (name: string, options: CreateInstanceOptions) => Promise<void>
  startInstance: (name: string) => Promise<void>
  stopInstance: (name: string) => Promise<void>
  restartInstance: (name: string) => Promise<void>
  deleteInstance: (name: string) => Promise<void>
  clearError: () => void
}

type InstancesContextType = InstancesContextState & InstancesContextActions

const InstancesContext = createContext<InstancesContextType | undefined>(undefined)

interface InstancesProviderProps {
  children: ReactNode
}

export const InstancesProvider = ({ children }: InstancesProviderProps) => {
  const { isAuthenticated, isLoading: authLoading } = useAuth()
  const [instancesMap, setInstancesMap] = useState<Map<string, Instance>>(new Map())
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Convert map to array for consumers
  const instances = Array.from(instancesMap.values())

  // Polling intervals (ms) — adaptive based on whether any instance is in a transition
  const TRANSITION_STATUSES = new Set(["running", "restarting", "shutting_down"])
  const POLL_ACTIVE_MS = 2000  // 2s while any instance is starting/restarting/shutting down
  const POLL_IDLE_MS = 15000   // 15s when all instances are stable (stopped/failed)

  const clearError = useCallback(() => {
    setError(null)
  }, [])

  // Background poller: keeps the instance list fresh so start/stop/restart
  // state changes are reflected without a manual browser refresh.
  const pollTimerRef = useRef<NodeJS.Timeout | null>(null)
  const pollIntervalRef = useRef<number>(POLL_IDLE_MS)

  // fetchInstances is defined below and stashed in a ref so the poller and
  // the fetch can reference each other without a temporal-dead-zone cycle.
  const fetchInstancesRef = useRef<() => Promise<void>>(async () => {})

  const clearPollTimer = useCallback(() => {
    if (pollTimerRef.current) {
      clearTimeout(pollTimerRef.current)
      pollTimerRef.current = null
    }
  }, [])

  const scheduleNextPoll = useCallback((instancesList: Instance[]) => {
    const anyTransitioning = instancesList.some((i) => TRANSITION_STATUSES.has(i.status))
    const nextInterval = anyTransitioning ? POLL_ACTIVE_MS : POLL_IDLE_MS
    pollIntervalRef.current = nextInterval
    clearPollTimer()
    pollTimerRef.current = setTimeout(() => {
      pollTimerRef.current = null
      void fetchInstancesRef.current()
    }, nextInterval)
  }, [clearPollTimer])

  const fetchInstances = useCallback(async () => {
    if (!isAuthenticated) {
      setLoading(false)
      return
    }

    try {
      setError(null)
      const data = await instancesApi.list()

      // Convert array to map
      const newMap = new Map<string, Instance>()
      data.forEach(instance => {
        newMap.set(instance.name, instance)
      })
      setInstancesMap(newMap)

      // Initial-load gate is satisfied once data is present.
      setLoading(false)

      // Schedule the next background poll with an interval appropriate to
      // whether any instance is mid-transition.
      scheduleNextPoll(data)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch instances')
      setLoading(false)
      // Keep trying even on error, but back off to the idle interval
      pollIntervalRef.current = POLL_IDLE_MS
      clearPollTimer()
      pollTimerRef.current = setTimeout(() => {
        pollTimerRef.current = null
        void fetchInstancesRef.current()
      }, POLL_IDLE_MS)
    }
  }, [isAuthenticated, scheduleNextPoll, clearPollTimer])

  // Keep the ref current so the poller always calls the latest fetchInstances.
  useEffect(() => {
    fetchInstancesRef.current = fetchInstances
  }, [fetchInstances])

  const updateInstanceInMap = useCallback((name: string, updates: Partial<Instance>) => {
    setInstancesMap(prev => {
      const newMap = new Map(prev)
      const existing = newMap.get(name)
      if (existing) {
        newMap.set(name, { ...existing, ...updates })
      }
      return newMap
    })
  }, [])

  const createInstance = useCallback(async (name: string, options: CreateInstanceOptions) => {
    try {
      setError(null)
      const newInstance = await instancesApi.create(name, options)
      
      // Add to map directly
      setInstancesMap(prev => {
        const newMap = new Map(prev)
        newMap.set(name, newInstance)
        return newMap
      })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create instance')
    }
  }, [])

  const updateInstance = useCallback(async (name: string, options: CreateInstanceOptions) => {
    try {
      setError(null)
      const updatedInstance = await instancesApi.update(name, options)
      
      // Update in map directly
      setInstancesMap(prev => {
        const newMap = new Map(prev)
        newMap.set(name, updatedInstance)
        return newMap
      })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update instance')
    }
  }, [])

  const startInstance = useCallback(async (name: string) => {
    try {
      setError(null)
      await instancesApi.start(name)

      // Update only this instance's status
      updateInstanceInMap(name, { status: "running" })

      // Trigger health check after starting
      healthService.checkHealthAfterOperation(name, 'start')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to start instance')
    }
  }, [updateInstanceInMap])

  const stopInstance = useCallback(async (name: string) => {
    try {
      setError(null)
      await instancesApi.stop(name)

      // Update only this instance's status
      updateInstanceInMap(name, { status: "stopped" })

      // Trigger health check after stopping
      healthService.checkHealthAfterOperation(name, 'stop')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to stop instance')
    }
  }, [updateInstanceInMap])

  const restartInstance = useCallback(async (name: string) => {
    try {
      setError(null)
      await instancesApi.restart(name)

      // Update only this instance's status
      updateInstanceInMap(name, { status: "running" })

      // Trigger health check after restarting
      healthService.checkHealthAfterOperation(name, 'restart')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to restart instance')
    }
  }, [updateInstanceInMap])

  const deleteInstance = useCallback(async (name: string) => {
    try {
      setError(null)
      await instancesApi.delete(name)
      
      // Remove from map directly
      setInstancesMap(prev => {
        const newMap = new Map(prev)
        newMap.delete(name)
        return newMap
      })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete instance')
    }
  }, [])

  // Only fetch instances when auth is ready and user is authenticated.
  // Also kicks off the background polling loop and cleans it up on unmount /
  // when auth drops.
  useEffect(() => {
    if (authLoading) return
    if (isAuthenticated) {
      void fetchInstances()
    } else {
      // Stop polling and clear instances when not authenticated
      if (pollTimerRef.current) {
        clearTimeout(pollTimerRef.current)
        pollTimerRef.current = null
      }
      setInstancesMap(new Map())
      setLoading(false)
      setError(null)
    }
    return () => {
      if (pollTimerRef.current) {
        clearTimeout(pollTimerRef.current)
        pollTimerRef.current = null
      }
    }
  }, [authLoading, isAuthenticated, fetchInstances])

  const value: InstancesContextType = {
    instances,
    loading,
    error,
    fetchInstances,
    createInstance,
    updateInstance,
    startInstance,
    stopInstance,
    restartInstance,
    deleteInstance,
    clearError,
  }

  return (
    <InstancesContext.Provider value={value}>
      {children}
    </InstancesContext.Provider>
  )
}

export const useInstances = (): InstancesContextType => {
  const context = useContext(InstancesContext)
  if (context === undefined) {
    throw new Error('useInstances must be used within an InstancesProvider')
  }
  return context
}