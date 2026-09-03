export interface HandoffStatus {
  phase: string
  supported?: boolean
  a_pid?: number
  b_pid?: number
  handoff_port?: number
  sockets_handed_off?: number
  total_sockets?: number
  error?: string
  started_at?: string
  completed_at?: string
}

export interface HotSwapResult {
  status: string
}
