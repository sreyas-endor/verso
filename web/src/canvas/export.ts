// export.ts — milestone 12: the final canvas as a PNG.
//
// The vectors are re-rendered at 2x. The display bitmap is never drawImage-
// upscaled: it would resample pixels that were already discarded, and
// imageSmoothingQuality cannot invent them back.
//
// Encoding runs in a worker. This is the one genuine reason to reach for
// OffscreenCanvas: Firefox encodes toBlob on a background pool, Chrome on the
// main thread in idle chunks, and WebKit SYNCHRONOUSLY on the calling thread —
// so a main-thread export janks Safari for the whole encode. Rasterising stays
// on the main thread (it is fast, and live drawing must never move off-thread:
// pointer events arrive here, and an extra hop is the one cost the artist's ink
// cannot pay). Only the ImageBitmap crosses to the worker.
//
// Other traps from §7, all handled below:
//   - there is no promise-based toBlob; the IDL returns undefined and takes a
//     callback. convertToBlob is the only promise variant.
//   - the transparency trap: {alpha:false} yields opaque BLACK, and alpha-less
//     formats composite onto black. White is filled explicitly instead.
//   - toBlob is preferred over toDataURL: base64 is +33%, built synchronously,
//     and a 2048x1536 PNG becomes a multi-megabyte string.
//   - quality is ignored for PNG, and image/webp silently falls back to PNG in
//     Safari — so the caller names the file from blob.type, never from a guess.
//   - navigator.share is gated on canShare({files}); Firefox never supports file
//     sharing and would otherwise throw on a valid-looking call.

import { renderStroke } from "./geometry.js";
import { LOGICAL_H, LOGICAL_W } from "./grid.js";
import { PAPER } from "./surface.js";

export interface ExportStroke {
  readonly colorIndex: number;
  readonly width: number;
  readonly points: readonly number[];
}

type AnyCtx = CanvasRenderingContext2D | OffscreenCanvasRenderingContext2D;

// Transfers a bitmap in, hands a PNG blob back. Kept to a handful of lines so
// it can live inline: a separate entry point would couple this module to the
// bundler's worker plumbing for no benefit.
const WORKER_SRC = `
self.onmessage = function (e) {
  var bitmap = e.data && e.data.bitmap;
  if (!bitmap) { self.postMessage({ error: "no bitmap" }); return; }
  try {
    var c = new OffscreenCanvas(bitmap.width, bitmap.height);
    var g = c.getContext("bitmaprenderer");
    if (!g) throw new Error("bitmaprenderer unavailable");
    g.transferFromImageBitmap(bitmap);
    c.convertToBlob({ type: "image/png" }).then(
      function (b) { self.postMessage({ blob: b }); },
      function (err) { self.postMessage({ error: String(err) }); }
    );
  } catch (err) {
    self.postMessage({ error: String(err) });
  }
};
`;

/**
 * Re-render a finished canvas from its vectors onto any 2D context, scaled
 * from the logical 1024x768 grid.
 *
 * Exported because the final reveal shows several rounds at once and the live
 * engine owns one surface. A finished round is only ever its stroke log, so
 * repainting it into a plain `<canvas>` is the whole job — no second engine,
 * no bitmap kept alive per round.
 */
export function paint(ctx: AnyCtx, strokes: readonly ExportStroke[], scale: number): void {
  ctx.setTransform(scale, 0, 0, scale, 0, 0);
  // Always fill white first. A transparent PNG dropped into any dark viewer is
  // an invisible drawing, and every alpha-less encoder composites onto black.
  ctx.fillStyle = PAPER;
  ctx.fillRect(0, 0, LOGICAL_W, LOGICAL_H);
  for (const s of strokes) {
    renderStroke(ctx, s.points, s.colorIndex, s.width);
  }
}

