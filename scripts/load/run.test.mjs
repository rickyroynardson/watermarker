import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, writeFile, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import { run } from "./run.mjs";

test("load runs preserve workload and save authoritative results and failures", async () => {
  const dir = await mkdtemp(join(tmpdir(), "watermarker-load-"));
  const original = globalThis.fetch;
  try {
    const fixture = join(dir, "fixture.png"),
      output = join(dir, "results.json");
    await writeFile(fixture, Buffer.from("fixture"));
    let uploads = 0,
      creates = 0,
      failed = false;
    globalThis.fetch = async (url, init = {}) => {
      if (url.endsWith("/uploads/presign"))
        return Response.json({
          data: {
            key: String(++uploads),
            url: "http://storage/upload",
            fields: { policy: "test" },
          },
        });
      if (url === "http://storage/upload") {
        assert.ok(init.body instanceof FormData);
        assert.equal(init.headers, undefined, "API credentials must not reach storage");
        return new Response("", { status: 200 });
      }
      if (url.endsWith("/batches")) {
        creates++;
        assert.equal(JSON.parse(init.body).source_keys.length, 2);
        assert.ok(init.headers["Idempotency-Key"]);
        return Response.json({ data: { id: "batch" } });
      }
      return Response.json({
        data: {
          status: failed ? "failed" : "done",
          duration_seconds: 2,
          images: [{ status: "done" }, { status: failed ? "failed" : "done" }],
        },
      });
    };
    const options = {
      api: "http://api",
      key: "private",
      source: fixture,
      watermark: fixture,
      images: 2,
      workers: 1,
      runs: 2,
      timeout: 10,
      output,
    };
    const report = await run(options);
    assert.equal(uploads, 6);
    assert.equal(creates, 2);
    assert.equal(report.runs[0].images_per_second, 1);
    assert.notEqual(report.runs[0].idempotency_key, report.runs[1].idempotency_key);
    assert.ok(!(await readFile(output, "utf8")).includes("private"));
    const second = join(dir, "second.json");
    await writeFile(
      second,
      JSON.stringify({
        ...report,
        workers: 2,
        runs: report.runs.map((r) => ({ ...r, duration_seconds: 1, images_per_second: 2 })),
      }),
    );
    const comparison = execFileSync(
      process.execPath,
      ["scripts/load/compare.mjs", output, second],
      { encoding: "utf8" },
    );
    assert.match(comparison, /2.00/);
    failed = true;
    await assert.rejects(run(options), /failures/);
    assert.ok(JSON.parse(await readFile(output, "utf8")).runs[0].error);
  } finally {
    globalThis.fetch = original;
    await rm(dir, { recursive: true });
  }
});
