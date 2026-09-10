export type Batch = { id: string; watermark_key: string; created_at: string };
export type BatchPage = { batches: Batch[] | null; next_cursor: string | null };
type Upload = { key: string; url: string; fields: Record<string, string> };

export async function request<T>(path: string, apiKey = "", init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (apiKey) headers.set("Authorization", `Bearer ${apiKey}`);
  if (init.body) headers.set("Content-Type", "application/json");
  const response = await fetch(`/api${path}`, {
    ...init,
    headers,
    signal: AbortSignal.timeout(60_000),
  });
  const body = await response.json().catch(() => null);
  if (!response.ok)
    throw new Error(body?.error?.message || `Request failed (HTTP ${response.status}).`);
  if (!body) throw new Error("The API returned an empty or invalid response.");
  return body;
}

export function validateFiles(files: File[]) {
  for (const file of files) {
    if (!["image/jpeg", "image/png", "image/webp"].includes(file.type)) {
      throw new Error(`${file.name}: choose a JPEG, PNG, or WebP image.`);
    }
    if (file.size < 1 || file.size > 10 * 1024 * 1024) {
      throw new Error(`${file.name}: each image must be between 1 byte and 10 MiB.`);
    }
  }
}

export async function uploadFile(file: File, apiKey: string): Promise<string> {
  validateFiles([file]);
  const { data } = await request<{ data: Upload }>("/uploads/presign", apiKey, {
    method: "POST",
    body: JSON.stringify({ content_type: file.type }),
  });
  const form = new FormData();
  for (const [name, value] of Object.entries(data.fields)) form.append(name, value);
  form.append("file", file);
  let response: Response;
  try {
    response = await fetch(data.url, {
      method: "POST",
      body: form,
      signal: AbortSignal.timeout(120_000),
    });
  } catch (cause) {
    if (!(cause instanceof TypeError)) throw cause;
    throw new Error(
      `${file.name}: could not reach storage or read its upload response. Check the upload endpoint and bucket CORS policy for this page's origin, then retry.`,
      { cause },
    );
  }
  if (!response.ok)
    throw new Error(`${file.name}: storage upload failed (HTTP ${response.status}).`);
  return data.key;
}

// Keep this draft across retries: an accepted batch may have lost its HTTP response.
export type Draft = { idempotencyKey: string; keys: string[] };
export async function createBatch(
  files: File[],
  apiKey: string,
  draft: Draft,
  progress: (message: string) => void,
) {
  if (files.length < 2) throw new Error("Choose a watermark and at least one source image.");
  validateFiles(files);
  for (let i = draft.keys.length; i < files.length; i++) {
    progress(`Uploading ${i + 1} of ${files.length}: ${files[i].name}`);
    draft.keys.push(await uploadFile(files[i], apiKey));
  }
  progress("Creating batch…");
  const { data } = await request<{ data: { id: string } }>("/batches", apiKey, {
    method: "POST",
    headers: { "Idempotency-Key": draft.idempotencyKey },
    body: JSON.stringify({ watermark_key: draft.keys[0], source_keys: draft.keys.slice(1) }),
  });
  return data.id;
}
