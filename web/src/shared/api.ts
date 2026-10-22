export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

/** Calls the admin API with the CSRF header. */
export async function api<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'X-Requested-With': 'cf-portal', Accept: 'application/json' };
  let payload: BodyInit | undefined;
  if (body instanceof FormData) {
    payload = body;
  } else if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    payload = JSON.stringify(body);
  }
  let res: Response;
  try {
    res = await fetch(path, { method, headers, body: payload, credentials: 'same-origin' });
  } catch {
    throw new ApiError(0, '無法連線至伺服器，請確認網路後再試。');
  }
  if (res.status === 401 && path.startsWith('/api/')) {
    window.location.href = '/login';
    throw new ApiError(401, '登入已逾時，請重新登入。');
  }
  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  if (!res.ok) {
    const msg = (data && typeof data === 'object' && 'error' in data ? String((data as { error: string }).error) : '') || `HTTP ${res.status}`;
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

export function errMsg(e: unknown) {
  return e instanceof Error ? e.message : String(e);
}

/** Reads the JSON the server injected into the page. */
export function readInit<T>(): T {
  const el = document.getElementById('cfp-init');
  if (!el?.textContent) return {} as T;
  try {
    return JSON.parse(el.textContent) as T;
  } catch {
    return {} as T;
  }
}
