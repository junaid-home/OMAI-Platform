import { fileURLToPath } from "node:url"
import { sentryVitePlugin } from "@sentry/vite-plugin"
import { defineConfig, loadEnv } from "vite"
import desktopPlugin from "./vite"

const repoRoot = fileURLToPath(new URL("../..", import.meta.url))

const sentry =
  process.env.SENTRY_AUTH_TOKEN && process.env.SENTRY_ORG && process.env.SENTRY_PROJECT
    ? sentryVitePlugin({
        authToken: process.env.SENTRY_AUTH_TOKEN,
        org: process.env.SENTRY_ORG,
        project: process.env.SENTRY_PROJECT,
        telemetry: false,
        release: {
          name: process.env.SENTRY_RELEASE ?? process.env.VITE_SENTRY_RELEASE,
        },
        sourcemaps: {
          assets: "./dist/**",
          filesToDeleteAfterUpload: "./dist/**/*.map",
        },
      })
    : false

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, repoRoot, "")
  const serverOrigin = `http://${env.VITE_OPENCODE_SERVER_HOST ?? "localhost"}:${env.VITE_OPENCODE_SERVER_PORT ?? "4096"}`
  return {
    envDir: repoRoot,
    plugins: [desktopPlugin, sentry] as any,
    server: {
      host: "0.0.0.0",
      allowedHosts: true,
      port: 3000,
      proxy: {
        // Local image files referenced in markdown render via the dev origin.
        "/file/image": {
          target: serverOrigin,
          changeOrigin: true,
        },
      },
    },
    build: {
      target: "esnext",
      sourcemap: true,
    },
  }
})
