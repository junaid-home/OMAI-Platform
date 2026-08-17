import type { CallOptions, Client } from "@connectrpc/connect";
import type { VoiceControlService } from "./gen/uab/v1/voice_pb.js";

export interface VoiceConnectInput {
  readonly workspaceId: string;
  readonly locale?: string;
  readonly voice?: string;
  readonly gatewayUrl?: string;
  readonly readyTimeoutMs?: number;
  readonly signal?: AbortSignal;
}

export interface VoiceReadyEvent {
  readonly type: "ready";
  readonly sessionId: string;
  readonly provider: string;
  readonly model: string;
  readonly registryEtag: string;
  readonly inputSampleRateHz: number;
  readonly outputSampleRateHz: number;
}

export interface VoiceTranscriptEvent {
  readonly type: "transcript";
  readonly role: "user" | "assistant";
  readonly transcript: string;
}

export interface VoiceUICommandEvent {
  readonly type: "ui_command";
  readonly requestId: string;
  readonly tool: string;
  readonly action: string;
  readonly timeoutMs: number;
  readonly payload: Readonly<Record<string, unknown>>;
}

export interface VoiceConfirmationEvent {
  readonly type: "confirmation_required";
  readonly requestId: string;
  readonly tool: string;
  readonly message: string;
}

export interface VoiceToolResultEvent {
  readonly type: "tool_result";
  readonly requestId: string;
  readonly tool: string;
  readonly payload: unknown;
}

export interface VoiceAudioEvent {
  readonly type: "audio";
  readonly data: ArrayBuffer;
}

export type VoiceEvent =
  | VoiceReadyEvent
  | VoiceTranscriptEvent
  | VoiceUICommandEvent
  | VoiceConfirmationEvent
  | VoiceToolResultEvent
  | VoiceAudioEvent
  | { readonly type: "interrupted" }
  | { readonly type: "turn_complete" }
  | { readonly type: "pong" }
  | { readonly type: "error"; readonly message: string };

export interface VoiceUIResult {
  readonly requestId: string;
  readonly success: boolean;
  readonly code?: string;
  readonly message?: string;
  readonly payload?: Readonly<Record<string, unknown>>;
}

export interface OMAIVoice {
  connect(input: VoiceConnectInput, options?: CallOptions): Promise<OMAIVoiceSession>;
}

export type WebSocketFactory = (url: string) => WebSocket;

export interface VoiceTransportOptions {
  readonly gatewayUrl?: string;
  readonly webSocketFactory?: WebSocketFactory;
  readonly allowInsecureTransport?: boolean;
}

export class VoiceProtocolError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "VoiceProtocolError";
  }
}

export class VoiceSessionClosedError extends Error {
  readonly code: number;
  readonly reason: string;

  constructor(code: number, reason: string) {
    super(`OMAI voice session closed (${code})${reason === "" ? "" : `: ${reason}`}`);
    this.name = "VoiceSessionClosedError";
    this.code = code;
    this.reason = reason;
  }
}

export class OMAIVoiceSession implements AsyncIterable<VoiceEvent> {
  readonly ready: Promise<VoiceReadyEvent>;
  readonly events: AsyncIterable<VoiceEvent>;

  #socket: WebSocket;
  #queue = new AsyncEventQueue<VoiceEvent>(256);
  #resolveReady!: (event: VoiceReadyEvent) => void;
  #rejectReady!: (error: unknown) => void;
  #readySettled = false;
  #closed = false;
  #readyTimer: ReturnType<typeof setTimeout>;
  #abortSignal: AbortSignal | undefined;
  #abortListener: (() => void) | undefined;

