import type { Session } from "@opencode-ai/sdk/v2/client"
import { base64Encode } from "@omai/platform-utils/encode"

// Navigate to the project immediately instead of blocking the click on the
// session-list fetch. The most recent session is opened as a best-effort
// follow-up: only while the user still sits on the bare project session route.
export function openProjectImmediate(
  directory: string,
  navigate: (href: string) => void,
  isBareSessionRoute: () => boolean,
  listSessions: () => Promise<{ data?: Session[] }>,
  createSession: () => Promise<{ data?: Session }>,
): void {
  navigate(`/${base64Encode(directory)}/session`)
  listSessions()
    .then((result) => {
      const sessions = (result.data ?? []).filter((session) => !session.time?.archived)
      if (sessions.length > 0) {
        const sorted = [...sessions].sort(
          (a, b) => (b.time?.updated ?? b.time?.created ?? 0) - (a.time?.updated ?? a.time?.created ?? 0),
        )
        return sorted[0].id
      }
      return createSession().then((created) => created.data?.id)
    })
    .then((id) => {
      if (!id || !isBareSessionRoute()) return
      navigate(`/${base64Encode(directory)}/session/${id}`)
    })
    .catch(() => {})
}
