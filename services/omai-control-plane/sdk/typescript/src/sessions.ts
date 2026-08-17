import type { CallOptions, Client } from "@connectrpc/connect"
import { Code } from "@connectrpc/connect"
import type { ProjectService, Session, SessionService } from "./gen/omai/platform/v1/platform_pb.js"
import { OMAIError } from "./errors.js"
import { parseTypedSessionEvent, type OMAISessionEvent } from "./events.js"

export interface OMAISessionRoute {
  readonly runtimeId: string
  readonly providerId: string
  readonly modelId: string
}

export interface OMAISendInput extends OMAISessionRoute {
  readonly externalSessionId: string
  readonly root: string
  readonly text: string
  readonly title?: string
  readonly projectName?: string
}

export interface OMAISessionHandle extends OMAISessionRoute {
  readonly sessionId: string
  readonly externalSessionId: string
  readonly projectId: string
  readonly workspaceId: string
  readonly root: string
}

export interface OMAISubscribeInput {
  readonly sessionId: string
  readonly since?: bigint
  /** @deprecated The typed platform stream derives this from the session. */
  readonly runtimeId?: string
  /** @deprecated The typed platform stream derives this from the session. */
  readonly workspaceId?: string
}

export interface OMAIConversationMessage {
  readonly id: string
  readonly sessionId: string
  readonly role: string
  readonly kind: string
  readonly text: string
  readonly dataJson: Uint8Array
  readonly createdAtUnixMillis: bigint
}

export interface OMAIPlatformSession {
  readonly id: string
  readonly externalSessionId: string
  readonly projectId: string
  readonly workspaceId: string
  readonly runtimeId: string
  readonly providerId: string
  readonly modelId: string
  readonly title: string
  readonly archived: boolean
  readonly createdUnixMillis: bigint
  readonly updatedUnixMillis: bigint
}

export interface OMAISessionPatch {
  readonly title?: string
  readonly archived?: boolean
}

export interface OMAISessionCreateInput {
  readonly projectId: string
  readonly runtimeId: string
  readonly externalSessionId: string
  readonly title?: string
}

export interface OMAISessionSubmitInput {
  readonly providerId: string
  readonly modelId: string
  readonly text: string
}

export interface OMAISessions {
  create(
    input: OMAISessionCreateInput,
    options?: CallOptions,
  ): Promise<{ readonly session: OMAIPlatformSession; readonly created: boolean }>
  list(projectId: string, includeArchived?: boolean, options?: CallOptions): Promise<readonly OMAIPlatformSession[]>
  get(sessionId: string, options?: CallOptions): Promise<OMAIPlatformSession>
  update(sessionId: string, patch: OMAISessionPatch, options?: CallOptions): Promise<OMAIPlatformSession>
  delete(sessionId: string, options?: CallOptions): Promise<boolean>
  submit(sessionId: string, input: OMAISessionSubmitInput, options?: CallOptions): Promise<OMAIPlatformSession>
  send(input: OMAISendInput, options?: CallOptions): Promise<OMAISessionHandle>
  cancel(input: Pick<OMAISessionHandle, "sessionId">, options?: CallOptions): Promise<boolean>
  listMessages(sessionId: string, options?: CallOptions): Promise<readonly OMAIConversationMessage[]>
  subscribe(input: OMAISubscribeInput, options?: CallOptions): AsyncIterable<OMAISessionEvent>
}

