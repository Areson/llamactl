// ui/src/components/ThroughputBadge.tsx
import { Activity, Gauge } from "lucide-react";
import type { InstanceStatus } from "@/types/instance";
import type { ThroughputRecord } from "@/types/throughput";
import { useInstanceThroughput } from "@/hooks/useInstanceThroughput";

interface ThroughputBadgeProps {
  instanceName: string;
  instanceStatus: InstanceStatus;
}

// ThroughputBadge shows a model's live generation state and recent throughput.
// - Live: pulsing "generating" dot when a slot is actively processing (from /slots).
// - Recent: avg / last tok/s + ms/tok from the most completed generations (from /stats).
// Degrades gracefully: stopped → subtle "idle"; no log → only the live dot.
export default function ThroughputBadge({ instanceName, instanceStatus }: ThroughputBadgeProps) {
  const snap = useInstanceThroughput(instanceName, instanceStatus);

  const running = instanceStatus === "running";
  if (!running) return null;

  const generating = snap?.status === "generating";
  const agg = snap?.aggregates;
  const last = snap?.records?.[0]; // most recent completed generation

  return (
    <div
      className="flex items-center gap-2 text-xs px-2.5 py-1 rounded-md border"
      style={{
        borderColor: generating ? "rgb(16 185 129 / 0.4)" : "rgb(var(--border))",
        background: generating ? "rgb(16 185 129 / 0.08)" : "transparent",
      }}
      title={
        agg && agg.count > 0
          ? `avg ${fmt(agg.avg_gen_tokens_per_second)} tok/s · p95 ${fmt(agg.p95_gen_tokens_per_second)} tok/s · ${agg.count} recent generations`
          : generating
            ? "Generating…"
            : "Idle — no completed generations in recent window"
      }
    >
      <span className="relative flex h-2.5 w-2.5 shrink-0">
        {generating && (
          <span
            className="animate-ping absolute inline-flex h-full w-full rounded-full opacity-70"
            style={{ background: "rgb(16 185 129)" }}
          />
        )}
        <span
          className="relative inline-flex rounded-full h-2.5 w-2.5"
          style={{
            background: generating ? "rgb(16 185 129)" : "rgb(var(--muted-foreground) / 0.5)",
          }}
        />
      </span>

      {generating ? (
        <span className="inline-flex items-center gap-1 font-medium" style={{ color: "rgb(16 185 129)" }}>
          <Activity className="h-3 w-3" />
          Generating
          {last && <span className="opacity-80">· {fmt(last.gen_tokens_per_second)} tok/s</span>}
        </span>
      ) : (
        <span className="inline-flex items-center gap-1 text-muted-foreground">
          <Gauge className="h-3 w-3" />
          {agg && agg.count > 0 ? (
            <>
              last {fmt(last ? last.gen_tokens_per_second : agg.avg_gen_tokens_per_second, 1)} tok/s
              {last && last.prompt_tokens > 0 && last.prompt_tokens_per_second > 0 && (
                <span className="opacity-50">· prefill {fmt(last.prompt_tokens_per_second, 0)} t/s</span>
              )}
            </>
          ) : (
            <span>idle</span>
          )}
        </span>
      )}

      {/* Sparkline of the last N generations' decode tok/s (newest → oldest). */}
      {snap && snap.records.length >= 2 && <Sparkline records={snap.records} />}
    </div>
  );
}

function fmt(n: number, digits = 0): string {
  return n.toFixed(digits);
}

// A minimal inline SVG sparkline of decode throughput for the most recent
// generations. No chart library — just a polyline scaled to the box.
function Sparkline({ records }: { records: ThroughputRecord[] }) {
  // Show the most recent up to 20, oldest → newest left→right.
  const data = records.slice(0, 20).slice().reverse();
  const values = data.map((r) => r.gen_tokens_per_second);
  const w = 72;
  const h = 20;
  const pad = 2;
  const max = Math.max(...values, 1);
  const min = Math.min(...values, 0);
  const range = max - min || 1;
  const step = values.length > 1 ? (w - pad * 2) / (values.length - 1) : 0;

  const points = values
    .map((v, i) => {
      const x = pad + i * step;
      const y = h - pad - ((v - min) / range) * (h - pad * 2);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");

  return (
    <svg width={w} height={h} className="opacity-80" aria-hidden>
      <polyline
        points={points}
        fill="none"
        stroke="rgb(16 185 129)"
        strokeWidth="1.5"
        strokeLinejoin="round"
        strokeLinecap="round"
      />
    </svg>
  );
}
