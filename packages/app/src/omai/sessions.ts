import type { GlobalSession, Session } from "@opencode-ai/sdk/v2/client"
import type { OMAIPlatformSession, OMAIProject } from "@omai/sdk-web"
import { omaiClient } from "./client"

// Presentation-only mapping for the existing SolidJS timeline. Session
// identity, lifecycle and persistence remain authoritative in Go.
export function omaiSessionView(session: OMAIPlatformSession, project: OMAIProject): Session {
  return {
    id: session.id,
    slug: session.externalSessionId || session.id,
    projectID: session.projectId,
    workspaceID: session.workspaceId,
    directory: project.root,
    title: session.title,
    version: "1.0.0",
    time: {
      created: Number(session.createdUnixMillis),
      updated: Number(session.updatedUnixMillis),
      ...(session.archived ? { archived: Number(session.updatedUnixMillis) } : {}),
    },
  }
}

export async function searchOMAISessions(search: string, signal?: AbortSignal): Promise<{ data: GlobalSession[] }> {
  const query = search.trim().toLowerCase()
  if (!query) return { data: [] }
  const projects = await omaiClient.projects.list({ signal })
  const sessions = await Promise.all(
    projects.map(async (project) =>
      (await omaiClient.sessions.list(project.id, false, { signal })).map((session) => ({ project, session })),
    ),
  )
  return {
    data: sessions
      .flat()
      .filter(({ project, session }) =>
        [session.title, session.externalSessionId, project.name, project.root].some((value) =>
          value.toLowerCase().includes(query),
        ),
      )
      .sort((left, right) => Number(right.session.updatedUnixMillis - left.session.updatedUnixMillis))
      .slice(0, 50)
      .map(({ project, session }) => ({
        ...omaiSessionView(session, project),
        project: { id: project.id, name: project.name, worktree: project.root },
      })),
  }
}

export async function listOMAISessionViews(signal?: AbortSignal): Promise<Session[]> {
  const projects = await omaiClient.projects.list({ signal })
  const sessions = await Promise.all(
    projects.map(async (project) =>
      (await omaiClient.sessions.list(project.id, false, { signal })).map((session) =>
        omaiSessionView(session, project),
      ),
    ),
  )
  return sessions
    .flat()
    .sort(
      (left, right) => right.time.updated - left.time.updated || (left.id < right.id ? -1 : left.id > right.id ? 1 : 0),
    )
}

export async function listOMAISessionViewsForRoot(root: string, signal?: AbortSignal): Promise<Session[]> {
  const project = await omaiClient.projects.resolve(root, "", { signal })
  const sessions = await omaiClient.sessions.list(project.id, false, { signal })
  return sessions
    .map((session) => omaiSessionView(session, project))
    .sort(
      (left, right) => right.time.updated - left.time.updated || (left.id < right.id ? -1 : left.id > right.id ? 1 : 0),
    )
}

export async function getOMAISessionView(sessionId: string, signal?: AbortSignal): Promise<Session> {
  const session = await omaiClient.sessions.get(sessionId, { signal })
  const project = await omaiClient.projects.get(session.projectId, { signal })
  return omaiSessionView(session, project)
}
