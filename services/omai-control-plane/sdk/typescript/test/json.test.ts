import { describe, expect, it } from "vitest";
import { decodeJsonBytes, encodeJsonBytes } from "../src/json.js";

describe("JSON byte helpers", () => {
  it("round-trips event and tool payloads", () => {
    const source = { payload: { text: "Bismillah", ok: true } };
    expect(decodeJsonBytes(encodeJsonBytes(source))).toEqual(source);
  });

  it("enforces bounded payloads", () => {
    expect(() => encodeJsonBytes({ value: "too large" }, 4)).toThrow(RangeError);
    expect(() => decodeJsonBytes(new Uint8Array(), 4)).toThrow(SyntaxError);
  });
});
