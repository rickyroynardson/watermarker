import type { BatchDetails } from "./api";

// Our server sends named JSON snapshots, not a historical event log.
export async function readBatchEvents(
  body: ReadableStream<Uint8Array>,
  onBatch: (batch: BatchDetails) => void,
  onBytes: () => void = () => {},
) {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) return;
      onBytes();
      buffer += decoder.decode(value, { stream: true });
      // ponytail: snapshots capped at 8 MiB; use incremental image events for very large batches.
      if (buffer.length > 8 * 1024 * 1024) throw new Error("Progress snapshot is too large.");
      let delimiter: RegExpExecArray | null;
      while ((delimiter = /\r?\n\r?\n/.exec(buffer))) {
        const frame = buffer.slice(0, delimiter.index);
        buffer = buffer.slice(delimiter.index + delimiter[0].length);
        let event = "";
        const data: string[] = [];
        for (const line of frame.split(/\r?\n/)) {
          if (line.startsWith("event:")) event = line.slice(6).trim();
          if (line.startsWith("data:")) data.push(line.slice(5).replace(/^ /, ""));
        }
        if (event === "batch") onBatch(JSON.parse(data.join("\n")) as BatchDetails);
      }
    }
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

class StreamHTTPError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

function wait(ms: number, signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    signal.throwIfAborted();
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", abort);
      resolve();
    }, ms);
    function abort() {
      clearTimeout(timer);
      reject(signal.reason);
    }
    signal.addEventListener("abort", abort, { once: true });
  });
}

export async function watchBatch(
  id: string,
  apiKey: string,
  signal: AbortSignal,
  onBatch: (batch: BatchDetails) => void,
  onStatus: (status: "connecting" | "live" | "reconnecting") => void,
) {
  let delay = 1000;
  onStatus("connecting");
  while (!signal.aborted) {
    const connection = new AbortController();
    let watchdog: ReturnType<typeof setTimeout>;
    const touch = () => {
      clearTimeout(watchdog);
      watchdog = setTimeout(() => connection.abort(), 45_000);
    };
    touch();
    try {
      const response = await fetch(`/api/batches/${id}/events`, {
        headers: {
          Accept: "text/event-stream",
          ...(apiKey && { Authorization: `Bearer ${apiKey}` }),
        },
        credentials: "same-origin",
        signal: AbortSignal.any([signal, connection.signal]),
        cache: "no-store",
      });
      if (!response.ok) {
        const body = await response.json().catch(() => null);
        throw new StreamHTTPError(
          response.status,
          body?.error?.message || `Live updates failed (HTTP ${response.status}).`,
        );
      }
      if (
        !response.body ||
        !response.headers.get("content-type")?.startsWith("text/event-stream")
      ) {
        throw new Error("Live updates returned an invalid response.");
      }
      await readBatchEvents(
        response.body,
        (batch) => {
          delay = 1000;
          onStatus("live");
          onBatch(batch);
        },
        touch,
      );
    } catch (cause) {
      if (signal.aborted) return;
      if (cause instanceof StreamHTTPError && [400, 401, 403, 404].includes(cause.status))
        throw cause;
    } finally {
      clearTimeout(watchdog!);
      connection.abort();
    }
    if (signal.aborted) return;
    onStatus("reconnecting");
    try {
      await wait(delay, signal);
    } catch {
      return;
    }
    delay = Math.min(delay * 2, 15_000);
  }
}
