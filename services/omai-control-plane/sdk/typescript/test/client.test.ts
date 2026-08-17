import { fromJson } from "@bufbuild/protobuf";
import { StructSchema } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { createOMAIClient, createOMAIClientFromTransport } from "../src/client.js";
import { asOMAIError } from "../src/errors.js";
import { ControlPlaneService } from "../src/gen/uab/v1/uab_pb.js";
import { ModelCatalogService } from "../src/gen/uab/v1/model_catalog_pb.js";
import { createMetadataInterceptor } from "../src/metadata.js";

describe("OMAI client", () => {
  it("creates typed service clients from one transport", async () => {
    const transport = createRouterTransport((router) => {
      router.service(ControlPlaneService, {
        health: () => ({ ok: true, unixMillis: 1_750_000_000_000n, version: "test" }),
      });
    });
    const omai = createOMAIClientFromTransport(transport);

    await expect(omai.services.controlPlane.health({})).resolves.toMatchObject({
      ok: true,
      version: "test",
    });
    expect(Object.isFrozen(omai)).toBe(true);
    expect(Object.isFrozen(omai.services)).toBe(true);
  });

  it("adds fresh authentication and identity metadata", async () => {
    let tokenCalls = 0;
    const transport = createRouterTransport((router) => {
      router.service(ControlPlaneService, {
        health: (_request, context) => {
          expect(context.requestHeader.get("authorization")).toBe("Bearer token-1");
          expect(context.requestHeader.get("x-omai-tenant-id")).toBe("tenant-a");
          expect(context.requestHeader.get("x-omai-actor-id")).toBe("actor-a");
          expect(context.requestHeader.get("x-sdk-test")).toBe("present");
          return { ok: true, unixMillis: 1n, version: "test" };
        },
      });
    }, {
      transport: {
        interceptors: [createMetadataInterceptor({
          accessToken: () => `token-${++tokenCalls}`,
          tenantId: "tenant-a",
          actorId: "actor-a",
          headers: { "X-SDK-Test": "present" },
        })],
      },
    });

    await createOMAIClientFromTransport(transport).services.controlPlane.health({});
    expect(tokenCalls).toBe(1);
  });

  it("rejects production cleartext endpoints by default", () => {
    expect(() => createOMAIClient({ baseUrl: "http://api.example.com" })).toThrow(TypeError);
    expect(() => createOMAIClient({ baseUrl: "/" })).not.toThrow();
  });

  it("prevents generic headers from overriding identity metadata", async () => {
    const transport = createRouterTransport((router) => {
      router.service(ControlPlaneService, {
        health: () => ({ ok: true, unixMillis: 1n, version: "test" }),
      });
    }, {
      transport: {
        interceptors: [createMetadataInterceptor({
          accessToken: "trusted-token",
          headers: { Authorization: "Bearer attacker-controlled" },
        })],
      },
    });

    await expect(createOMAIClientFromTransport(transport).services.controlPlane.health({})).rejects.toMatchObject({
      cause: expect.any(TypeError),
    });
  });

  it("exposes a typed catalog facade over the compatibility Struct contract", async () => {
    const response = fromJson(StructSchema, {
      schema_version: "1",
      providers: [{
        id: "openrouter",
        source_id: "openrouter",
        name: "OpenRouter",
        model_count: 1,
        connected: true,
        runtime_id: "go-adk",
      }],
      connected: ["openrouter"],
      default: { openrouter: "anthropic/claude-sonnet-4.5" },
      models: [{
        id: "anthropic/claude-sonnet-4.5",
        name: "Claude Sonnet 4.5",
        provider_id: "openrouter",
        runtime_id: "go-adk",
        ready: true,
        tool_call: true,
        limits: { context: 200000, output: 64000 },
        cost: { input: 3, output: 15 },
      }],
      total: 1,
      offset: 0,
      next_offset: 0,
    });
    const transport = createRouterTransport((router) => {
      router.service(ModelCatalogService, {
        listModels: () => response,
      });
    });

    const page = await createOMAIClientFromTransport(transport).models.listModels({
      providerId: "openrouter",
    });

    expect(page.total).toBe(1);
    expect(page.defaults.openrouter).toBe("anthropic/claude-sonnet-4.5");
    expect(page.models[0]).toMatchObject({
      providerId: "openrouter",
      ready: true,
      toolCall: true,
      limits: { context: 200000, output: 64000 },
    });
  });
});

describe("OMAI errors", () => {
  it("preserves Connect code, metadata and retryability", () => {
    const source = new ConnectError("temporarily unavailable", Code.Unavailable, new Headers({ "retry-after": "2" }));
    const error = asOMAIError(source);

    expect(error.code).toBe(Code.Unavailable);
    expect(error.retryable).toBe(true);
    expect(error.metadata.get("retry-after")).toBe("2");
    expect(error.cause).toBe(source);
  });
});
