import { createSignal, Show } from "solid-js"
import { useNavigate } from "@solidjs/router"
import { Button } from "@omai/ui/button"
import { TextField } from "@omai/ui/text-field"
import { useAuth } from "@/context/auth"
import { useLanguage } from "@/context/language"
import { BrandLogo } from "@/components/brand-logo"

export default function LoginPage() {
  const auth = useAuth()
  const language = useLanguage()
  const navigate = useNavigate()
  const [identifier, setIdentifier] = createSignal("")
  const [password, setPassword] = createSignal("")
  const [error, setError] = createSignal("")
  const [loading, setLoading] = createSignal(false)

  const handleSubmit = async (e: SubmitEvent) => {
    e.preventDefault()
    if (!identifier() || !password()) {
      setError(language.t("auth.login.error.required"))
      return
    }
    setLoading(true)
    setError("")
    const result = await auth.login(identifier(), password())
    setLoading(false)
    if (result.error) {
      setError(result.error)
    } else {
      navigate("/")
    }
  }

  return (
    <div class="min-h-screen flex items-center justify-center bg-background-base relative overflow-hidden">
      <div class="absolute inset-0 backdrop-blur-xl bg-background-base/80" />
      <div class="relative z-10 w-full max-w-sm mx-auto px-6">
        <div class="flex flex-col items-center gap-6 mb-8">
          <BrandLogo size="large" />
          <div class="text-18-semibold text-text-strong">{language.t("auth.login.title")}</div>
          <div class="text-14-regular text-text-weak">{language.t("auth.login.subtitle")}</div>
        </div>
        <form onSubmit={handleSubmit} class="flex flex-col gap-4">
          <TextField
            label={language.t("auth.login.username.label")}
            placeholder={language.t("auth.login.username.placeholder")}
            value={identifier()}
            onChange={(v) => {
              setIdentifier(v)
              setError("")
            }}
            validationState={error() ? "invalid" : undefined}
            error={error()}
          />
          <TextField
            label={language.t("auth.login.password.label")}
            type="password"
            placeholder="••••••••"
            value={password()}
            onChange={(v) => {
              setPassword(v)
              setError("")
            }}
            validationState={error() ? "invalid" : undefined}
          />
          <Button type="submit" variant="primary" size="large" disabled={loading()}>
            {loading() ? language.t("auth.login.submitting") : language.t("auth.login.submit")}
          </Button>
        </form>
        <div class="mt-6 text-center text-14-regular text-text-weak">
          {/* {language.t("auth.login.noAccount")}{" "}
          <a href="/signup" class="text-accent-base hover:underline cursor-pointer">{language.t("auth.login.signup")}</a> */}
          {/* {language.t("auth.login.stealthMode")} */}
          STEALTH MODE
        </div>
      </div>
    </div>
  )
}
