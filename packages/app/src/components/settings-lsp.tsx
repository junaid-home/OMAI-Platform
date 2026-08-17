import { useLanguage } from "@/context/language"

export function SettingsLsp() {
  const language = useLanguage()

  return (
    <div class="flex flex-col gap-4 px-4 py-4">
      <div class="settings-v2-tab-header">
        <div class="settings-v2-tab-title">LSP</div>
      </div>
      <div class="settings-v2-tab-body">
        <div class="settings-v2-section flex items-center justify-center py-8 text-center">
          <div class="text-14-regular text-text-base">{language.t("dialog.lsp.empty")}</div>
        </div>
      </div>
    </div>
  )
}
