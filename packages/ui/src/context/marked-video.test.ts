import { expect, test } from "bun:test"
import { isVideoUrl, localVideoUrl } from "./marked"

test("isVideoUrl recognizes playable video links", () => {
  expect(isVideoUrl("https://v3b.fal.media/files/b/0aa52883/C4iXb2TAAZJYfdvB1fRHR_video.mp4")).toBe(true)
  expect(isVideoUrl("https://cdn.example.com/clip.webm")).toBe(true)
  expect(isVideoUrl("https://cdn.example.com/clip.MP4")).toBe(true)
  expect(isVideoUrl("data:video/mp4;base64,AAAA")).toBe(true)
  expect(isVideoUrl("https://cdn.example.com/clip.ogg")).toBe(true)
  expect(isVideoUrl("https://youtube.com/watch?v=abc")).toBe(false)
  expect(isVideoUrl("https://cdn.example.com/clip.txt")).toBe(false)
  expect(isVideoUrl("not a url")).toBe(false)
})

test("localVideoUrl maps file paths to the video route", () => {
  expect(localVideoUrl("/tmp/clip.mp4")).toBe("/file/video?path=%2Ftmp%2Fclip.mp4")
  expect(localVideoUrl("https://cdn.example.com/clip.mp4")).toBe("https://cdn.example.com/clip.mp4")
})
