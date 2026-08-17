import { useParams } from "@solidjs/router"
import { onCleanup } from "solid-js"
import { useCommand } from "@/context/command"
import { useLanguage } from "@/context/language"
import { useDialog } from "@omai/ui/context/dialog"
import { useAuth } from "@/context/auth"

export function useSettingsDialog(defaultValue?: string) {
  const dialog = useDialog()
  const params = useParams<{ id?: string }>()
  let run = 0
  let dead = false

  onCleanup(() => {
    dead = true
  })

  return () => {
    const current = ++run
    const sessionID = params.id
    void import("@/components/settings-v2").then((module) => {
      if (dead || run !== current) return
      void dialog.show(() => <module.DialogSettings sessionID={sessionID} defaultValue={defaultValue} />)
    })
  }
}

export function useSettingsCommand() {
  const command = useCommand()
  const language = useLanguage()
  const auth = useAuth()
  const show = useSettingsDialog()

  command.register("settings", () => [
    {
      id: "settings.open",
      title: language.t("command.settings.open"),
      category: language.t("command.category.settings"),
      keybind: "mod+comma",
      onSelect: show,
    },
    {
      id: "auth.logout",
      title: "Log out",
      category: language.t("command.category.settings"),
      onSelect: () => {
        auth.logout()
        window.location.href = "/login"
      },
    },
  ])

  return show
}
