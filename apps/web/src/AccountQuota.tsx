import { useEffect, useRef, useState } from "react";
import { request } from "./api";

type Quota = {
  plan: string;
  included_bytes: number;
  addon_bytes: number;
  used_bytes: number;
  demo_enabled: boolean;
};
const mib = (bytes: number) => `${(bytes / 1048576).toFixed(1)} MiB`;

export default function AccountQuota({
  apiKey,
  refreshKey,
}: {
  apiKey: string;
  refreshKey: string;
}) {
  const [quota, setQuota] = useState<Quota>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const pendingAddon = useRef("");
  async function load(signal?: AbortSignal) {
    const { data } = await request<{ data: Quota }>("/account/quota", apiKey, { signal });
    setQuota(data);
  }
  useEffect(() => {
    const controller = new AbortController();
    void request<{ data: Quota }>("/account/quota", apiKey, { signal: controller.signal })
      .then(({ data }) => {
        if (!controller.signal.aborted) setQuota(data);
      })
      .catch(() => {
        if (!controller.signal.aborted) setError("Could not load storage allowance.");
      });
    return () => controller.abort();
  }, [apiKey, refreshKey]);
  async function change(body: { plan: string } | { addon: boolean }) {
    setBusy(true);
    setError("");
    try {
      if ("addon" in body) pendingAddon.current ||= crypto.randomUUID();
      await request("/account/quota/demo", apiKey, {
        headers: "addon" in body ? { "Idempotency-Key": pendingAddon.current } : undefined,
        method: "POST",
        body: JSON.stringify(body),
      });
      if ("addon" in body) pendingAddon.current = "";
      await load();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not update allowance.");
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="mt-4 space-y-3" aria-label="Storage allowance">
      <h3>Storage allowance</h3>
      {quota && (
        <>
          <p className="text-sm">
            {quota.plan === "pro" ? "Pro" : "Free"}: {mib(quota.used_bytes)} reserved /{" "}
            {mib(quota.included_bytes + quota.addon_bytes)}
          </p>
          <p className="text-xs text-slate-500">
            Includes {mib(quota.included_bytes)} from your plan and {mib(quota.addon_bytes)} extra
            storage. Counts source images, watermarks, generated images, and pending upload
            reservations. Space returns after cleanup deletes files.
          </p>
          {quota.demo_enabled && (
            <fieldset disabled={busy} className="flex flex-wrap gap-3">
              <legend className="mb-2 text-sm">
                Demo purchases — no payment or recurring billing
              </legend>
              <button
                disabled={quota.plan === "free"}
                onClick={() => void change({ plan: "free" })}
              >
                Free · 100 MiB
              </button>
              <button disabled={quota.plan === "pro"} onClick={() => void change({ plan: "pro" })}>
                Pro · 1 GiB
              </button>
              <button onClick={() => void change({ addon: true })}>Add 100 MiB</button>
            </fieldset>
          )}
        </>
      )}
      <button
        disabled={busy}
        onClick={() => {
          setError("");
          void load().catch(() => setError("Could not load storage allowance."));
        }}
      >
        Refresh allowance
      </button>
      {error && <p role="alert">{error}</p>}
    </section>
  );
}
