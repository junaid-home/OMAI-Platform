import { mkdir, writeFile } from "node:fs/promises"
import { chromium } from "@playwright/test"

const portalURL = process.env.OMAI_E2E_APP_URL ?? "http://omai-app"
const apiURL = process.env.OMAI_E2E_API_URL ?? "http://omai:8787"
const token = process.env.OMAI_E2E_API_TOKEN
const output = process.env.OMAI_E2E_BROWSER_OUTPUT ?? "/tmp/omai-browser-e2e"

if (!token) throw new Error("OMAI_E2E_API_TOKEN is required")

await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true })
const page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
const pageErrors = []
const consoleErrors = []
const failedRequests = []

page.on("pageerror", (error) => pageErrors.push(error.message))
page.on("console", (message) => {
  if (message.type() === "error") consoleErrors.push(message.text())
})
page.on("requestfailed", (request) => {
  failedRequests.push({ url: request.url(), error: request.failure()?.errorText ?? "unknown" })
})

try {
  const response = await page.goto(portalURL, { waitUntil: "domcontentloaded", timeout: 60_000 })
  if (!response?.ok()) throw new Error(`Portal returned HTTP ${response?.status() ?? 0}`)
  await page.waitForTimeout(5_000)

  const portal = await page.evaluate(() => ({
    title: document.title,
    bodyTextBytes: new TextEncoder().encode(document.body.innerText).byteLength,
    bodyHTMLBytes: new TextEncoder().encode(document.body.innerHTML).byteLength,
    rootChildren: document.body.children.length,
  }))
  const health = await page.evaluate(
    async ({ endpoint, credential }) => {
      const response = await fetch(`${endpoint}/uab.v1.ControlPlaneService/Health`, {
        method: "POST",
        headers: {
          Authorization: `Bearer ${credential}`,
          "Connect-Protocol-Version": "1",
          "Content-Type": "application/json",
        },
        body: "{}",
      })
      return { status: response.status, body: await response.json() }
    },
    { endpoint: apiURL, credential: token },
  )

  await page.screenshot({ path: `${output}/portal.png`, fullPage: true })
  const result = { portalURL, apiURL, portal, health, pageErrors, consoleErrors, failedRequests }
  await writeFile(`${output}/browser.json`, `${JSON.stringify(result, null, 2)}\n`)

  if (portal.rootChildren === 0 || portal.bodyHTMLBytes < 100) throw new Error("Portal rendered no application shell")
  if (health.status !== 200 || health.body.ok !== true) throw new Error("Browser-to-Go ConnectRPC health failed")
  if (pageErrors.length > 0) throw new Error(`Portal emitted page errors: ${pageErrors.join(" | ")}`)
} finally {
  await browser.close()
}

console.log("Portal browser, CORS and ConnectRPC smoke passed")
