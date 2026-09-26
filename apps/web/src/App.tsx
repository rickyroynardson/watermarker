import { useEffect, useRef, useState } from "react";
import AccountKeys from "./AccountKeys";
import BatchDetails from "./BatchDetails";
import { createBatch, request } from "./api";
import type { BatchPage, Draft } from "./api";

const accept = "image/jpeg,image/png,image/webp";
const panel = "rounded-xl border border-slate-200 bg-white p-5 sm:p-6";

export default function App() {
  const [apiKey, setApiKey] = useState("");
  const [user, setUser] = useState<{ id: string; name: string }>();
  const [loginUrl, setLoginUrl] = useState("");
  const [checkingAuth, setCheckingAuth] = useState(true);
  const authenticated = !!user || !!apiKey.trim();
  const [watermark, setWatermark] = useState<File>();
  const [sources, setSources] = useState<File[]>([]);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState(() =>
    new URLSearchParams(window.location.search).has("auth_error")
      ? "Sign-in failed or was cancelled. Please try again."
      : "",
  );
  const [created, setCreated] = useState("");
  const [selected, setSelected] = useState("");
  const [page, setPage] = useState<BatchPage>();
  const [limit, setLimit] = useState("20");
  const draft = useRef<Draft | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    void (async () => {
      try {
        const { data } = await request<{ data: { oidc_enabled: boolean; login_url: string } }>(
          "/auth/config",
          "",
          {
            signal: controller.signal,
          },
        );
        setLoginUrl(data.oidc_enabled ? data.login_url : "");
        const response = await fetch("/api/auth/me", {
          signal: AbortSignal.any([controller.signal, AbortSignal.timeout(60_000)]),
          cache: "no-store",
        });
        if (response.ok) setUser((await response.json()).data);
        else if (response.status !== 401)
          throw new Error("Could not check your sign-in. Reload to retry.");
      } catch (cause) {
        if (!controller.signal.aborted)
          setError(cause instanceof Error ? cause.message : "Could not check sign-in.");
      }
    })().finally(() => {
      if (!controller.signal.aborted) setCheckingAuth(false);
    });
    return () => controller.abort();
  }, []);

  function resetDraft() {
    draft.current = null;
    setCreated("");
    setMessage("");
    setError("");
  }

  async function run(action: () => Promise<void>) {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await action();
    } catch (cause) {
      setMessage("");
      setError(cause instanceof Error ? cause.message : "Request failed. Please retry.");
    } finally {
      setBusy(false);
    }
  }

  async function list(cursor = "") {
    const query = new URLSearchParams({ limit, ...(cursor ? { cursor } : {}) });
    const { data } = await request<{ data: BatchPage }>(`/batches?${query}`, apiKey.trim());
    setPage(data);
  }

  return (
    <main className="mx-auto max-w-4xl space-y-6 px-4 py-10 sm:px-6">
      <header>
        <p className="text-xs font-semibold uppercase tracking-widest text-slate-500">
          Image tools
        </p>
        <h1 className="mt-2 text-3xl font-semibold tracking-tight">Watermarker</h1>
        <p className="mt-2 text-slate-600">
          Upload your images, apply a watermark, and keep track of your batches.
        </p>
      </header>

      <section className={panel} aria-labelledby="connection-heading">
        <h2 id="connection-heading">Account</h2>
        {user ? (
          <div className="mt-4 space-y-4">
            <p>Signed in as {user.name}</p>
            <button
              disabled={busy}
              onClick={() =>
                void run(async () => {
                  await request("/auth/logout", "", { method: "POST" });
                  window.location.replace("/");
                })
              }
            >
              Sign out
            </button>
            <AccountKeys />
          </div>
        ) : (
          <fieldset disabled={busy || checkingAuth} className="mt-4 space-y-4">
            {loginUrl && (
              <p>
                <a href={loginUrl} className="font-medium text-blue-700 underline">
                  Sign in
                </a>
              </p>
            )}
            <label>
              {loginUrl ? "Or use an API key" : "API key"}
              <input
                type="password"
                autoComplete="off"
                spellCheck={false}
                value={apiKey}
                placeholder="Enter your API key"
                onChange={(event) => {
                  setApiKey(event.target.value);
                  setPage(undefined);
                  setSelected("");
                  resetDraft();
                }}
              />
            </label>
            <p className="text-xs text-slate-500">
              Your key stays in memory and is cleared on reload.
            </p>
          </fieldset>
        )}
      </section>

      <div aria-live="polite" className="empty:hidden">
        {message && (
          <p className="rounded-lg border border-blue-200 bg-blue-50 p-4 text-sm text-blue-900">
            {message}
          </p>
        )}
      </div>
      {error && (
        <p
          role="alert"
          className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-800"
        >
          {error}
        </p>
      )}

      <section className={panel} aria-labelledby="create-heading">
        <h2 id="create-heading">Create a batch</h2>
        <p className="mt-1 text-sm text-slate-500">JPEG, PNG, or WebP · Up to 10 MiB per image</p>
        <form
          className="mt-5"
          onSubmit={(event) => {
            event.preventDefault();
            void run(async () => {
              if (!watermark) throw new Error("Choose a watermark image.");
              draft.current ??= { idempotencyKey: crypto.randomUUID(), keys: [] };
              const id = await createBatch(
                [watermark, ...sources],
                apiKey.trim(),
                draft.current,
                setMessage,
              );
              setCreated(id);
              setSelected(id);
              setMessage("Batch accepted. Processing status appears below.");
            });
          }}
        >
          <fieldset disabled={busy} className="space-y-5">
            <label>
              Watermark image
              <input
                type="file"
                accept={accept}
                required
                onChange={(event) => {
                  setWatermark(event.target.files?.[0]);
                  resetDraft();
                }}
              />
            </label>
            <label>
              Source images
              <input
                type="file"
                accept={accept}
                multiple
                required
                onChange={(event) => {
                  setSources(Array.from(event.target.files || []));
                  resetDraft();
                }}
              />
            </label>
            {sources.length > 0 && (
              <p className="text-sm text-slate-500">
                {sources.length} source image{sources.length === 1 ? "" : "s"} selected.
              </p>
            )}
            <button
              type="submit"
              disabled={!authenticated || !watermark || !sources.length || !!created}
            >
              {busy ? "Working…" : created ? "Batch created" : "Upload & create batch"}
            </button>
            <p className="text-xs text-slate-500">
              If a request fails, retry with the same files. Successful uploads are reused.
            </p>
          </fieldset>
        </form>
        {created && (
          <p className="mt-4 break-all rounded-lg bg-emerald-50 p-3 text-sm text-emerald-900">
            Batch ID: <code>{created}</code>
            <br />
            Choose new files to start another batch.
          </p>
        )}
      </section>

      {selected && <BatchDetails key={selected + apiKey} id={selected} apiKey={apiKey.trim()} />}

      <section className={panel} aria-labelledby="batches-heading">
        <h2 id="batches-heading">Batch history</h2>
        <fieldset disabled={busy} className="my-4 flex flex-wrap items-end gap-3">
          <label>
            Per page
            <select
              value={limit}
              onChange={(event) => {
                setLimit(event.target.value);
                setPage(undefined);
              }}
            >
              {[10, 20, 50, 100].map((value) => (
                <option key={value}>{value}</option>
              ))}
            </select>
          </label>
          <button disabled={!authenticated} onClick={() => void run(() => list())}>
            {page ? "Refresh / first page" : "Load batches"}
          </button>
          <button
            disabled={!authenticated || !page?.next_cursor}
            onClick={() => void run(() => list(page?.next_cursor || ""))}
          >
            Next page
          </button>
        </fieldset>
        {!page ? (
          <p className="text-sm text-slate-500">Load batches to view your history.</p>
        ) : !page.batches?.length ? (
          <p className="text-sm text-slate-500">No batches found.</p>
        ) : (
          <ul className="divide-y divide-slate-100">
            {page.batches.map((batch) => (
              <li key={batch.id} className="space-y-1 py-4 text-sm">
                <button onClick={() => setSelected(batch.id)} aria-pressed={selected === batch.id}>
                  View results
                </button>
                <p className="break-all font-mono font-medium">{batch.id}</p>
                {batch.expired_at && <p className="text-amber-700">Files expired</p>}
                <p className="text-slate-500">{new Date(batch.created_at).toLocaleString()}</p>
                <p className="break-all text-xs text-slate-500">Watermark: {batch.watermark_key}</p>
              </li>
            ))}
          </ul>
        )}
      </section>
    </main>
  );
}
