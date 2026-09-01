import { render, screen, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import ThroughputBadge from '@/components/ThroughputBadge'
import { throughputService, type ThroughputSnapshot } from '@/lib/throughputService'
import { useInstanceThroughput } from '@/hooks/useInstanceThroughput'

// We test the badge by controlling the hook it uses.
vi.mock('@/hooks/useInstanceThroughput', () => ({
  useInstanceThroughput: vi.fn(),
}))

const running = 'running' as const
const stopped = 'stopped' as const

function snap(over: Partial<ThroughputSnapshot> = {}): ThroughputSnapshot {
  return {
    status: 'idle',
    activeSlots: 0,
    records: [],
    aggregates: {
      count: 0,
      avg_gen_tokens_per_second: 0,
      min_gen_tokens_per_second: 0,
      max_gen_tokens_per_second: 0,
      p95_gen_tokens_per_second: 0,
      avg_gen_per_token_ms: 0,
    },
    timestamp: Date.now(),
    ...over,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('ThroughputBadge', () => {
  it('renders nothing when the instance is stopped', () => {
    vi.mocked(useInstanceThroughput).mockReturnValue(undefined)
    const { container } = render(
      <ThroughputBadge instanceName="m" instanceStatus={stopped} />,
    )
    expect(container).toBeEmptyDOMElement()
  })

  it('shows the idle state with no history', () => {
    vi.mocked(useInstanceThroughput).mockReturnValue(snap())
    render(<ThroughputBadge instanceName="m" instanceStatus={running} />)
    expect(screen.getByText('idle')).toBeTruthy()
  })

  it('shows the generating state with live tok/s when a slot is active', () => {
    vi.mocked(useInstanceThroughput).mockReturnValue(
      snap({
        status: 'generating',
        activeSlots: 1,
        records: [
          {
            slot_id: 0,
            task_id: 100,
            captured_at: new Date().toISOString(),
            gen_tokens: 120,
            gen_time_ms: 1200,
            gen_per_token_ms: 10,
            gen_tokens_per_second: 100,
            prompt_tokens: 50,
            prompt_time_ms: 50,
            prompt_per_token_ms: 1,
            prompt_tokens_per_second: 1000,
          },
        ],
        aggregates: { count: 5, avg_gen_tokens_per_second: 88, min_gen_tokens_per_second: 60, max_gen_tokens_per_second: 110, p95_gen_tokens_per_second: 105, avg_gen_per_token_ms: 12 },
      }),
    )
    const { container } = render(
      <ThroughputBadge instanceName="m" instanceStatus={running} />,
    )
    expect(screen.getByText('Generating')).toBeTruthy()
    // live tok/s shown alongside
    expect(container.textContent).toContain('100 tok/s')
  })

  it('shows last + recent aggregates when idle but history exists', () => {
    vi.mocked(useInstanceThroughput).mockReturnValue(
      snap({
        records: [
          {
            slot_id: 0,
            task_id: 101,
            captured_at: new Date().toISOString(),
            gen_tokens: 200,
            gen_time_ms: 3000,
            gen_per_token_ms: 15,
            gen_tokens_per_second: 66.4,
            prompt_tokens: 300,
            prompt_time_ms: 100,
            prompt_per_token_ms: 0.3,
            prompt_tokens_per_second: 3000,
          },
        ],
        aggregates: { count: 8, avg_gen_tokens_per_second: 70, min_gen_tokens_per_second: 55, max_gen_tokens_per_second: 82, p95_gen_tokens_per_second: 80, avg_gen_per_token_ms: 14.5 },
      }),
    )
    const { container } = render(
      <ThroughputBadge instanceName="m" instanceStatus={running} />,
    )
    expect(container.textContent).toContain('last')
    expect(container.textContent).toContain('66.4 tok/s')
    // sparkline renders when >=2 records
    // (only 1 here, so no sparkline — assert it's absent)
    expect(container.querySelector('svg polyline')).toBeNull()
  })

  it('renders a sparkline when there are >= 2 history records', () => {
    const rec = (taskId: number, tps: number) => ({
      slot_id: 0, task_id: taskId, captured_at: new Date().toISOString(),
      gen_tokens: 100, gen_time_ms: 1000, gen_per_token_ms: 10, gen_tokens_per_second: tps,
      prompt_tokens: 0, prompt_time_ms: 0, prompt_per_token_ms: 0, prompt_tokens_per_second: 0,
    })
    vi.mocked(useInstanceThroughput).mockReturnValue(
      snap({ records: [rec(1, 60), rec(2, 80), rec(3, 70)] }),
    )
    const { container } = render(
      <ThroughputBadge instanceName="m" instanceStatus={running} />,
    )
    const spark = container.querySelector('svg polyline')
    expect(spark).not.toBeNull()
    expect(spark!.getAttribute('points')).toMatch(/\d/)
  })
})
