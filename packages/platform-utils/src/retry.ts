export interface RetryOptions {
  attempts?: number
  delay?: number
  factor?: number
  maxDelay?: number
  retryIf?: (error: unknown) => boolean
}

const transientMessages = [
  "load failed",
  "network connection was lost",
  "network request failed",
  "failed to fetch",
  "econnreset",
  "econnrefused",
  "etimedout",
  "socket hang up",
]

function isTransientError(error: unknown) {
  if (!error) return false
  const message = String(error instanceof Error ? error.message : error).toLowerCase()
  return transientMessages.some((candidate) => message.includes(candidate))
}

export async function retry<T>(operation: () => Promise<T>, options: RetryOptions = {}): Promise<T> {
  const { attempts = 3, delay = 500, factor = 2, maxDelay = 10_000, retryIf = isTransientError } = options

  let lastError: unknown
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      return await operation()
    } catch (error) {
      lastError = error
      if (attempt === attempts - 1 || !retryIf(error)) throw error
      await new Promise((resolve) => setTimeout(resolve, Math.min(delay * factor ** attempt, maxDelay)))
    }
  }
  throw lastError
}