  constructor(socket: WebSocket, readyTimeoutMs: number, signal?: AbortSignal) {
    this.#socket = socket;
    this.events = this.#queue;
    this.ready = new Promise<VoiceReadyEvent>((resolve, reject) => {
      this.#resolveReady = resolve;
      this.#rejectReady = reject;
    });
    this.#readyTimer = setTimeout(() => {
      this.#fail(new VoiceProtocolError("Timed out waiting for the OMAI voice ready message"));
    }, readyTimeoutMs);
    this.#abortSignal = signal;
    this.#abortListener = signal === undefined ? undefined : () => this.close(1_000, "aborted");

    socket.binaryType = "arraybuffer";
    socket.addEventListener("message", this.#onMessage);
    socket.addEventListener("error", this.#onError);
    socket.addEventListener("close", this.#onClose);
    if (this.#abortListener !== undefined) {
      signal?.addEventListener("abort", this.#abortListener, { once: true });
    }
    if (signal?.aborted === true) {
      this.close(1_000, "aborted");
    }
  }

  [Symbol.asyncIterator](): AsyncIterator<VoiceEvent> {
    return this.#queue[Symbol.asyncIterator]();
  }

  sendAudio(frame: ArrayBuffer | ArrayBufferView): void {
    this.#requireOpen();
    const bytes = frame instanceof ArrayBuffer
      ? new Uint8Array(frame)
      : new Uint8Array(frame.buffer, frame.byteOffset, frame.byteLength);
    if (bytes.byteLength === 0 || bytes.byteLength > 64 * 1024) {
      throw new RangeError("Voice audio frames must contain between 1 and 65536 bytes");
    }
    const owned = new Uint8Array(bytes.byteLength);
    owned.set(bytes);
    this.#socket.send(owned.buffer);
  }

  interrupt(): void {
    this.#sendControl({ type: "interrupt" });
  }

  confirm(requestId: string, confirmed: boolean): void {
    this.#sendControl({ type: "confirm", request_id: checkedRequestId(requestId), confirmed });
  }

  acknowledgeUI(result: VoiceUIResult): void {
    const message: Record<string, unknown> = {
      type: "ui_result",
      request_id: checkedRequestId(result.requestId),
      success: result.success,
    };
    if (result.code !== undefined) {
      message.code = checkedCode(result.code);
    }
    if (result.message !== undefined) {
      message.message = boundedText(result.message, 2_048, "UI result message");
    }
    if (result.payload !== undefined) {
      message.payload = result.payload;
    }
    this.#sendControl(message);
  }

  ping(): void {
    this.#sendControl({ type: "ping" });
  }

  close(code = 1_000, reason = "client closed"): void {
    if (this.#closed) {
      return;
    }
    this.#closed = true;
    clearTimeout(this.#readyTimer);
    this.#detach();
    if (!this.#readySettled) {
      this.#readySettled = true;
      this.#rejectReady(new VoiceSessionClosedError(code, reason));
    }
    this.#queue.close();
    if (this.#socket.readyState === 0 || this.#socket.readyState === 1) {
      this.#socket.close(code, boundedText(reason, 123, "WebSocket close reason"));
    }
  }

  #onMessage = (event: MessageEvent<unknown>): void => {
    try {
      const parsed = typeof event.data === "string"
        ? parseServerMessage(event.data)
        : parseAudioFrame(event.data);
      if (parsed.type === "ready" && !this.#readySettled) {
        this.#readySettled = true;
        clearTimeout(this.#readyTimer);
        this.#resolveReady(parsed);
      }
      if (!this.#queue.push(parsed)) {
        this.#fail(new VoiceProtocolError("Voice event queue exceeded its bounded capacity"));
      }
    } catch (error) {
      this.#fail(error);
    }
  };

  #onError = (): void => {
    this.#fail(new VoiceProtocolError("OMAI voice WebSocket transport failed"));
  };

  #onClose = (event: CloseEvent): void => {
    if (this.#closed) {
      return;
    }
    this.#closed = true;
    clearTimeout(this.#readyTimer);
    this.#detach();
    const error = event.code === 1_000 ? undefined : new VoiceSessionClosedError(event.code, event.reason);
    if (!this.#readySettled) {
      this.#readySettled = true;
      this.#rejectReady(error ?? new VoiceSessionClosedError(event.code, event.reason));
    }
    this.#queue.close(error);
  };

  #sendControl(value: Readonly<Record<string, unknown>>): void {
    this.#requireOpen();
    const serialized = JSON.stringify(value);
    if (serialized.length > 128 * 1024) {
      throw new RangeError("Voice control message exceeds 128 KiB");
    }
    this.#socket.send(serialized);
  }

  #requireOpen(): void {
    if (this.#closed || this.#socket.readyState !== 1) {
      throw new VoiceSessionClosedError(1_006, "socket is not open");
    }
  }

  #fail(error: unknown): void {
    const cause = error instanceof Error ? error : new VoiceProtocolError("Unknown voice protocol failure");
    if (!this.#readySettled) {
      this.#readySettled = true;
      this.#rejectReady(cause);
    }
    this.#queue.close(cause);
    this.close(1_002, "protocol error");
  }

  #detach(): void {
    this.#socket.removeEventListener("message", this.#onMessage);
    this.#socket.removeEventListener("error", this.#onError);
    this.#socket.removeEventListener("close", this.#onClose);
    if (this.#abortListener !== undefined) {
      this.#abortSignal?.removeEventListener("abort", this.#abortListener);
    }
  }
}

