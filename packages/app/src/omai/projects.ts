import type { Project } from "@opencode-ai/sdk/v2/client"
import type { OMAIProject } from "@omai/sdk-web"

// The visual shell still consumes its established Project view model. This is
// a pure presentation mapping; project identity and persistence belong to Go.
export function omaiProjectView(project: OMAIProject): Project {
  return {
    id: project.id,
    worktree: project.root,
    ...(project.repoRoot ? { vcs: "git" as const } : {}),
    ...(project.name ? { name: project.name } : {}),
    ...(project.iconColor || project.iconOverride
      ? { icon: { color: project.iconColor || undefined, override: project.iconOverride || undefined } }
      : {}),
    ...(project.startupCommand ? { commands: { start: project.startupCommand } } : {}),
    time: {
      created: Number(project.createdUnixMillis),
      updated: Number(project.updatedUnixMillis),
    },
    sandboxes: [],
  }
}
