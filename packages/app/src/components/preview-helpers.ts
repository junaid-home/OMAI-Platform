import type { useFile } from "@/context/file"

type FileContext = ReturnType<typeof useFile>

function extractRelativePaths(html: string): string[] {
  const paths: string[] = []
  const linkRe = /<link\s+[^>]*rel=["']stylesheet["'][^>]*href=["']([^"']+)["']/gi
  const scriptRe = /<script\s+[^>]*src=["']([^"']+)["']/gi
  let match
  while ((match = linkRe.exec(html))) {
    if (!match[1].startsWith("http") && !match[1].startsWith("//")) paths.push(match[1])
  }
  while ((match = scriptRe.exec(html))) {
    if (!match[1].startsWith("http") && !match[1].startsWith("//")) paths.push(match[1])
  }
  return paths
}

function inlineAssets(html: string, assets: Map<string, string>): string {
  let result = html
  result = result.replace(
    /<link\s+([^>]*?)href=["']([^"']+)["']([^>]*?)>/gi,
    (full, _before, href, _after) => {
      if (href.startsWith("http") || href.startsWith("//")) return full
      const content = assets.get(href)
      if (!content) return full
      return `<style>/* ${href} */\n${content}\n</style>`
    },
  )
  result = result.replace(
    /<script\s+([^>]*?)src=["']([^"']+)["']([^>]*?)>\s*<\/script>/gi,
    (full, _before, src, _after) => {
      if (src.startsWith("http") || src.startsWith("//")) return full
      const content = assets.get(src)
      if (!content) return full
      return `<script>/* ${src} */\n${content}\n</script>`
    },
  )
  return result
}

export async function loadPreviewHtml(file: FileContext): Promise<string | undefined> {
  const state = file.get("index.html")
  if (!state?.loaded || !state.content) return undefined
  if (state.content.type === "binary") return undefined
  const html = state.content.content

  const paths = extractRelativePaths(html)
  if (paths.length === 0) return html

  const assets = new Map<string, string>()
  await Promise.all(
    paths.map((p) =>
      file.load(p, { force: true }).then(() => {
        const s = file.get(p)
        if (s?.content && s.content.type === "text") {
          assets.set(p, s.content.content)
        }
      }),
    ),
  )
  return inlineAssets(html, assets)
}
