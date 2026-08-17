import { describe, expect, test } from "bun:test"
import type { Session } from "@opencode-ai/sdk/v2/client"
import { base64Encode } from "@omai/platform-utils/encode"
import { openProjectImmediate } from "./open-project"

const session = (id: string, updated: number): Session => ({
  id,
  slug: id,
  projectID: "project",
  directory: "/repo",
  title: id,
  version: "1",
  time: { created: 1, updated },
})

describe("openProjectImmediate", () => {
  test("navigates immediately and resumes the most recent session", async () => {
    const hrefs: string[] = []
    openProjectImmediate(
      "/repo",
      (href) => hrefs.push(href),
      () => true,
      async () => ({ data: [session("older", 10), session("newest", 20)] }),
      async () => ({ data: session("created", 30) }),
    )

    expect(hrefs[0]).toBe(`/${base64Encode("/repo")}/session`)
    await Promise.resolve()
    await Promise.resolve()
    expect(hrefs).toEqual([`/${base64Encode("/repo")}/session`, `/${base64Encode("/repo")}/session/newest`])
  })

  test("skips archived sessions", async () => {
    const hrefs: string[] = []
    openProjectImmediate(
      "/repo",
      (href) => hrefs.push(href),
      () => true,
      async () => ({
        data: [
          { ...session("archived", 50), time: { created: 1, updated: 50, archived: 60 } },
          session("active", 10),
        ],
      }),
      async () => ({ data: session("created", 30) }),
    )
    await Promise.resolve()
    await Promise.resolve()
    expect(hrefs).toEqual([`/${base64Encode("/repo")}/session`, `/${base64Encode("/repo")}/session/active`])
  })

  test("does not resume once the user left the bare session route", async () => {
    const hrefs: string[] = []
    openProjectImmediate(
      "/repo",
      (href) => hrefs.push(href),
      () => false,
      async () => ({ data: [session("old", 10), session("new", 20)] }),
      async () => ({ data: session("created", 30) }),
    )
    await Promise.resolve()
    await Promise.resolve()
    expect(hrefs).toEqual([`/${base64Encode("/repo")}/session`])
  })

  test("creates a session when there are no sessions", async () => {
    const hrefs: string[] = []
    let created = false
    openProjectImmediate(
      "/repo",
      (href) => hrefs.push(href),
      () => true,
      async () => ({ data: [] }),
      async () => {
        created = true
        return { data: session("created", 30) }
      },
    )
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
    expect(created).toBe(true)
    expect(hrefs).toEqual([`/${base64Encode("/repo")}/session`, `/${base64Encode("/repo")}/session/created`])
  })

  test("stays on the project when a created session fails", async () => {
    const hrefs: string[] = []
    openProjectImmediate(
      "/repo",
      (href) => hrefs.push(href),
      () => true,
      async () => ({ data: [] }),
      async () => {
        throw new Error("create failed")
      },
    )
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
    expect(hrefs).toEqual([`/${base64Encode("/repo")}/session`])
  })
})
