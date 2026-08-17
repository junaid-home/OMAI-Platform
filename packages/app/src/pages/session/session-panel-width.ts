export const CHAT_PANEL_RATIO = 0.35
export const REVIEW_PANEL_RATIO = 0.65
export const CHAT_PANEL_MIN_RATIO = 0.25
export const CHAT_PANEL_MAX_RATIO = 0.55

export const SESSION_PANEL_WIDTH_MIN = 150

// The observer reports content-box width (already excludes padding).
// Subtract the flex gap between panels to get the true available space.
export function sessionPanelAvailableWidth(panelRowWidth: number, gap: number): number {
  return panelRowWidth - gap
}

export function chatPanelDefaultWidth(available: number): number {
  return Math.floor(available * CHAT_PANEL_RATIO)
}

export function chatPanelWidthMin(available: number): number {
  return Math.max(SESSION_PANEL_WIDTH_MIN, Math.floor(available * CHAT_PANEL_MIN_RATIO))
}

export function sessionPanelWidthMax(available: number) {
  return Math.max(SESSION_PANEL_WIDTH_MIN, Math.floor(available * CHAT_PANEL_MAX_RATIO))
}

// `available` is undefined until the layout row is first measured; render the
// stored width untouched until then to avoid a first-frame snap.
export function clampSessionPanelWidth(input: { width: number; available: number | undefined }) {
  if (input.available === undefined) return input.width
  const min = chatPanelWidthMin(input.available)
  const max = sessionPanelWidthMax(input.available)
  const lower = Math.min(min, max)
  return Math.max(lower, Math.min(input.width, max))
}
