import { describe, expect, test } from "bun:test"
import {
  CHAT_PANEL_MIN_RATIO,
  CHAT_PANEL_RATIO,
  CHAT_PANEL_MAX_RATIO,
  chatPanelDefaultWidth,
  chatPanelWidthMin,
  clampSessionPanelWidth,
  REVIEW_PANEL_RATIO,
  SESSION_PANEL_WIDTH_MIN,
  sessionPanelAvailableWidth,
  sessionPanelWidthMax,
} from "./session-panel-width"

describe("constants", () => {
  test("chat and review ratios sum to 1", () => {
    expect(CHAT_PANEL_RATIO + REVIEW_PANEL_RATIO).toBe(1)
  })

  test("min is less than default is less than max", () => {
    expect(CHAT_PANEL_MIN_RATIO).toBeLessThan(CHAT_PANEL_RATIO)
    expect(CHAT_PANEL_RATIO).toBeLessThan(CHAT_PANEL_MAX_RATIO)
  })
})

describe("chatPanelDefaultWidth", () => {
  test("returns 35% of the available width", () => {
    expect(chatPanelDefaultWidth(1000)).toBe(Math.floor(1000 * CHAT_PANEL_RATIO))
    expect(chatPanelDefaultWidth(1700)).toBe(595)
    expect(chatPanelDefaultWidth(3440)).toBe(1204)
  })
})

describe("chatPanelWidthMin", () => {
  test("returns 25% of the available width, floored at the pixel minimum", () => {
    expect(chatPanelWidthMin(1700)).toBe(Math.max(SESSION_PANEL_WIDTH_MIN, Math.floor(1700 * CHAT_PANEL_MIN_RATIO)))
    expect(chatPanelWidthMin(200)).toBe(SESSION_PANEL_WIDTH_MIN)
  })
})

describe("sessionPanelAvailableWidth", () => {
  test("subtracts the gap from the measured row width", () => {
    expect(sessionPanelAvailableWidth(1208, 8)).toBe(1200)
    expect(sessionPanelAvailableWidth(1200, 0)).toBe(1200)
  })
})

describe("sessionPanelWidthMax", () => {
  test("caps the chat panel at 55% of the available width so review gets at least 45%", () => {
    expect(sessionPanelWidthMax(1700)).toBe(Math.floor(1700 * CHAT_PANEL_MAX_RATIO))
  })

  test("lets the chat panel take 55% on wide screens", () => {
    const available = 3440
    expect(sessionPanelWidthMax(available)).toBe(Math.floor(3440 * CHAT_PANEL_MAX_RATIO))
  })

  test("never drops below the chat panel minimum on small windows", () => {
    expect(sessionPanelWidthMax(150)).toBe(SESSION_PANEL_WIDTH_MIN)
    expect(sessionPanelWidthMax(0)).toBe(SESSION_PANEL_WIDTH_MIN)
  })
})

describe("clampSessionPanelWidth", () => {
  test("keeps widths already within the limit", () => {
    expect(clampSessionPanelWidth({ width: 500, available: 1700 })).toBe(500)
  })

  test("forces the width down when the window shrinks above the minimum", () => {
    expect(clampSessionPanelWidth({ width: 1600, available: 1700 })).toBe(
      Math.floor(1700 * CHAT_PANEL_MAX_RATIO),
    )
  })

  test("enforces the 25% minimum when the stored width is too small", () => {
    const available = 1700
    const min = Math.max(SESSION_PANEL_WIDTH_MIN, Math.floor(available * CHAT_PANEL_MIN_RATIO))
    expect(clampSessionPanelWidth({ width: 50, available })).toBe(min)
  })

  test("skips clamping before the layout is measured", () => {
    expect(clampSessionPanelWidth({ width: 1600, available: undefined })).toBe(1600)
  })
})
