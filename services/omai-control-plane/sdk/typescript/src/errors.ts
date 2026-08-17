import { Code, ConnectError } from "@connectrpc/connect";

const retryableCodes = new Set<Code>([
  Code.Aborted,
  Code.DeadlineExceeded,
  Code.ResourceExhausted,
  Code.Unavailable,
]);

/** Stable application-facing representation of a Connect failure. */
export class OMAIError extends Error {
  readonly code: Code;
  readonly metadata: Headers;
  readonly retryable: boolean;

  constructor(message: string, options: {
    code: Code;
    metadata?: Headers;
    cause?: unknown;
  }) {
    super(message, { cause: options.cause });
    this.name = "OMAIError";
    this.code = options.code;
    this.metadata = new Headers(options.metadata);
    this.retryable = retryableCodes.has(options.code);
  }
}

/** Convert an unknown thrown value without discarding Connect metadata. */
export function asOMAIError(error: unknown): OMAIError {
  if (error instanceof OMAIError) {
    return error;
  }
  const connected = ConnectError.from(error);
  return new OMAIError(connected.rawMessage || connected.message, {
    code: connected.code,
    metadata: connected.metadata,
    cause: error,
  });
}