export function createVoiceClient(
  control: Client<typeof VoiceControlService>,
  transportOptions: VoiceTransportOptions,
): OMAIVoice {
  return Object.freeze({
    async connect(input: VoiceConnectInput, options?: CallOptions): Promise<OMAIVoiceSession> {
      const gatewayUrl = input.gatewayUrl ?? transportOptions.gatewayUrl;
      if (gatewayUrl === undefined) {
        throw new TypeError("voice gateway URL is required");
      }
      const response = await control.mintTicket({
        workspaceId: checkedIdentifier(input.workspaceId, "workspace id"),
        locale: input.locale ?? "",
        voice: input.voice ?? "",
      }, options);
      if (response.expiresUnixMillis <= BigInt(Date.now())) {
        throw new VoiceProtocolError("Voice ticket expired before connection");
      }
      const socketUrl = voiceSocketUrl(gatewayUrl, response.websocketPath, response.ticket, transportOptions.allowInsecureTransport ?? false);
      const factory = transportOptions.webSocketFactory ?? defaultWebSocketFactory;
      const readyTimeoutMs = boundedTimeout(input.readyTimeoutMs ?? 10_000);
      const session = new OMAIVoiceSession(factory(socketUrl), readyTimeoutMs, input.signal);
      await session.ready;
      return session;
    },
  });
}

function voiceSocketUrl(gatewayUrl: string, path: string, ticket: string, allowInsecure: boolean): string {
  if (!path.startsWith("/") || path.startsWith("//") || /[\r\n\0]/u.test(path)) {
    throw new VoiceProtocolError("Voice gateway returned an invalid WebSocket path");
  }
  if (ticket.length < 16 || ticket.length > 4_096 || /[\r\n\0]/u.test(ticket)) {
    throw new VoiceProtocolError("Voice gateway returned an invalid ticket");
  }
  const base = new URL(gatewayUrl);
  if (base.protocol !== "https:" && base.protocol !== "http:") {
    throw new TypeError("Voice gateway URL must use HTTPS or HTTP");
  }
  if (base.protocol === "http:" && !allowInsecure && !isLoopback(base.hostname)) {
    throw new TypeError("Plain HTTP voice transport is allowed only for loopback development");
  }
  const target = new URL(path, base);
  target.protocol = base.protocol === "https:" ? "wss:" : "ws:";
  target.searchParams.set("ticket", ticket);
  return target.toString();
}

function defaultWebSocketFactory(url: string): WebSocket {
  if (typeof WebSocket === "undefined") {
    throw new TypeError("WebSocket is unavailable; provide webSocketFactory explicitly");
  }
  return new WebSocket(url);
}

function parseAudioFrame(value: unknown): VoiceAudioEvent {
  if (!(value instanceof ArrayBuffer) || value.byteLength === 0 || value.byteLength > 128 * 1024) {
    throw new VoiceProtocolError("Invalid voice audio frame");
  }
  return Object.freeze({ type: "audio", data: value });
}

export function parseServerMessage(serialized: string): VoiceEvent {
  if (serialized.length === 0 || serialized.length > 128 * 1024) {
    throw new VoiceProtocolError("Invalid voice control message size");
  }
  let value: unknown;
  try {
    value = JSON.parse(serialized) as unknown;
  } catch (error) {
    throw new VoiceProtocolError(error instanceof Error ? `Invalid voice JSON: ${error.message}` : "Invalid voice JSON");
  }
  const object = objectValue(value, "voice message");
  const type = stringValue(object.type, "voice message type");
  switch (type) {
    case "ready":
      return Object.freeze({
        type,
        sessionId: stringValue(object.session_id, "session_id"),
        provider: stringValue(object.provider, "provider"),
        model: stringValue(object.model, "model"),
        registryEtag: stringValue(object.registry_etag, "registry_etag"),
        inputSampleRateHz: positiveInteger(object.input_sample_rate_hz, "input_sample_rate_hz"),
        outputSampleRateHz: positiveInteger(object.output_sample_rate_hz, "output_sample_rate_hz"),
      });
    case "transcript": {
      const role = stringValue(object.role, "role");
      if (role !== "user" && role !== "assistant") {
        throw new VoiceProtocolError("Invalid transcript role");
      }
      return Object.freeze({ type, role, transcript: stringValue(object.transcript, "transcript", true) });
    }
    case "confirmation_required":
      return Object.freeze({
        type,
        requestId: checkedRequestId(stringValue(object.request_id, "request_id")),
        tool: stringValue(object.tool, "tool"),
        message: stringValue(object.message, "message", true),
      });
    case "ui_command":
      return Object.freeze({
        type,
        requestId: checkedRequestId(stringValue(object.request_id, "request_id")),
        tool: stringValue(object.tool, "tool"),
        action: stringValue(object.action, "action"),
        timeoutMs: positiveInteger(object.timeout_ms, "timeout_ms"),
        payload: Object.freeze(objectValue(object.payload ?? {}, "payload")),
      });
    case "tool_result":
      return Object.freeze({
        type,
        requestId: checkedRequestId(stringValue(object.request_id, "request_id")),
        tool: stringValue(object.tool, "tool"),
        payload: object.payload,
      });
    case "error":
      return Object.freeze({ type, message: stringValue(object.message, "message", true) });
    case "interrupted":
    case "turn_complete":
    case "pong":
      return Object.freeze({ type });
    default:
      throw new VoiceProtocolError(`Unsupported voice message type ${JSON.stringify(type)}`);
  }
}

