# media/

Images referenced by [`../README.md`](../README.md) and the app itself
(`logo.png`, favicons, PWA icons — those are `go:embed`ded, see
`internal/web`). Screenshots, `badges-status.png`, and `preview.gif` are
for the README only — nothing matching `SCR_*.png` or `preview.gif` is
built into the binary.

## Screenshots

Full-window screenshots of the running app, one per sidebar section,
named `SCR_<Section>_<NN>.png` (`NN` lets a section have more than one —
see `SCR_Containers_01.png`/`SCR_Containers_02.png`). Dark theme is the
default DoUpRo ships with — screenshot that unless you specifically want
to show the light theme off. Native resolution/window size doesn't need
to match between screenshots; `preview.gif` normalizes that (see below).

`badges-status.png` is a close crop of the seven container-state badges
(Created/Dead/Exited/Paused/Restarting/Running/Warning) shown side by
side — referenced directly in the README under "What it looks like", not
part of the GIF.

## Generating `preview.gif`

The README embeds a single `media/preview.gif` that cycles through the
screenshots above instead of a long vertical stack of PNGs — same visual
coverage, a fraction of the scroll space, and GitHub renders an animated
GIF inline with no extra markup.

The screenshots come in at different native resolutions (different
browser window sizes/scroll positions when they were taken), so each one
is first normalized to the same canvas — scaled to a fixed width, then
cropped to a fixed height from the top — before being assembled into the
GIF. Skipping this step makes the GIF visibly jump in size frame to
frame.

```bash
# 1. Normalize every screenshot to the same 1400x900 canvas, in the
#    order they should appear in the GIF.
mkdir -p /tmp/doupro-gif-frames
i=1
for f in SCR_Containers_01.png SCR_Containers_02.png SCR_Schedule_01.png \
         SCR_Notifications_01.png SCR_Logs_01.png SCR_Stats_01.png; do
  n=$(printf "%02d" $i)
  ffmpeg -y -i "media/$f" -vf "scale=1400:-1,crop=1400:900:0:0" \
    "/tmp/doupro-gif-frames/frame_${n}.png"
  i=$((i+1))
done

# 2. Build the GIF (two-pass palette for quality/size) — 2.2s per frame.
ffmpeg -y -framerate 1/2.2 -i /tmp/doupro-gif-frames/frame_%02d.png \
  -vf palettegen /tmp/doupro-gif-frames/palette.png
ffmpeg -y -framerate 1/2.2 -i /tmp/doupro-gif-frames/frame_%02d.png \
  -i /tmp/doupro-gif-frames/palette.png -lavfi paletteuse -loop 0 \
  media/preview.gif

rm -rf /tmp/doupro-gif-frames
```

- `crop=1400:900:0:0` takes the top-left 1400×900 of the scaled image —
  representative of each page (header + first screenful of content)
  without the long trailing whitespace some pages have below the fold.
  If a screenshot is short enough that scaling to 1400 wide leaves it
  under 900 tall, this crop would fail — add `,pad=1400:900:0:0:0x030712`
  (DoUpRo's dark-theme background color) after the scale filter to
  letterbox instead of cropping in that case.
- `-framerate 1/2.2` holds each frame on screen for 2.2 seconds — adjust
  to taste (lower = longer hold).
- The two-pass `palettegen`/`paletteuse` gives noticeably better color
  quality than a single-pass GIF encode, at no extra tooling cost (only
  `ffmpeg`, no `gifski`/ImageMagick needed).

Check the result isn't huge before committing it — GitHub renders GIFs
inline regardless of size, but a slideshow like this should comfortably
stay under 1MB. If it doesn't, drop the canvas width or hold each frame
longer with fewer, larger steps.

### Simpler alternative: ImageMagick only

If you'd rather not use `ffmpeg`, ImageMagick's `magick` can build the
GIF directly from already-same-sized PNGs, no intermediate video:

```bash
brew install imagemagick
magick -delay 220 -loop 0 media/SCR_*.png -resize 1400x media/preview.gif
```

Skips the normalize step above, so it only looks right if the source
screenshots already share the same aspect ratio — otherwise each frame
gets independently resized and the GIF will visibly jump in dimensions.
