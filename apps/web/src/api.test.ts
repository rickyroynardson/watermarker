import assert from "node:assert/strict";
import { test } from "node:test";
import { createBatch, request, validateFiles } from "./api.ts";

test("upload contract, safe batch retry, validation, and API errors", async () => {
  const originalFetch = globalThis.fetch;
  const files = [
    new File(["watermark"], "mark.png", { type: "image/png" }),
    new File(["source"], "source.jpg", { type: "image/jpeg" }),
  ];
  assert.throws(() => validateFiles([new File([], "empty.png", { type: "image/png" })]), /1 byte/);
  assert.throws(() => validateFiles([new File(["x"], "bad.gif", { type: "image/gif" })]), /JPEG/);
  assert.throws(
    () =>
      validateFiles([
        new File([new Uint8Array(10 * 1024 * 1024 + 1)], "large.png", { type: "image/png" }),
      ]),
    /10 MiB/,
  );
  let presigns = 0;
  let uploads = 0;
  const submissions: RequestInit[] = [];
  globalThis.fetch = async (url, init = {}) => {
    if (url === "/api/uploads/presign") {
      assert.equal(new Headers(init.headers).get("Authorization"), "Bearer secret");
      return Response.json({
        data: {
          key: `uploads/key/${++presigns}`,
          url: "https://storage.example/upload",
          fields: {
            policy: "unchanged",
            key: `uploads/key/${presigns}`,
            "Content-Type": files[presigns - 1].type,
          },
        },
      });
    }
    if (url === "https://storage.example/upload") {
      assert.equal(init.method, "POST");
      assert.equal(init.headers, undefined, "never send API credentials to storage");
      const form = init.body as FormData;
      assert.deepEqual([...form.keys()], ["policy", "key", "Content-Type", "file"]);
      assert.equal(form.get("policy"), "unchanged");
      assert.equal(form.get("Content-Type"), files[uploads].type);
      uploads++;
      return new Response(null, { status: 204 });
    }
    assert.equal(url, "/api/batches");
    submissions.push(init);
    if (submissions.length === 1) throw new TypeError("Lost response");
    return Response.json({ data: { id: "batch-id" } }, { status: 201 });
  };
  try {
    const draft = { idempotencyKey: "stable-key", keys: [] };
    await assert.rejects(
      createBatch(files, "secret", draft, () => {}),
      /Lost response/,
    );
    assert.equal(await createBatch(files, "secret", draft, () => {}), "batch-id");
    assert.equal(presigns, 2);
    assert.equal(uploads, 2);
    assert.equal(submissions[0].body, submissions[1].body);
    assert.deepEqual(JSON.parse(submissions[1].body as string), {
      watermark_key: "uploads/key/1",
      source_keys: ["uploads/key/2"],
    });
    for (const submission of submissions)
      assert.equal(new Headers(submission.headers).get("Idempotency-Key"), "stable-key");
    const failedDraft = { idempotencyKey: "upload-retry", keys: [] };
    let uploadFails = true;
    let batchCalls = 0;
    globalThis.fetch = async (url) => {
      if (url === "/api/uploads/presign")
        return Response.json({
          data: {
            key: `uploads/retry/${failedDraft.keys.length}`,
            url: "https://storage.example/upload",
            fields: { "Content-Type": "image/png" },
          },
        });
      if (url === "https://storage.example/upload") {
        if (uploadFails) throw new TypeError("Failed to fetch");
        return new Response(null, { status: 204 });
      }
      batchCalls++;
      return Response.json({ data: { id: "retried-batch" } });
    };
    await assert.rejects(
      createBatch(files, "secret", failedDraft, () => {}),
      /bucket CORS/,
    );
    assert.deepEqual(failedDraft.keys, []);
    assert.equal(batchCalls, 0);
    uploadFails = false;
    assert.equal(await createBatch(files, "secret", failedDraft, () => {}), "retried-batch");
    assert.equal(batchCalls, 1);
    globalThis.fetch = async () =>
      Response.json({ error: { message: "Invalid or revoked API key." } }, { status: 401 });
    await assert.rejects(request("/batches", "bad"), /Invalid or revoked/);
    globalThis.fetch = async () => new Response("proxy unavailable", { status: 502 });
    await assert.rejects(request("/ping"), /HTTP 502/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
