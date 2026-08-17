import type { ParentProps } from "solid-js"

export default function AuthLayout(props: ParentProps) {
  return (
    <div class="min-h-screen bg-background-base">
      {props.children}
    </div>
  )
}
