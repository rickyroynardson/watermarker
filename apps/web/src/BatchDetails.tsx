import { useEffect, useState } from "react";
import { request } from "./api";
import type { BatchDetails as Details } from "./api";

export default function BatchDetails({ id, apiKey }: { id: string; apiKey: string }) {
  const [batch, setBatch] = useState<Details>();
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [cancelling, setCancelling] = useState(false);
  const [retrying, setRetrying] = useState<string[]>([]);
  const [retryError, setRetryError] = useState("");
  const [loading, setLoading] = useState(true);
  const [previewErrors, setPreviewErrors] = useState<string[]>([]);

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    async function load() {
      setLoading(true);
      try {
        const { data } = await request<{ data: Details }>(`/batches/${id}`, apiKey, {
          signal: controller.signal,
        });
        if (controller.signal.aborted) return;
        setBatch(data);
        setError("");
        setPreviewErrors([]);
        // Renew completed links before their 15-minute expiry; pending batches poll faster.
        timer = setTimeout(() => void load(), data.status === "pending" ? 3000 : 600_000);
      } catch (cause) {
        if (!controller.signal.aborted) {
          setError(cause instanceof Error ? cause.message : "Could not load batch.");
        }
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    }
    void load();
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [id, apiKey, refresh]);

  async function retry(image: Details["images"][number]) {
    setRetrying((ids) => [...ids, image.id]);
    setRetryError("");
    try {
      await request(`/batches/${id}/images/${image.id}/retry`, apiKey, {
        method: "POST",
        body: JSON.stringify({ attempt: image.attempt }),
      });
      setRefresh((value) => value + 1);
    } catch (cause) {
      setRetryError(cause instanceof Error ? cause.message : "Could not retry image.");
    } finally {
      setRetrying((ids) => ids.filter((value) => value !== image.id));
    }
  }

  async function cancelBatch() {
    setCancelling(true);
    setRetryError("");
    try {
      await request(`/batches/${id}/cancel`, apiKey, { method: "POST" });
      setRefresh((value) => value + 1);
    } catch (cause) {
      setRetryError(cause instanceof Error ? cause.message : "Could not cancel batch.");
    } finally {
      setCancelling(false);
    }
  }

  const cancelled = batch?.images.filter((image) => image.status === "cancelled").length || 0;
  const done = batch?.images.filter((image) => image.status === "done").length || 0;
  const failed = batch?.images.filter((image) => image.status === "failed").length || 0;
  return (
    <section
      className="rounded-xl border border-slate-200 bg-white p-5 sm:p-6"
      aria-labelledby="results-heading"
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 id="results-heading">Batch results</h2>
        {batch && (batch.status === "pending" || batch.images.some((image) => image.retryable)) && (
          <button
            disabled={loading || cancelling || retrying.length > 0}
            onClick={() => void cancelBatch()}
          >
            {cancelling ? "Cancelling…" : "Cancel remaining work"}
          </button>
        )}
        <button disabled={loading} onClick={() => setRefresh((value) => value + 1)}>
          {loading ? "Refreshing…" : "Refresh results & links"}
        </button>
      </div>
      <p className="mt-2 break-all font-mono text-xs text-slate-500">{id}</p>
      {error && (
        <p role="alert" className="mt-3 text-sm text-red-700">
          {error} Use Refresh to retry.
        </p>
      )}
      {retryError && (
        <p role="alert" className="mt-3 text-sm text-red-700">
          {retryError}
        </p>
      )}
      <div aria-live="polite" className="my-4 text-sm">
        {batch ? (
          <>
            <p className="font-medium">
              {batch.status === "expired"
                ? "Expired"
                : batch.status === "pending"
                  ? "Queued / processing"
                  : batch.status === "failed"
                    ? "Finished with errors"
                    : batch.status === "cancelled"
                      ? "Cancelled"
                      : "Completed"}
            </p>
            <p className="text-slate-600">
              {done} of {batch.images.length} completed · {failed} failed · {cancelled} cancelled ·{" "}
              {batch.images.length - done - failed - cancelled} pending
            </p>
            {batch.duration_seconds != null && (
              <p className="text-slate-600">
                Finished in {batch.duration_seconds.toFixed(1)} seconds after submission (excludes
                uploads).
              </p>
            )}
            <progress
              className="mt-2 w-full accent-slate-900"
              aria-label="Images finished processing"
              value={done + failed + cancelled}
              max={Math.max(batch.images.length, 1)}
            />
          </>
        ) : (
          <p>{error ? "Batch details unavailable." : "Loading batch…"}</p>
        )}
      </div>
      {batch?.expired_at && (
        <p className="mb-4 text-sm text-slate-600">
          Files have expired under the retention policy. Upload your files in a new batch to process
          them again.
        </p>
      )}
      <ul className="grid gap-4 sm:grid-cols-2">
        {batch?.images.map((image, index) => (
          <li key={image.id} className="min-w-0 rounded-lg border border-slate-200 p-4">
            <p className="font-medium">
              Image {index + 1} ·{" "}
              {batch.status === "expired"
                ? "File expired"
                : image.status === "pending"
                  ? "Queued / processing"
                  : image.status === "done"
                    ? "Completed"
                    : image.status === "cancelled"
                      ? "Cancelled"
                      : image.retryable
                        ? "Needs attention"
                        : "Failed"}
            </p>
            <p className="mt-1 break-all font-mono text-xs text-slate-500">{image.id}</p>
            {image.error && <p className="mt-3 break-words text-sm text-red-700">{image.error}</p>}
            {batch.status !== "expired" &&
              image.status === "failed" &&
              (image.retryable ? (
                <button
                  className="mt-3"
                  disabled={loading || cancelling || retrying.includes(image.id)}
                  onClick={() => void retry(image)}
                >
                  {retrying.includes(image.id) ? "Retrying…" : "Retry image"}
                </button>
              ) : (
                <p className="mt-3 text-sm text-slate-600">
                  Upload a corrected image or watermark in a new batch.
                </p>
              ))}
            {image.preview_url && (
              <>
                {previewErrors.includes(image.id) && (
                  <p className="mt-3 text-sm text-amber-800">
                    Preview unavailable. Refresh the links and check storage access.
                  </p>
                )}
                <img
                  key={image.preview_url}
                  src={image.preview_url}
                  alt={`Watermarked result ${index + 1}`}
                  loading="lazy"
                  referrerPolicy="no-referrer"
                  className="mt-3 max-h-72 w-full rounded bg-slate-100 object-contain"
                  onError={() =>
                    setPreviewErrors((ids) => (ids.includes(image.id) ? ids : [...ids, image.id]))
                  }
                />
              </>
            )}
            {image.download_url && (
              <a
                className="mt-3 inline-block rounded-lg bg-slate-900 px-4 py-2 text-sm font-medium text-white focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600"
                href={image.download_url}
                referrerPolicy="no-referrer"
              >
                Download image {index + 1}
              </a>
            )}
          </li>
        ))}
      </ul>
      <p className="mt-4 text-xs text-slate-500">
        Pending batches update every 3 seconds. Download links expire after 15 minutes and refresh
        automatically while this panel is open.
      </p>
    </section>
  );
}
