import { useEffect, useState } from "react";
import { request } from "./api";

type Key = { id: string; name: string };

export default function AccountKeys() {
  const [keys, setKeys] = useState<Key[]>([]);
  const [name, setName] = useState("");
  const [secret, setSecret] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function load(signal?: AbortSignal) {
    const { data } = await request<{ data: Key[] }>("/auth/keys", "", { signal });
    setKeys(data);
  }
  useEffect(() => {
    const controller = new AbortController();
    void request<{ data: Key[] }>("/auth/keys", "", { signal: controller.signal })
      .then(({ data }) => {
        if (!controller.signal.aborted) setKeys(data);
      })
      .catch(() => {
        if (!controller.signal.aborted) setError("Could not load API keys.");
      });
    return () => controller.abort();
  }, []);
  async function act(action: () => Promise<void>) {
    setBusy(true);
    setError("");
    setSecret("");
    try {
      await action();
      await load();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not update keys.");
    } finally {
      setBusy(false);
    }
  }
  return (
    <details>
      <summary>API keys for scripts</summary>
      <form
        className="mt-3 space-y-3"
        onSubmit={(event) => {
          event.preventDefault();
          void act(async () => {
            const { data } = await request<{ data: { key: string } }>("/auth/keys", "", {
              method: "POST",
              body: JSON.stringify({ name }),
            });
            setSecret(data.key);
            setName("");
          });
        }}
      >
        <label>
          Key name
          <input
            value={name}
            maxLength={100}
            onChange={(event) => setName(event.target.value)}
            required
          />
        </label>
        <button disabled={busy || !name.trim()}>Create key</button>
      </form>
      {secret && (
        <output className="mt-3 block break-all">
          Copy this key now; it is shown only once: <code>{secret}</code>
        </output>
      )}
      {error && <p role="alert">{error}</p>}
      <ul className="mt-3 space-y-2">
        {keys.map((key) => (
          <li key={key.id}>
            {key.name}{" "}
            <button
              disabled={busy}
              onClick={() =>
                void act(async () => {
                  await request(`/auth/keys/${key.id}`, "", { method: "DELETE" });
                })
              }
            >
              Revoke
            </button>
          </li>
        ))}
      </ul>
    </details>
  );
}