function encodeInWorker(bitmap: ImageBitmap): Promise<Blob> {
  return new Promise<Blob>((resolve, reject) => {
    const url = URL.createObjectURL(new Blob([WORKER_SRC], { type: "text/javascript" }));
    let worker: Worker;
    try {
      worker = new Worker(url);
    } catch (err) {
      URL.revokeObjectURL(url);
      reject(err instanceof Error ? err : new Error(String(err)));
      return;
    }
    const settle = (): void => {
      worker.terminate();
      URL.revokeObjectURL(url);
    };
    worker.onmessage = (ev: MessageEvent): void => {
      settle();
      const data = ev.data as { blob?: Blob; error?: string };
      if (data.blob instanceof Blob) resolve(data.blob);
      else reject(new Error(data.error ?? "worker encode failed"));
    };
    worker.onerror = (): void => {
      settle();
      reject(new Error("worker encode failed"));
    };
    worker.postMessage({ bitmap }, [bitmap]);
  });
}

function encodeOnMainThread(strokes: readonly ExportStroke[], scale: number): Promise<Blob> {
  const canvas = document.createElement("canvas");
  canvas.width = LOGICAL_W * scale;
  canvas.height = LOGICAL_H * scale;
  const ctx = canvas.getContext("2d");
  if (!ctx) return Promise.reject(new Error("verso: 2D canvas context unavailable"));
  paint(ctx, strokes, scale);
  return new Promise<Blob>((resolve, reject) => {
    canvas.toBlob((blob) => {
      if (blob) resolve(blob);
      else reject(new Error("verso: toBlob produced nothing"));
    }, "image/png");
  });
}

/**
 * Re-render every committed stroke at `scale` and encode it as a PNG.
 *
 * Check `blob.type` before naming the file: Safari substitutes PNG for formats
 * it cannot encode without telling anyone.
 */
export async function renderPng(
  strokes: readonly ExportStroke[],
  scale = 2,
): Promise<Blob> {
  if (typeof OffscreenCanvas !== "undefined" && typeof Worker !== "undefined") {
    try {
      const off = new OffscreenCanvas(LOGICAL_W * scale, LOGICAL_H * scale);
      const ctx = off.getContext("2d");
      if (ctx) {
        paint(ctx, strokes, scale);
        // transferToImageBitmap empties the source canvas, so a failure past
        // this line falls through to a fresh render rather than a blank one.
        return await encodeInWorker(off.transferToImageBitmap());
      }
    } catch {
      // Fall through: no OffscreenCanvas 2D, no bitmaprenderer, blocked worker.
    }
  }
  if (typeof OffscreenCanvas !== "undefined") {
    try {
      const off = new OffscreenCanvas(LOGICAL_W * scale, LOGICAL_H * scale);
      const ctx = off.getContext("2d");
      if (ctx) {
        paint(ctx, strokes, scale);
        return await off.convertToBlob({ type: "image/png" });
      }
    } catch {
      // Fall through to the HTMLCanvasElement path.
    }
  }
  return encodeOnMainThread(strokes, scale);
}

export type SaveOutcome = "shared" | "downloaded";

/**
 * Hand the PNG to the viewer. iOS gets the share sheet, which offers "Save
 * Image"; everything else gets a download. `canShare({files})` is the only
 * reliable gate — Firefox exposes navigator.share and refuses files.
 */
export async function savePng(blob: Blob, baseName = "verso-canvas"): Promise<SaveOutcome> {
  const ext = blob.type === "image/png" ? "png" : (blob.type.split("/")[1] ?? "png");
  const filename = `${baseName}.${ext}`;

  if (typeof File === "function" && typeof navigator.canShare === "function") {
    const file = new File([blob], filename, { type: blob.type });
    if (navigator.canShare({ files: [file] })) {
      try {
        await navigator.share({ files: [file], title: "Verso canvas" });
        return "shared";
      } catch (err) {
        // A cancelled share sheet is a completed interaction, not a failure.
        if (err instanceof DOMException && err.name === "AbortError") return "shared";
      }
    }
  }

  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.rel = "noopener";
  document.body.append(a);
  a.click();
  a.remove();
  // Revoking synchronously races the navigation in Safari.
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
  return "downloaded";
}
