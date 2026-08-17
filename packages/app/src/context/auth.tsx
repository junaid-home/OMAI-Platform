import { createContext, createMemo, createEffect, createSignal, type ParentProps, useContext } from "solid-js"
import { useServer } from "@/context/server"

interface AuthUser {
  id: string
  username: string
  email: string
}

interface AuthContextValue {
  user: () => AuthUser | null
  token: () => string | null
  isAuthenticated: () => boolean
  loading: () => boolean
  login: (usernameOrEmail: string, password: string) => Promise<{ error?: string }>
  signup: (username: string, email: string, password: string) => Promise<{ error?: string }>
  logout: () => void
}

const AuthContext = createContext<AuthContextValue>()

const TOKEN_KEY = "omai_auth_token"
const USER_KEY = "omai_auth_user"

function getStoredToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY)
  } catch {
    return null
  }
}

function getStoredUser(): AuthUser | null {
  try {
    const raw = localStorage.getItem(USER_KEY)
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}

export function AuthProvider(props: ParentProps) {
  const server = useServer()
  const [token, setToken] = createSignal<string | null>(getStoredToken())
  const [user, setUser] = createSignal<AuthUser | null>(getStoredUser())
  const [loading, setLoading] = createSignal(true)

  const isAuthenticated = createMemo(() => !!token() && !!user())

  createEffect(() => {
    token()
    user()
    setLoading(false)
  })

  const hardcodedUsers: { user: AuthUser; token: string; password: string }[] = [
    {
      user: {
        id: "3b996485-bf2d-440a-9ce1-1d6af5ff1169",
        username: "junaid",
        email: "jj123dev@gmail.com",
      },
      token:
        "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIzYjk5NjQ4NS1iZjJkLTQ0MGEtOWNlMS0xZDZhZjVmZjExNjkiLCJ1c2VybmFtZSI6Imp1bmFpZGF2ZWQiLCJlbWFpbCI6Imp1bmFpZGF2ZWQud29ya0BnbWFpbC5jb20iLCJpYXQiOjE3ODY3MTUwNzksImV4cCI6MTc4NjgwMTQ3OX0.placeholder_signature",
      password: "changeme",
    },
    {
      user: {
        id: "4c107596-cg3e-551b-adf2-2e7bg6ff2280",
        username: "omnaich",
        email: "omnaich@gmail.com",
      },
      token:
        "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiI0YzEwNzU5Ni1jZzNlLTU1MWItYWRmMi0yZTdiZzZmZjIyODAiLCJ1c2VybmFtZSI6Im9tbmFpY2giLCJlbWFpbCI6Im9tbmFpY2hAZ21haWwuY29tIiwiaWF0IjoxNzg2NzE1MDc5LCJleHAiOjE3ODY4MDE0Nzl9.placeholder_signature",
      password: "K9#mP2$vX8!q",
    },
  ]

  const login = async (usernameOrEmail: string, password: string): Promise<{ error?: string }> => {
    const query = usernameOrEmail.toLowerCase()
    const entry = hardcodedUsers.find(
      (u) => u.user.username.toLowerCase() === query || u.user.email.toLowerCase() === query,
    )
    if (!entry) return { error: "Invalid username or password" }
    if (entry.password !== password) return { error: "Invalid username or password" }

    localStorage.setItem(TOKEN_KEY, entry.token)
    localStorage.setItem(USER_KEY, JSON.stringify(entry.user))
    setToken(entry.token)
    setUser(entry.user)
    return {}
  }

  const signup = async (username: string, email: string, password: string): Promise<{ error?: string }> => {
    const serverUrl = server.current?.http.url
    if (!serverUrl) return { error: "Server not available" }

    try {
      const res = await fetch(`${serverUrl}/auth/signup`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, email, password }),
      })
      const data = await res.json()
      if (!res.ok) return { error: data.error || "Signup failed" }

      localStorage.setItem(TOKEN_KEY, data.token)
      localStorage.setItem(USER_KEY, JSON.stringify(data.user))
      setToken(data.token)
      setUser(data.user)
      return {}
    } catch (err) {
      return { error: err instanceof Error ? err.message : "Network error" }
    }
  }

  const logout = () => {
    localStorage.removeItem(TOKEN_KEY)
    localStorage.removeItem(USER_KEY)
    setToken(null)
    setUser(null)
  }

  return (
    <AuthContext.Provider value={{ user, token, isAuthenticated, loading, login, signup, logout }}>
      {props.children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error("useAuth must be used within AuthProvider")
  return ctx
}
