import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { instanceEventsClient } from "@/lib/instanceEventsClient";

// Use the EventSourceMock exposed by test/setup.ts
const EventSourceMock = (globalThis as any).__EventSourceMock;

describe("InstanceEventsClient", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    // Reset the singleton's disposed flag so each test can connect fresh.
    (instanceEventsClient as any).disposed = false;
    (instanceEventsClient as any).es = null;
    (instanceEventsClient as any).reconnectAttempts = 0;
    (instanceEventsClient as any).callbacks.clear();
  });

  afterEach(() => {
    instanceEventsClient.destroy();
  });

  it("delivers status_change events to subscribers", () => {
    const cb = vi.fn();
    const unsubscribe = instanceEventsClient.subscribe(cb);

    // Find the active EventSource instance created by the client
    // (the mock is a class; the client stored a real instance on .es which is private)
    // We access it via the mock's dispatchEvent helper by reaching into the client.
    // Since `es` is private, we use a test hook: the mock instance is created in
    // connect(). We trigger it by calling dispatchEvent on the underlying mock.
    // The simplest path: re-create a scenario where we know the ES instance.
    // Here we use the fact that the mock class instances are reachable via the
    // client's internal `es` (private in TS but accessible at runtime).
    const es = (instanceEventsClient as any).es as InstanceType<typeof EventSourceMock> | null;
    expect(es).not.toBeNull();

    es!.dispatchEvent("status_change", JSON.stringify({
      type: "status_change",
      name: "test-instance",
      oldStatus: "stopped",
      newStatus: "running",
    }));

    expect(cb).toHaveBeenCalledTimes(1);
    expect(cb).toHaveBeenCalledWith({
      name: "test-instance",
      oldStatus: "stopped",
      newStatus: "running",
    });

    unsubscribe();
  });

  it("ignores malformed event payloads", () => {
    const cb = vi.fn();
    const unsubscribe = instanceEventsClient.subscribe(cb);
    const es = (instanceEventsClient as any).es as InstanceType<typeof EventSourceMock> | null;
    expect(es).not.toBeNull();

    es!.dispatchEvent("status_change", "not-json{{{");

    expect(cb).not.toHaveBeenCalled();
    unsubscribe();
  });
});
