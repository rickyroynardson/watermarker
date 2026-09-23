import { readFile, writeFile } from "node:fs/promises";
import { createHash, randomUUID } from "node:crypto";
import { parseArgs } from "node:util";
import { pathToFileURL } from "node:url";
import { setTimeout as sleep } from "node:timers/promises";

// Runs use API state as the authoritative result; telemetry remains best effort.
export async function run({ api, key, source, watermark, images, workers, runs, timeout, output }) {
  const fixture = async (path) => {
    const data = await readFile(path);
    const type = { png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", webp: "image/webp" }[
      path.split(".").pop().toLowerCase()
    ];
    if (!type || !data.length || data.length > 10 * 1024 * 1024)
      throw Error("Use PNG/JPEG/WebP fixtures between 1 byte and 10 MiB.");
    return { data, type, sha256: createHash("sha256").update(data).digest("hex") };
  };
  const src = await fixture(source),
    mark = await fixture(watermark);
  const report = {
    workers,
    images,
    fixtures: { source: src.sha256, watermark: mark.sha256 },
    runs: [],
  };
  const request = async (path, body, idem) => {
    const response = await fetch(api.replace(/\/$/, "") + path, {
      method: body ? "POST" : "GET",
      headers: {
        Authorization: `Bearer ${key}`,
        ...(body && { "Content-Type": "application/json" }),
        ...(idem && { "Idempotency-Key": idem }),
      },
      body: body && JSON.stringify(body),
      signal: AbortSignal.timeout(60_000),
    });
    if (!response.ok) throw Error(`API ${path}: HTTP ${response.status}`);
    return (await response.json()).data;
  };
  const upload = async (file) => {
    const signed = await request("/uploads/presign", { content_type: file.type });
    const form = new FormData();
    for (const [k, v] of Object.entries(signed.fields)) form.append(k, v);
    form.append("file", new Blob([file.data], { type: file.type }), "fixture");
    const response = await fetch(signed.url, {
      method: "POST",
      body: form,
      signal: AbortSignal.timeout(120_000),
    });
    if (!response.ok) throw Error(`Upload: HTTP ${response.status}`);
    return signed.key;
  };
  for (let n = 0; n < runs; n++) {
    const row = { run: n + 1, idempotency_key: randomUUID(), started_at: new Date().toISOString() };
    report.runs.push(row);
    await writeFile(output, JSON.stringify(report, null, 2) + "\n");
    try {
      const watermark_key = await upload(mark),
        source_keys = [];
      for (let i = 0; i < images; i++) source_keys.push(await upload(src));
      row.submitted_at = new Date().toISOString();
      const batch = await request("/batches", { watermark_key, source_keys }, row.idempotency_key);
      row.batch_id = batch.id;
      await writeFile(output, JSON.stringify(report, null, 2) + "\n");
      const deadline = Date.now() + timeout * 1000;
      let details;
      while (true) {
        details = await request(`/batches/${batch.id}`);
        if (details.status !== "pending") break;
        if (Date.now() >= deadline)
          throw Error(
            "Batch timeout; jobs remain queued or processing. Do not start another comparison run until drained.",
          );
        await sleep(1000);
      }
      row.finished_at = new Date().toISOString();
      row.status = details.status;
      row.duration_seconds = details.duration_seconds;
      row.done = details.images.filter((i) => i.status === "done").length;
      row.failed = details.images.filter((i) => i.status === "failed").length;
      row.images_per_second = row.duration_seconds > 0 ? row.done / row.duration_seconds : null;
      row.grafana = `http://localhost:3000/d/watermarker-performance?from=${Date.parse(row.submitted_at) - 60000}&to=${Date.now() + 60000}`;
      if (details.status !== "done") throw Error("Batch contains failures; stopping comparison.");
    } catch (error) {
      row.error = error.message;
      throw error;
    } finally {
      await writeFile(output, JSON.stringify(report, null, 2) + "\n");
    }
    console.log(JSON.stringify(row));
  }
  return report;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const { values } = parseArgs({
    options: {
      api: { type: "string", default: "http://localhost:8080" },
      source: { type: "string" },
      watermark: { type: "string" },
      images: { type: "string", default: "50" },
      workers: { type: "string" },
      runs: { type: "string", default: "3" },
      timeout: { type: "string", default: "1200" },
      output: { type: "string", default: "load-results.json" },
    },
  });
  for (const name of ["images", "workers", "runs", "timeout"]) {
    values[name] = Number(values[name]);
    if (!Number.isSafeInteger(values[name]) || values[name] < 1)
      throw Error(`--${name} must be a positive integer`);
  }
  if (!values.source || !values.watermark || !process.env.WATERMARKER_API_KEY)
    throw Error("Provide --source, --watermark and WATERMARKER_API_KEY.");
  await run({ ...values, key: process.env.WATERMARKER_API_KEY });
}
