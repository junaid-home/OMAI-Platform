import { createSignal } from "solid-js"

const OS_NAME = (() => {
  if (navigator.userAgent.includes("Mac")) return "macos"
  if (navigator.userAgent.includes("Windows")) return "windows"
  if (navigator.userAgent.includes("Linux")) return "linux"
  return "unknown"
})()

const [webviewZoom, setWebviewZoom] = createSignal(1)
let requestedZoom = 1
let pinchZoomEnabled = false
let wheelPinch:
  | {
      active: boolean
      startZoom: number
      totalDelta: number
      timeout: ReturnType<typeof setTimeout> | undefined
    }
  | undefined

const MAX_ZOOM_LEVEL = 10
const MIN_ZOOM_LEVEL = 0.2
const WHEEL_PINCH_THRESHOLD = 20
const WHEEL_PINCH_STEP = 0.2
const WHEEL_PINCH_END_DELAY = 160

const clamp = (value: number) => Math.min(Math.max(value, MIN_ZOOM_LEVEL), MAX_ZOOM_LEVEL)

const applyZoom = (next: number) => {
  requestedZoom = next
  const root = document.getElementById("root")
  if (root) {
    if (next === 1) {
      root.style.zoom = ""
      root.style.width = ""
      root.style.height = ""
    } else {
      root.style.zoom = String(next)
      root.style.width = `${100 / next}vw`
      root.style.height = `${100 / next}vh`
    }
  }
  if (next === 1) {
    stopPortalObserver()
  } else {
    syncPortals()
    startPortalObserver()
  }
  const overflow = next === 1 ? "" : "hidden"
  document.documentElement.style.overflow = overflow
  document.body.style.overflow = overflow
  document.documentElement.style.setProperty("--browser-zoom", String(next))
  setWebviewZoom(next)
}

let portalObserver: MutationObserver | undefined

const syncPortals = () => {
  if (requestedZoom === 1) return
  const root = document.getElementById("root")
  for (let i = 0; i < document.body.children.length; i++) {
    const child = document.body.children[i] as HTMLElement
    if (child === root) continue
    child.style.zoom = String(requestedZoom)
  }
}

const startPortalObserver = () => {
  if (portalObserver) return
  portalObserver = new MutationObserver(syncPortals)
  portalObserver.observe(document.body, { childList: true })
}

const stopPortalObserver = () => {
  if (!portalObserver) return
  portalObserver.disconnect()
  portalObserver = undefined
}

const resetZoom = () => applyZoom(1)
const zoomIn = () => applyZoom(clamp(requestedZoom + 0.2))
const zoomOut = () => applyZoom(clamp(requestedZoom - 0.2))

const resetWheelPinch = () => {
  clearTimeout(wheelPinch?.timeout)
  wheelPinch = undefined
}

const normalizeWheelDelta = (event: WheelEvent) => {
  if (event.deltaMode === WheelEvent.DOM_DELTA_LINE) return event.deltaY * 16
  if (event.deltaMode === WheelEvent.DOM_DELTA_PAGE) return event.deltaY * window.innerHeight
  return event.deltaY
}

const updateWheelPinch = (event: WheelEvent) => {
  wheelPinch ??= {
    active: false,
    startZoom: requestedZoom,
    totalDelta: 0,
    timeout: undefined,
  }

  clearTimeout(wheelPinch.timeout)
  wheelPinch.timeout = setTimeout(resetWheelPinch, WHEEL_PINCH_END_DELAY)
  wheelPinch.totalDelta += normalizeWheelDelta(event)

  if (!wheelPinch.active && Math.abs(wheelPinch.totalDelta) < WHEEL_PINCH_THRESHOLD) return
  if (!wheelPinch.active) {
    wheelPinch.active = true
    wheelPinch.startZoom = requestedZoom
    wheelPinch.totalDelta = 0
    return
  }

  wheelPinch.active = true
  applyZoom(clamp(wheelPinch.startZoom - (wheelPinch.totalDelta / WHEEL_PINCH_THRESHOLD) * WHEEL_PINCH_STEP))
}

let zoomWheelHandler: ((event: WheelEvent) => void) | undefined

const attachZoomWheel = () => {
  if (zoomWheelHandler) return
  zoomWheelHandler = (event: WheelEvent) => {
    if (!event.ctrlKey) return
    event.preventDefault()
    updateWheelPinch(event)
  }
  window.addEventListener("wheel", zoomWheelHandler, { passive: false })
}

const detachZoomWheel = () => {
  if (!zoomWheelHandler) return
  window.removeEventListener("wheel", zoomWheelHandler, { passive: false } as EventListenerOptions)
  zoomWheelHandler = undefined
}

window.addEventListener("keydown", (event) => {
  if (!(OS_NAME === "macos" ? event.metaKey : event.ctrlKey)) return

  if (event.key === "-") {
    event.preventDefault()
    zoomOut()
    return
  }
  if (event.key === "=" || event.key === "+") {
    event.preventDefault()
    zoomIn()
    return
  }
  if (event.key === "0") {
    event.preventDefault()
    resetZoom()
  }
})

const getPinchZoomEnabled = (): boolean => pinchZoomEnabled

const setPinchZoomEnabled = (enabled: boolean) => {
  pinchZoomEnabled = enabled
  resetWheelPinch()
  if (enabled) {
    attachZoomWheel()
  } else {
    detachZoomWheel()
    if (requestedZoom !== 1) {
      applyZoom(1)
    }
  }
}

const handleWheelPinch = (deltaY: number, deltaMode: number) => {
  if (!pinchZoomEnabled) return
  const fakeEvent = { deltaY, deltaMode, ctrlKey: true } as WheelEvent
  updateWheelPinch(fakeEvent)
}

export { webviewZoom, getPinchZoomEnabled, setPinchZoomEnabled, handleWheelPinch }