export function createSessions(
  projects: Client<typeof ProjectService>,
  sessions: Client<typeof SessionService>,
): OMAISessions {
  return Object.freeze({
    async create(input: OMAISessionCreateInput, options?: CallOptions) {
      const response = await sessions.createSession(
        {
          projectId: requireIdentifier(input.projectId, "projectId"),
          runtimeId: requireIdentifier(input.runtimeId, "runtimeId"),
          externalSessionId: requireIdentifier(input.externalSessionId, "externalSessionId"),
          title: optionalText(input.title, "title", 500),
        },
        options,
      )
      return Object.freeze({
        session: platformSession(required(response.session, "session")),
        created: response.created,
      })
    },

    async list(projectId: string, includeArchived = false, options?: CallOptions) {
      const result: OMAIPlatformSession[] = []
      let pageToken = ""
      for (let page = 0; page < 10_000; page++) {
        const response = await sessions.listSessions(
          {
            projectId: requireIdentifier(projectId, "projectId"),
            includeArchived,
            pageSize: 200,
            pageToken,
          },
          options,
        )
        result.push(...response.sessions.map(platformSession))
        if (!response.nextPageToken) return Object.freeze(result)
        if (response.nextPageToken === pageToken) {
          throw new OMAIError("OMAI returned a repeated session page token", { code: Code.DataLoss })
        }
        pageToken = response.nextPageToken
      }
      throw new OMAIError("OMAI session pagination exceeded its safety limit", { code: Code.ResourceExhausted })
    },

    async get(sessionId: string, options?: CallOptions) {
      const response = await sessions.getSession({ sessionId: requireIdentifier(sessionId, "sessionId") }, options)
      return platformSession(required(response.session, "session"))
    },

    async update(sessionId: string, patch: OMAISessionPatch, options?: CallOptions) {
      if (patch.title === undefined && patch.archived === undefined)
        throw new TypeError("Session update cannot be empty")
      const response = await sessions.updateSession(
        {
          sessionId: requireIdentifier(sessionId, "sessionId"),
          ...(patch.title === undefined ? {} : { title: optionalText(patch.title, "title", 500) }),
          ...(patch.archived === undefined ? {} : { archived: patch.archived }),
        },
        options,
      )
      return platformSession(required(response.session, "session"))
    },

    async delete(sessionId: string, options?: CallOptions) {
      return (await sessions.deleteSession({ sessionId: requireIdentifier(sessionId, "sessionId") }, options)).deleted
    },

    async submit(sessionId: string, input: OMAISessionSubmitInput, options?: CallOptions) {
      const response = await sessions.submitText(
        {
          sessionId: requireIdentifier(sessionId, "sessionId"),
          providerId: requireIdentifier(input.providerId, "providerId"),
          modelId: requireIdentifier(input.modelId, "modelId"),
          text: requireIdentifier(input.text, "text"),
        },
        options,
      )
      return platformSession(required(response.session, "session"))
    },

    async send(input: OMAISendInput, options?: CallOptions): Promise<OMAISessionHandle> {
      validateSend(input)
      const resolved = await projects.resolveProject(
        {
          root: input.root,
          name: input.projectName ?? "",
        },
        options,
      )
      const project = resolved.project
      if (project === undefined || project.id.length === 0 || project.workspaceId.length === 0) {
        throw new Error("OMAI did not resolve the project")
      }
      const created = await sessions.createSession(
        {
          projectId: project.id,
          runtimeId: input.runtimeId,
          externalSessionId: input.externalSessionId,
          title: input.title ?? "",
        },
        options,
      )
      if (created.session === undefined || created.session.id.length === 0) {
        throw new Error("OMAI did not create the session")
      }
      const submitted = await sessions.submitText(
        {
          sessionId: created.session.id,
          providerId: input.providerId,
          modelId: input.modelId,
          text: input.text,
        },
        options,
      )
      const session = submitted.session
      if (session === undefined || session.id.length === 0) {
        throw new Error("OMAI did not accept the session turn")
      }
      return Object.freeze({
        runtimeId: session.runtimeId,
        providerId: session.providerId,
        modelId: session.modelId,
        sessionId: session.id,
        externalSessionId: session.externalSessionId,
        projectId: session.projectId,
        workspaceId: session.workspaceId,
        root: project.root,
      })
    },

    async cancel(input: Pick<OMAISessionHandle, "sessionId">, options?: CallOptions): Promise<boolean> {
      requireIdentifier(input.sessionId, "sessionId")
      return (await sessions.cancelSession({ sessionId: input.sessionId }, options)).cancelled
    },

    async listMessages(sessionId: string, options?: CallOptions): Promise<readonly OMAIConversationMessage[]> {
      requireIdentifier(sessionId, "sessionId")
      const response = await sessions.listMessages({ sessionId }, options)
      return Object.freeze(
        response.messages.map((message) =>
          Object.freeze({
            id: message.id,
            sessionId: message.sessionId,
            role: message.role,
            kind: message.kind,
            text: message.text,
            dataJson: message.dataJson.slice(),
            createdAtUnixMillis: message.createdUnixMillis,
          }),
        ),
      )
    },

    subscribe(input: OMAISubscribeInput, options?: CallOptions): AsyncIterable<OMAISessionEvent> {
      requireIdentifier(input.sessionId, "sessionId")
      const source = sessions.subscribeSessionEvents(
        {
          sessionId: input.sessionId,
          afterSequence: input.since ?? 0n,
        },
        options,
      )
      return {
        async *[Symbol.asyncIterator]() {
          for await (const event of source) {
            yield parseTypedSessionEvent(event)
          }
        },
      }
    },
  })
}

function validateSend(input: OMAISendInput): void {
  requireIdentifier(input.runtimeId, "runtimeId")
  requireIdentifier(input.providerId, "providerId")
  requireIdentifier(input.modelId, "modelId")
  requireIdentifier(input.externalSessionId, "externalSessionId")
  requireIdentifier(input.root, "root")
  requireIdentifier(input.text, "text")
  if (input.projectName !== undefined) {
    requireIdentifier(input.projectName, "projectName")
  }
}

function requireIdentifier(value: string, name: string): string {
  const trimmed = value.trim()
  if (trimmed.length === 0 || trimmed.length > 4_096 || /[\r\n\0]/u.test(trimmed)) {
    throw new TypeError(`Invalid OMAI ${name}`)
  }
  return trimmed
}

function required<T>(value: T | undefined, label: string): T {
  if (value === undefined) throw new OMAIError(`OMAI returned no ${label}`, { code: Code.Internal })
  return value
}

function optionalText(value: string | undefined, name: string, limit: number): string {
  if (value === undefined || value === "") return ""
  const trimmed = value.trim()
  if (trimmed.length > limit || /[\0]/u.test(trimmed)) throw new TypeError(`Invalid OMAI ${name}`)
  return trimmed
}

function platformSession(value: Session): OMAIPlatformSession {
  return Object.freeze({
    id: value.id,
    externalSessionId: value.externalSessionId,
    projectId: value.projectId,
    workspaceId: value.workspaceId,
    runtimeId: value.runtimeId,
    providerId: value.providerId,
    modelId: value.modelId,
    title: value.title,
    archived: value.archived,
    createdUnixMillis: value.createdUnixMillis,
    updatedUnixMillis: value.updatedUnixMillis,
  })
}
