import { LAUNCHER_PX, launcherRingRadius } from './hubGeometry'

export interface Camera { k: number, tx: number, ty: number }
export type HubLevel = 0 | 1 | 2

export const MIN_REL = 0.6
export const MAX_REL = 14
export const LEVEL_TOPICS = 1.8
export const LEVEL_NOTES = 4
export const DOCK_REL = 1.5
export const FLY_MS = 480
export const LEVEL_TARGETS: Record<HubLevel, number> = { 0: 1, 1: 2.4, 2: 5.5 }

// Reserves 110px for the controls and 150px for the queue when fitting the disc (radius 490).
export function fitScale(width: number, height: number): number {
  return Math.max(0.05, Math.min(width - 110, height - 150) / (2 * 490))
}

export function levelOf(rel: number): HubLevel {
  return rel < LEVEL_TOPICS ? 0 : rel < LEVEL_NOTES ? 1 : 2
}

export function clampScale(k: number, k0: number): number {
  return Math.max(k0 * MIN_REL, Math.min(k0 * MAX_REL, k))
}

export function toScreen(cam: Camera, x: number, y: number): [number, number] {
  return [x * cam.k + cam.tx, y * cam.k + cam.ty]
}

export function toWorld(cam: Camera, sx: number, sy: number): [number, number] {
  return [(sx - cam.tx) / cam.k, (sy - cam.ty) / cam.k]
}

export function zoomAt(cam: Camera, factor: number, mx: number, my: number, k0: number): Camera {
  const k = clampScale(cam.k * factor, k0)
  const q = k / cam.k
  return { k, tx: mx - (mx - cam.tx) * q, ty: my - (my - cam.ty) * q }
}

export function centredOn(wx: number, wy: number, k: number, width: number, height: number): Camera {
  return { k, tx: width / 2 - wx * k, ty: height / 2 - wy * k }
}

export function easeInOut(p: number): number {
  return p < 0.5 ? 2 * p * p : 1 - (-2 * p + 2) ** 2 / 2
}

// Zoom interpolates in log space so a 1×→10× flight does not spend its first half barely moving.
export function flyFrame(from: Camera, to: { wx: number, wy: number, k: number }, width: number, height: number, p: number): Camera {
  if (p >= 1)
    return centredOn(to.wx, to.wy, to.k, width, height)
  const q = easeInOut(Math.max(0, p))
  const [cx, cy] = toWorld(from, width / 2, height / 2)
  const k = Math.exp(Math.log(from.k) + (Math.log(to.k) - Math.log(from.k)) * q)
  return centredOn(cx + (to.wx - cx) * q, cy + (to.wy - cy) * q, k, width, height)
}

// The ring grows with the legend it clears and with the zoom, so it docks once it no longer fits the stage.
export function launchersDocked(rel: number, k0: number, agentRingPx: number, stagePx: number): boolean {
  return rel > DOCK_REL || launcherRingRadius(k0, agentRingPx) * k0 * rel > stagePx / 2 - LAUNCHER_PX / 2
}
