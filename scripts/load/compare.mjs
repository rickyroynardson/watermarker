import { readFile } from "node:fs/promises";
const paths = process.argv.slice(2);
if (paths.length < 2) throw Error("Provide at least two load-result JSON files.");
const reports = await Promise.all(
  paths.map(async (path) => JSON.parse(await readFile(path, "utf8"))),
);
const first = reports[0];
for (const r of reports) {
  if (r.images !== first.images || JSON.stringify(r.fixtures) !== JSON.stringify(first.fixtures))
    throw Error("Comparison requires identical fixtures and image counts.");
  if (
    !r.runs.length ||
    r.runs.some((x) => x.error || x.status !== "done" || !(x.duration_seconds > 0))
  )
    throw Error("Comparison requires completed, successful runs.");
}
const median = (values) => {
  const sorted = values.toSorted((a, b) => a - b),
    i = Math.floor(sorted.length / 2);
  return sorted.length % 2 ? sorted[i] : (sorted[i - 1] + sorted[i]) / 2;
};
const baseline = median(first.runs.map((x) => x.duration_seconds));
console.table(
  reports.map((r, i) => ({
    file: paths[i],
    workers: r.workers,
    runs: r.runs.length,
    median_seconds: median(r.runs.map((x) => x.duration_seconds)).toFixed(3),
    median_images_per_second: median(r.runs.map((x) => x.images_per_second)).toFixed(3),
    speedup: (baseline / median(r.runs.map((x) => x.duration_seconds))).toFixed(2),
  })),
);
