import { useState, useEffect, useCallback } from "react";
import { serverApi } from "@/lib/api";
import type { HandoffStatus } from "@/types/hotswap";

export type HotSwapPhase = "idle" | "starting" | "in_progress" | "complete" | "failed" | "unsupported";

interface UseHotSwapReturn {
  supported: boolean;
  phase: HotSwapPhase;
  status: HandoffStatus | null;
  isBusy: boolean;
  error: string | null;
  triggerSwap: (binaryPath: string) => Promise<void>;
  clearError: () => void;
}

/**
 * Hook for the hot-swap feature.
 * - Checks platform support (Windows only).
 * - Polls status at 500ms while a swap is in progress.
 * - Exposes triggerSwap() and current phase/status.
 */
export function useHotSwap(): UseHotSwapReturn {
  const [supported, setSupported] = useState(false);
  const [phase, setPhase] = useState<HotSwapPhase>("idle");
  const [status, setStatus] = useState<HandoffStatus | null>(null);
  const [error, setError] = useState<string | null>(null);

  const isBusy = phase === "starting" || phase === "in_progress";

  // Check platform support on mount via the status endpoint (does not wrap /config).
  useEffect(() => {
    let cancelled = false;
    const checkSupport = async () => {
      try {
        const s = await serverApi.getHotSwapStatus();
        if (!cancelled) {
          setSupported(s.phase !== "unsupported");
        }
      } catch {
        // Not supported or not loaded yet.
      }
    };
    void checkSupport();
    return () => { cancelled = true; };
  }, []);

  // Poll status while busy.
  useEffect(() => {
    if (!isBusy) return;

    let timer: ReturnType<typeof setTimeout>;

    const poll = async () => {
      try {
        const s = await serverApi.getHotSwapStatus();
        setStatus(s);
        setPhase(s.phase as HotSwapPhase);

        if (s.phase === "complete" || s.phase === "failed") {
          if (s.phase === "failed") {
            setError(s.error || "Hot-swap failed");
          }
          // Stop polling after terminal state.
          return;
        }
      } catch {
        // Network error — keep polling.
      }
      timer = setTimeout(poll, 500);
    };

    void poll();
    return () => clearTimeout(timer);
  }, [isBusy]);

  const triggerSwap = useCallback(async (binaryPath: string) => {
    setError(null);
    setStatus(null);
    setPhase("starting");

    try {
      await serverApi.hotSwap(binaryPath);
      // The swap is now in progress — the poller will track it.
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to start hot-swap";
      setError(msg);
      setPhase("failed");
    }
  }, []);

  const clearError = useCallback(() => {
    setError(null);
  }, []);

  return { supported, phase, status, isBusy, error, triggerSwap, clearError };
}
