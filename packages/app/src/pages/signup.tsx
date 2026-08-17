import { createSignal } from "solid-js"
import { useNavigate } from "@solidjs/router"
import { Button } from "@omai/ui/button"
import { TextField } from "@omai/ui/text-field"
import { useAuth } from "@/context/auth"
import { useLanguage } from "@/context/language"
import { BrandLogo } from "@/components/brand-logo"

export default function SignupPage() {
  const auth = useAuth()
  const language = useLanguage()
  const navigate = useNavigate()
  const [username, setUsername] = createSignal("")
  const [email, setEmail] = createSignal("")
  const [password, setPassword] = createSignal("")
  const [confirmPassword, setConfirmPassword] = createSignal("")
  const [error, setError] = createSignal("")
  const [loading, setLoading] = createSignal(false)

  const handleSubmit = async (e: SubmitEvent) => {
    e.preventDefault()
    setError("")

    if (!username() || !email() || !password()) {
      setError(language.t("auth.signup.error.required"))
      return
    }
    if (username().length < 3 || username().length > 30) {
      setError(language.t("auth.signup.error.usernameLength"))
      return
    }
    if (!email().includes("@")) {
      setError(language.t("auth.signup.error.emailInvalid"))
      return
    }
    if (password().length < 6) {
      setError(language.t("auth.signup.error.passwordLength"))
      return
    }
    if (password() !== confirmPassword()) {
      setError(language.t("auth.signup.error.passwordMismatch"))
      return
    }

    setLoading(true)
    const result = await auth.signup(username(), email(), password())
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
          <div class="text-18-semibold text-text-strong">{language.t("auth.signup.title")}</div>
          <div class="text-14-regular text-text-weak">{language.t("auth.signup.subtitle")}</div>
        </div>
        <form onSubmit={handleSubmit} class="flex flex-col gap-4">
          <TextField
            label={language.t("auth.signup.username.label")}
            placeholder={language.t("auth.signup.username.placeholder")}
            value={username()}
            onChange={(v) => { setUsername(v); setError("") }}
            validationState={error() ? "invalid" : undefined}
            error={error()}
          />
          <TextField
            label={language.t("auth.signup.email.label")}
            type="email"
            placeholder={language.t("auth.signup.email.placeholder")}
            value={email()}
            onChange={(v) => { setEmail(v); setError("") }}
            validationState={error() ? "invalid" : undefined}
          />
          <TextField
            label={language.t("auth.signup.password.label")}
            type="password"
            placeholder="••••••••"
            value={password()}
            onChange={(v) => { setPassword(v); setError("") }}
            validationState={error() ? "invalid" : undefined}
          />
          <TextField
            label={language.t("auth.signup.confirmPassword.label")}
            type="password"
            placeholder="••••••••"
            value={confirmPassword()}
            onChange={(v) => { setConfirmPassword(v); setError("") }}
            validationState={error() ? "invalid" : undefined}
          />
          <Button type="submit" variant="primary" size="large" disabled={loading()}>
            {loading() ? language.t("auth.signup.submitting") : language.t("auth.signup.submit")}
          </Button>
        </form>
        <div class="mt-6 text-center text-14-regular text-text-weak">
          {language.t("auth.signup.hasAccount")}{" "}
          <a href="/login" class="text-accent-base hover:underline cursor-pointer">{language.t("auth.signup.login")}</a>
        </div>
      </div>
    </div>
  )
}
