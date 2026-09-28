import assert from "node:assert/strict";
import { test } from "node:test";
import { readBatchEvents, watchBatch } from "./events.ts";
import type { BatchDetails } from "./api.ts";

test("SSE snapshots survive UTF-8/chunk boundaries, CRLF and keepalives", async () => {
  const snapshot = { id: "batch", status: "pending", images: [{ error: "café" }] };
  const bytes = new TextEncoder().encode(
    `: keepalive\r\n\r\nevent: batch\r\ndata: ${JSON.stringify(snapshot)}\r\n\r\nevent: batch\ndata: {"status":"done"}\n\n`,
  );
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const byte of bytes) controller.enqueue(new Uint8Array([byte]));
      controller.close();
    },
  });
  const received: BatchDetails[] = [];
  await readBatchEvents(body, (batch) => received.push(batch));
  assert.deepEqual(received, [snapshot, { status: "done" }]);
});

test("live progress reconnects with a fresh snapshot and stops on abort or unauthorized", async () => {
  const originalFetch = globalThis.fetch;
  const controller = new AbortController();
  let calls = 0;
  const snapshots: string[] = [];
  globalThis.fetch = async (url, init) => {
    assert.equal(url, "/api/batches/batch/events");
    assert.equal(new Headers(init?.headers).get("Authorization"), "Bearer secret");
    assert.equal(init?.credentials, "same-origin");
    calls++;
    return new Response(
      `event: batch\ndata: {"status":"${calls === 1 ? "pending" : "done"}"}\n\n`,
      { headers: { "Content-Type": "text/event-stream" } },
    );
  };
  try {
    await watchBatch(
      "batch",
      "secret",
      controller.signal,
      (batch) => {
        snapshots.push(batch.status);
        if (batch.status === "done") controller.abort();
      },
      () => {},
    );
    assert.deepEqual(snapshots, ["pending", "done"]);
    assert.equal(calls, 2);
    globalThis.fetch = async (_url, init) => {
      assert.equal(new Headers(init?.headers).get("Authorization"), null);
      return Response.json({ error: { message: "Sign in again." } }, { status: 401 });
    };
    await assert.rejects(
      watchBatch(
        "batch",
        "",
        new AbortController().signal,
        () => {},
        () => {},
      ),
      /Sign in again/,
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});
