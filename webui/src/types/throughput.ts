// Throughput stats types — mirror the Go stats package response.
export interface ThroughputRecord {
  slot_id: number
  task_id: number
  captured_at: string
  // Decode (generation) timing.
  gen_tokens: number
  gen_time_ms: number
  gen_per_token_ms: number
  gen_tokens_per_second: number
  // Prompt (prefill) timing.
  prompt_tokens: number
  prompt_time_ms: number
  prompt_per_token_ms: number
  prompt_tokens_per_second: number
}

export interface ThroughputAggregates {
  count: number
  avg_gen_tokens_per_second: number
  min_gen_tokens_per_second: number
  max_gen_tokens_per_second: number
  p95_gen_tokens_per_second: number
  avg_gen_per_token_ms: number
}

export interface ThroughputStats {
  instance: string
  records: ThroughputRecord[]
  aggregates: ThroughputAggregates
}

export type ThroughputStatus = 'idle' | 'generating' | 'unknown'
