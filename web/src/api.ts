export class ApiError extends Error {
  constructor(message: string, public status: number, public code?: string) { super(message) }
}

export async function api<T>(path: string, body?: unknown, method?: 'GET' | 'POST' | 'DELETE'): Promise<T> {
  const response = await fetch(path, {
    method: method || (body === undefined ? 'GET' : 'POST'),
    credentials: 'same-origin',
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const data = await response.json().catch(() => ({ error: '服务暂时不可用，请稍后重试' }))
  if (!response.ok) throw new ApiError(data.error || '操作没有成功，请再试一次', response.status, data.code)
  return data as T
}
