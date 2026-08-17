const decoder = new TextDecoder("utf-8", { fatal: true });
const encoder = new TextEncoder();

export function decodeJsonBytes<T = unknown>(bytes: Uint8Array, maximumBytes = 4 * 1024 * 1024): T {
  if (bytes.byteLength > maximumBytes) {
    throw new RangeError(`JSON payload exceeds ${maximumBytes} bytes`);
  }
  if (bytes.byteLength === 0) {
    throw new SyntaxError("JSON payload is empty");
  }
  return JSON.parse(decoder.decode(bytes)) as T;
}

export function encodeJsonBytes(value: unknown, maximumBytes = 4 * 1024 * 1024): Uint8Array {
  const bytes = encoder.encode(JSON.stringify(value));
  if (bytes.byteLength > maximumBytes) {
    throw new RangeError(`JSON payload exceeds ${maximumBytes} bytes`);
  }
  return bytes;
}
