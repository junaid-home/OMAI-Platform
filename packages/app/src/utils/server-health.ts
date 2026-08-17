import { healthAt } from "@/omai/client"
import { ServerConnection } from "@/context/server"
import { Accessor, createEffect, onCleanup } from "solid-js"
import { createStore, reconcile } from "solid-js/store"

export type ServerHealth = { healthy: boolean; version?: string }

interface CheckServerHealthOptions {
  timeoutMs?: number
  signal?: AbortSignal
  retryCount?: number
  retryDelayMs?: number
}

const defaultTimeoutMs = 30_000
const defaultRetryCount = 2
const defaultRetryDelayMs = 100
const cacheMs = 750
const healthCache = new Map<string, { at: number; done: boolean; promise: Promise<ServerHealth> }>()

function timeoutSignal(timeoutMs: number) {
  const timeout = (AbortSignal as unknown as { timeout?: (ms: number) => AbortSignal }).timeout
  if (timeout) {
    try {
      return {
        signal: timeout.call(AbortSignal, timeoutMs),
        clear: undefined as (() => void) | undefined,
      }
    } catch {}
  }
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeoutMs)
  return { signal: controller.signal, clear: () => clearTimeout(timer) }
}

function wait(ms: number, signal?: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("Aborted", "AbortError"))
      return
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort)
      resolve()
    }, ms)
    const onAbort = () => {
      clearTimeout(timer)
      reject(new DOMException("Aborted", "AbortError"))
    }
    signal?.addEventListener("abort", onAbort, { once: true })
  })
}

function retryable(error: unknown, signal?: AbortSignal) {
  if (signal?.aborted) return false
  if (!(error instanceof Error)) return false
  if (error.name === "AbortError" || error.name === "TimeoutError") return false
  if (error instanceof TypeError) return true
  return /network|fetch|econnreset|econnrefused|enotfound|timedout/i.test(error.message)
}

export async function checkServerHealth(
  server: ServerConnection.HttpBase = { url: "/" },
  fetch?: typeof globalThis.fetch,
  opts?: CheckServerHealthOptions,
): Promise<ServerHealth> {
  const timeout = opts?.signal ? undefined : timeoutSignal(opts?.timeoutMs ?? defaultTimeoutMs)
  const signal = opts?.signal ?? timeout?.signal
  const retryCount = opts?.retryCount ?? defaultRetryCount
  const retryDelayMs = opts?.retryDelayMs ?? defaultRetryDelayMs
  const next = (count: number, error: unknown) => {
    if (count >= retryCount || !retryable(error, signal)) return Promise.resolve({ healthy: false } as const)
    return wait(retryDelayMs * (count + 1), signal)
      .then(() => attempt(count + 1))
      .catch(() => ({ healthy: false }))
  }
  const attempt = (count: number): Promise<ServerHealth> =>
    healthAt(server.url, fetch, { signal })
      .then((x) => ({ healthy: x.ok === true, version: x.version || undefined }))
      .catch((error) => next(count, error))
  return attempt(0).finally(() => timeout?.clear?.())
}

const pollMs = 10_000

export function useCheckServerHealth() {
  return (_http: ServerConnection.HttpBase) => {
    const key = "rpc"
    const hit = healthCache.get(key)
    const now = Date.now()
    if (hit && (!hit.done || now - hit.at < cacheMs)) return hit.promise
    const promise = checkServerHealth(_http).finally(() => {
      const next = healthCache.get(key)
      if (!next || next.promise !== promise) return
      next.done = true
      next.at = Date.now()
    })
    healthCache.set(key, { at: now, done: false, promise })
    return promise
  }
}

export const useServerHealth = (servers: Accessor<ServerConnection.Any[]>, enabled: Accessor<boolean>) => {
  const checkServerHealth = useCheckServerHealth()
  const [status, setStatus] = createStore({} as Record<ServerConnection.Key, ServerHealth | undefined>)

  createEffect(() => {
    if (!enabled()) {
      setStatus(reconcile({}))
      return
    }
    const list = servers()
    let dead = false

    const refresh = async () => {
      const results: Record<string, ServerHealth> = {}
      await Promise.all(
        list.map(async (conn) => {
          const key = ServerConnection.key(conn)
          const result = await checkServerHealth(conn.http)
          results[key] = result
          if (!dead) setStatus(key, result)
        }),
      )
      if (dead) return
      setStatus(reconcile(results))
    }

    void refresh()
    const id = setInterval(() => void refresh(), pollMs)
    onCleanup(() => {
      dead = true
      clearInterval(id)
    })
  })

  return status
}
