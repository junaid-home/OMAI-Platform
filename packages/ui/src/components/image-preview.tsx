import { Show } from "solid-js"
import { Dialog as Kobalte } from "@kobalte/core/dialog"
import { useI18n } from "../context/i18n"
import { useDialog } from "../context/dialog"
import { IconButton } from "./icon-button"

export interface ImagePreviewProps {
  src: string
  alt?: string
  mime?: string
}

export function ImagePreview(props: ImagePreviewProps) {
  const i18n = useI18n()
  const dialog = useDialog()
  const isVideo = () => !!props.mime?.startsWith("video/")
  return (
    <div data-component="image-preview">
      <button
        type="button"
        data-slot="image-preview-backdrop"
        tabIndex={-1}
        aria-label={i18n.t("ui.common.close")}
        onClick={() => dialog.close()}
      />
      <Kobalte.Content data-slot="image-preview-content">
        <Show when={isVideo()} fallback={<img src={props.src} alt={props.alt ?? i18n.t("ui.imagePreview.alt")} data-slot="image-preview-image" />}>
          <video src={props.src} data-slot="image-preview-image" controls autoplay loop muted />
        </Show>
        <Kobalte.CloseButton
          data-slot="image-preview-close"
          as={IconButton}
          icon="close"
          variant="ghost"
          aria-label={i18n.t("ui.common.close")}
        />
      </Kobalte.Content>
    </div>
  )
}
