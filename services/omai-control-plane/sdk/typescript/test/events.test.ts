import { create } from "@bufbuild/protobuf"
import { describe, expect, it } from "vitest"
import { EventSchema } from "../src/gen/uab/v1/uab_pb.js"
import { MessageChannel, PermissionDecision, SessionEventSchema } from "../src/gen/omai/platform/v1/platform_pb.js"
import { encodeJsonBytes } from "../src/json.js"
import { parseSessionEvent, parseTypedSessionEvent } from "../src/events.js"

describe("OMAI session events", () => {
  it("normalizes message chunks", () => {
    const event = parseSessionEvent(
      create(EventSchema, {
        seq: 7n,
        unixMillis: 1_750_000_000_000n,
        type: "acp.session/update",
        sessionId: "session-1",
        workspaceId: "workspace-1",
        runtimeId: "go-adk",
        payloadJson: encodeJsonBytes({
          payload: {
            update: {
              sessionUpdate: "agent_message_chunk",
              messageId: "message-1",
              content: { type: "text", text: "Bismillah" },
            },
          },
        }),
      }),
    )

    expect(event).toMatchObject({
      type: "message_delta",
      sequence: 7n,
      messageId: "message-1",
      channel: "answer",
      delta: "Bismillah",
    })
  })

  it("preserves unknown events", () => {
    const event = parseSessionEvent(
      create(EventSchema, {
        type: "future.event",
        payloadJson: encodeJsonBytes({ payload: { version: 2 } }),
      }),
    )

    expect(event).toMatchObject({
      type: "unknown",
      wireType: "future.event",
      payload: { version: 2 },
    })
  })

  it("maps the stable typed platform event without a JSON discriminator", () => {
    const event = parseTypedSessionEvent(
      create(SessionEventSchema, {
        sequence: 9n,
        sessionId: "session-1",
        externalSessionId: "portal-1",
        payload: {
          case: "messageDelta",
          value: {
            messageId: "message-2",
            channel: MessageChannel.REASONING,
            delta: "checking",
          },
        },
      }),
    )

    expect(event).toMatchObject({
      type: "message_delta",
      sequence: 9n,
      externalSessionId: "portal-1",
      messageId: "message-2",
      channel: "reasoning",
      delta: "checking",
    })
  })

  it("maps durable permission and question interaction events", () => {
    const requested = parseTypedSessionEvent(
      create(SessionEventSchema, {
        sequence: 10n,
        sessionId: "session-1",
        payload: {
          case: "permissionRequested",
          value: {
            permission: {
              id: "permission-1",
              sessionId: "session-1",
              permission: "edit",
              patterns: ["src/**"],
              metadataJson: encodeJsonBytes({ reason: "tool" }),
            },
          },
        },
      }),
    )
    const resolved = parseTypedSessionEvent(
      create(SessionEventSchema, {
        sequence: 11n,
        sessionId: "session-1",
        payload: {
          case: "permissionResolved",
          value: { sessionId: "session-1", permissionId: "permission-1", decision: PermissionDecision.ONCE },
        },
      }),
    )

    expect(requested).toMatchObject({
      type: "permission_requested",
      permission: { id: "permission-1", metadata: { reason: "tool" } },
    })
    expect(resolved).toMatchObject({ type: "permission_resolved", permissionId: "permission-1", decision: "once" })
  })
})
