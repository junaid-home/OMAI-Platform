import { type JSX } from "solid-js"

export function BrandLogo(props: {
  size?: "small" | "medium" | "large" | "extra-large" | "huge"
  class?: string
  alt?: string
}): JSX.Element {
  const sizeClasses = {
    small: "w-8 h-8",
    medium: "w-16 h-16",
    large: "w-16 h-16",
    "extra-large": "w-19 h-19",
    huge: "w-32 h-32",
  }

  return (
    <img
      src="/icon.jpeg"
      alt={props.alt || "Logo"}
      class={`${sizeClasses[props.size || "medium"]} rounded-xl object-cover ${props.class || ""}`}
    />
  )
}
