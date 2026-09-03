import { useState, useEffect } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Loader2, CheckCircle2, XCircle, CloudUpload, FolderOpen } from "lucide-react";
import { useHotSwap } from "@/hooks/useHotSwap";
import { toast } from "sonner";

interface HotSwapDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function HotSwapDialog({ open, onOpenChange }: HotSwapDialogProps) {
  const { supported, phase, status, isBusy, error, triggerSwap } = useHotSwap();
  const [binaryPath, setBinaryPath] = useState("");
  const [candidates, setCandidates] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);

  // Fetch candidate binaries when the dialog opens.
  useEffect(() => {
    if (!open || !supported) return;
    let cancelled = false;
    setLoading(true);
    fetch(`${document.baseURI}api/v1/hot-swap/candidates`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: string[]) => {
        if (!cancelled) setCandidates(data);
      })
      .catch(() => {
        if (!cancelled) setCandidates([]);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => { cancelled = true; };
  }, [open, supported]);

  if (!supported) {
    return null;
  }

  const handleSwap = async () => {
    if (!binaryPath.trim()) {
      toast.error("Please select a binary to hot-swap.");
      return;
    }
    try {
      await triggerSwap(binaryPath.trim());
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to start hot-swap");
    }
  };

  const handleClose = () => {
    if (isBusy) {
      if (!confirm("A swap is in progress. Close anyway?")) return;
    }
    onOpenChange(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <CloudUpload className="h-5 w-5" />
            Hot Swap
          </DialogTitle>
          <DialogDescription>
            Replace the running llamactl binary without stopping model instances or dropping connections.
            The new binary must be a compatible build of llamactl.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-4">
          <div className="space-y-2">
            <Label htmlFor="binary-path">New binary</Label>
            {loading ? (
              <div className="flex items-center gap-2 text-sm text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" />
                Loading candidates...
              </div>
            ) : candidates.length > 0 ? (
              <select
                id="binary-path"
                value={binaryPath}
                onChange={(e) => setBinaryPath(e.target.value)}
                disabled={isBusy}
                className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
              >
                <option value="">Select a binary…</option>
                {candidates.map((c) => (
                  <option key={c} value={c}>
                    {c}
                  </option>
                ))}
              </select>
            ) : (
              <div className="flex gap-2">
                <Input
                  id="binary-path"
                  placeholder="C:\path\to\llamactl.exe"
                  value={binaryPath}
                  onChange={(e) => setBinaryPath(e.target.value)}
                  disabled={isBusy}
                  className="flex-1"
                />
                <Button
                  variant="outline"
                  size="icon"
                  disabled={isBusy}
                  title="No local candidates found — enter path manually"
                >
                  <FolderOpen className="h-4 w-4" />
                </Button>
              </div>
            )}
            {candidates.length === 0 && (
              <p className="text-xs text-muted-foreground">
                No candidate binaries found in the llamactl directory.
                Enter the full path manually.
              </p>
            )}
          </div>

          {/* Status display */}
          {status !== null && status !== undefined && (
            <div className="rounded-md border p-3 space-y-1 text-sm">
              <div className="flex items-center gap-2">
                {phase === "complete" && <CheckCircle2 className="h-4 w-4 text-green-500" />}
                {phase === "failed" && <XCircle className="h-4 w-4 text-red-500" />}
                {isBusy && <Loader2 className="h-4 w-4 animate-spin" />}
                <span className="font-medium">
                  Phase:                 {phase !== null && phase !== undefined ? phase.replace(/_/g, " ") : "idle"}
                </span>
              </div>
              {status.sockets_handed_off !== undefined && (
                <div className="text-muted-foreground">
                  Sockets handed off: {status.sockets_handed_off}/{status.total_sockets || "?"}
                </div>
              )}
              {status.a_pid !== undefined && (
                <div className="text-muted-foreground">A PID: {status.a_pid}</div>
              )}
              {status.b_pid !== undefined && (
                <div className="text-muted-foreground">B PID: {status.b_pid}</div>
              )}
            </div>
          )}

          {error && (
            <div className="rounded-md border border-red-500/50 bg-red-500/10 p-3 text-sm text-red-600 dark:text-red-400">
              {error}
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={handleClose} disabled={isBusy}>
            Close
          </Button>
          <Button
            onClick={handleSwap}
            disabled={isBusy || !binaryPath.trim()}
            variant="default"
          >
            {isBusy ? (
              <>
                <Loader2 className="h-4 w-4 animate-spin mr-2" />
                Swapping...
              </>
            ) : (
              <>
                <CloudUpload className="h-4 w-4 mr-2" />
                Start Swap
              </>
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
