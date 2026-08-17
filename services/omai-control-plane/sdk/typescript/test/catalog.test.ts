import { describe, expect, it } from "vitest";
import { CatalogSchemaError, parseCatalogPage } from "../src/catalog.js";

describe("model catalog schema", () => {
  it("normalizes complete nested model metadata", () => {
    const page = parseCatalogPage({
      schema_version: "1",
      providers: [{ id: "google", name: "Google", model_count: 1, connected: true, runtime_id: "go-adk", runtime_ids: ["go-adk", "opencode"] }],
      connected: ["google"],
      default: { google: "gemini-flash-latest" },
      models: [{
        id: "gemini-flash-latest",
        name: "Gemini Flash",
        provider_id: "google",
        runtime_id: "go-adk",
        runtime_ids: ["go-adk", "opencode"],
        ready: true,
        reasoning_options: [{ type: "effort", values: ["low", "high", null] }],
        modalities: { input: ["text", "image"], output: ["text"] },
        limits: { context: 1048576, input: 1048576, output: 65536 },
        cost: {
          input: 0.3,
          output: 2.5,
          cache_read: 0.03,
          tiers: [{ input: 0.5, output: 3, tier: { type: "input", size: 200000 } }],
          context_over_200k: { input: 0.6, output: 5 },
        },
        experimental: {
          modes: {
            thinking: { provider: { body: { thinking: true }, headers: { "x-mode": "thinking" } } },
          },
        },
      }],
      total: 1,
      offset: 0,
      next_offset: 0,
    });

    const model = page.models[0];
    expect(model?.modalities.input).toEqual(["text", "image"]);
    expect(model?.reasoningOptions[0]?.values).toEqual(["low", "high", null]);
    expect(model?.cost?.cacheRead).toBe(0.03);
    expect(model?.cost?.tiers[0]?.tier.size).toBe(200000);
    expect(model?.experimentalModes.thinking?.provider?.body.thinking).toBe(true);
    expect(page.providers[0]?.runtimeIds).toEqual(["go-adk", "opencode"]);
    expect(model?.runtimeIds).toEqual(["go-adk", "opencode"]);
  });

  it("rejects malformed server data instead of silently trusting it", () => {
    expect(() => parseCatalogPage({
      schema_version: "1",
      providers: [],
      models: [{ id: "broken", provider_id: "google" }],
    })).toThrow(CatalogSchemaError);
  });
});
