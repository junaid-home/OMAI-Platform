import type { OMAIFileContent } from "@omai/sdk-web"

export type FileNode = {
  name: string
  path: string
  absolute: string
  type: "file" | "directory"
  ignored: boolean
}

export type FileContent = OMAIFileContent & {
  diff?: string
  patch?: {
    oldFileName: string
    newFileName: string
    oldHeader?: string
    newHeader?: string
    hunks: Array<{
      oldStart: number
      oldLines: number
      newStart: number
      newLines: number
      lines: Array<string>
    }>
    index?: string
  }
}

export type FileSelection = {
  startLine: number
  startChar: number
  endLine: number
  endChar: number
}

export type SelectedLineRange = {
  start: number
  end: number
  side?: "additions" | "deletions"
  endSide?: "additions" | "deletions"
}

export type FileViewState = {
  scrollTop?: number
  scrollLeft?: number
  selectedLines?: SelectedLineRange | null
}

export type FileState = {
  path: string
  name: string
  loaded?: boolean
  loading?: boolean
  error?: string
  content?: FileContent
}

export function selectionFromLines(range: SelectedLineRange): FileSelection {
  const startLine = Math.min(range.start, range.end)
  const endLine = Math.max(range.start, range.end)
  return {
    startLine,
    endLine,
    startChar: 0,
    endChar: 0,
  }
}
