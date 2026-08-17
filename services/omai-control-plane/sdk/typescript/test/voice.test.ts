import { describe, expect, it } from "vitest";
import { OMAIVoiceSession, parseServerMessage, VoiceProtocolError } from "../src/voice.js";

describe("voice protocol", () => {
  it("parses the negotiated ready envelope", () => {
    expect(parseServerMessage(JSON.stringify({
      type: "ready",
      session_id: "vcs_1",
      provider: "gemini-live",
      model: "gemini-3.1-flash-live-preview",
      registry_etag: "etag-1",
      input_sample_rate_hz: 16000,
      output_sample_rate_hz: 24000,
    }))).toEqual({
      type: "ready",
      sessionId: "vcs_1",
      provider: "gemini-live",
      model: "gemini-3.1-flash-live-preview",
      registryEtag: "etag-1",
      inputSampleRateHz: 16000,
      outputSampleRateHz: 24000,
    });
  });

  it("parses only the allow-listed UI command envelope", () => {
    expect(parseServerMessage(JSON.stringify({
      type: "ui_command",
      request_id: "call-17",
      tool: "open_portal_file",
      action: "open_file",
      timeout_ms: 5000,
      payload: { workspace_id: "wsp_1", path: "main.go" },
    }))).toMatchObject({
      type: "ui_command",
      requestId: "call-17",
      action: "open_file",
      payload: { path: "main.go" },
    });
  });

  it("fails closed on unknown message types", () => {
    expect(() => parseServerMessage('{"type":"execute_javascript"}')).toThrow(VoiceProtocolError);
    expect(() => parseServerMessage("{")).toThrow(VoiceProtocolError);
  });

  it("negotiates ready and sends bounded binary/control frames", async () => {
    const socket = new FakeWebSocket();
    const session = new OMAIVoiceSession(socket as unknown as WebSocket, 1_000);
    socket.message(JSON.stringify({
      type: "ready",
      session_id: "vcs_1",
      provider: "gemini-live",
      model: "gemini-live",
      registry_etag: "etag-1",
      input_sample_rate_hz: 16000,
      output_sample_rate_hz: 24000,
    }));

    await expect(session.ready).resolves.toMatchObject({ inputSampleRateHz: 16000 });
    await expect(session[Symbol.asyncIterator]().next()).resolves.toMatchObject({
      done: false,
      value: { type: "ready" },
    });

    session.sendAudio(new Uint8Array([1, 2, 3, 4]));
    session.confirm("call-1", true);
    session.acknowledgeUI({ requestId: "call-2", success: true, payload: { applied: true } });

    expect(socket.sent[0]).toBeInstanceOf(ArrayBuffer);
    expect(socket.sent[1]).toBe('{"type":"confirm","request_id":"call-1","confirmed":true}');
    expect(socket.sent[2]).toContain('"type":"ui_result"');
    session.close();
  });
});

class FakeWebSocket extends EventTarget {
  binaryType: BinaryType = "blob";
  readyState = 1;
  readonly sent: Array<string | ArrayBufferLike | Blob | ArrayBufferView> = [];

  send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void {
    this.sent.push(data);
  }

  close(): void {
    this.readyState = 3;
  }

  message(data: string | ArrayBuffer): void {
    this.dispatchEvent(new MessageEvent("message", { data }));
  }
}