function objectValue(value: unknown, name: string): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new VoiceProtocolError(`${name} must be an object`);
  }
  return value as Record<string, unknown>;
}

function stringValue(value: unknown, name: string, allowEmpty = false): string {
  if (typeof value !== "string" || (!allowEmpty && value.length === 0) || value.length > 16_384 || /[\0]/u.test(value)) {
    throw new VoiceProtocolError(`Invalid ${name}`);
  }
  return value;
}

function positiveInteger(value: unknown, name: string): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value <= 0) {
    throw new VoiceProtocolError(`Invalid ${name}`);
  }
  return value;
}

function checkedIdentifier(value: string, name: string): string {
  const normalized = value.trim();
  if (normalized === "" || normalized !== value || normalized.length > 256 || /[\r\n\0]/u.test(value)) {
    throw new TypeError(`Invalid ${name}`);
  }
  return normalized;
}

function checkedRequestId(value: string): string {
  if (value.length === 0 || value.length > 256 || /[\r\n\0]/u.test(value)) {
    throw new TypeError("Invalid voice request id");
  }
  return value;
}

function checkedCode(value: string): string {
  if (!/^[A-Z0-9_]{1,64}$/u.test(value)) {
    throw new TypeError("Invalid voice result code");
  }
  return value;
}

function boundedText(value: string, maximum: number, name: string): string {
  if (new TextEncoder().encode(value).byteLength > maximum || /[\0]/u.test(value)) {
    throw new TypeError(`Invalid ${name}`);
  }
  return value;
}

function isLoopback(hostname: string): boolean {
  return hostname === "localhost" || hostname === "127.0.0.1" || hostname === "[::1]" || hostname === "::1";
}

function boundedTimeout(value: number): number {
  if (!Number.isSafeInteger(value) || value < 1_000 || value > 60_000) {
    throw new RangeError("Voice ready timeout must be between 1000 and 60000 milliseconds");
  }
  return value;
}

class AsyncEventQueue<T> implements AsyncIterable<T> {
  readonly #capacity: number;
  #values: T[] = [];
  #waiters: Array<{ resolve: (result: IteratorResult<T>) => void; reject: (error: unknown) => void }> = [];
  #closed = false;
  #error: unknown;
  #consumerCreated = false;

  constructor(capacity: number) {
    this.#capacity = capacity;
  }

  push(value: T): boolean {
    if (this.#closed) {
      return false;
    }
    const waiter = this.#waiters.shift();
    if (waiter !== undefined) {
      waiter.resolve({ done: false, value });
      return true;
    }
    if (this.#values.length >= this.#capacity) {
      return false;
    }
    this.#values.push(value);
    return true;
  }

  close(error?: unknown): void {
    if (this.#closed) {
      return;
    }
    this.#closed = true;
    this.#error = error;
    for (const waiter of this.#waiters.splice(0)) {
      if (error === undefined) {
        waiter.resolve({ done: true, value: undefined });
      } else {
        waiter.reject(error);
      }
    }
  }

  [Symbol.asyncIterator](): AsyncIterator<T> {
    if (this.#consumerCreated) {
      throw new TypeError("OMAI voice events support one async consumer per session");
    }
    this.#consumerCreated = true;
    return { next: () => this.#next() };
  }

  #next(): Promise<IteratorResult<T>> {
    const value = this.#values.shift();
    if (value !== undefined) {
      return Promise.resolve({ done: false, value });
    }
    if (this.#closed) {
      return this.#error === undefined
        ? Promise.resolve({ done: true, value: undefined })
        : Promise.reject(this.#error);
    }
    return new Promise<IteratorResult<T>>((resolve, reject) => {
      this.#waiters.push({ resolve, reject });
    });
  }
}
