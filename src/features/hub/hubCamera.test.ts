import { describe, expect, it } from 'vitest'
import {
  centredOn,
  clampScale,
  DOCK_REL,
  fitScale,
  flyFrame,
  launchersDocked,
  levelOf,
  MAX_REL,
  MIN_REL,
  toScreen,
  toWorld,
  zoomAt,
} from './hubCamera'
import { LAUNCHER_PX, launcherRingRadius } from './hubGeometry'

describe('levelOf', () => {
  it('switches at 1.8 and 4', () => {
    expect(levelOf(1.79)).toBe(0)
    expect(levelOf(1.8)).toBe(1)
    expect(levelOf(3.99)).toBe(1)
    expect(levelOf(4)).toBe(2)
  })
})

describe('camera maths', () => {
  const cam = { k: 2, tx: 100, ty: 50 }
  it('round-trips screen and world', () => {
    const [sx, sy] = toScreen(cam, 12, -7)
    expect(toWorld(cam, sx, sy)).toEqual([12, -7])
  })
  it('keeps the point under the pointer fixed while zooming', () => {
    const before = toWorld(cam, 300, 200)
    const next = zoomAt(cam, 1.5, 300, 200, 1)
    const after = toWorld(next, 300, 200)
    expect(after[0]).toBeCloseTo(before[0])
    expect(after[1]).toBeCloseTo(before[1])
  })
  it('clamps to 0.6×–14× of fit', () => {
    expect(clampScale(0.01, 1)).toBe(MIN_REL)
    expect(clampScale(99, 1)).toBe(MAX_REL)
    expect(zoomAt({ k: 14, tx: 0, ty: 0 }, 2, 0, 0, 1).k).toBe(14)
  })
  it('fits the world disc into the stage', () => {
    expect(fitScale(1090, 1130)).toBeCloseTo(1)
    expect(fitScale(10, 10)).toBeGreaterThan(0)
  })
  it('flies from the start to exactly the target', () => {
    const from = { k: 1, tx: 0, ty: 0 }
    const end = flyFrame(from, { wx: 40, wy: -20, k: 3 }, 800, 600, 1)
    expect(end).toEqual(centredOn(40, -20, 3, 800, 600))
    const start = flyFrame(from, { wx: 40, wy: -20, k: 3 }, 800, 600, 0)
    expect(start.k).toBeCloseTo(1)
  })
})

describe('launchersDocked', () => {
  // The stage that just holds the ring: its radius plus half a launcher button.
  const fits = (k0: number, ring: number, rel = 1) => 2 * (launcherRingRadius(k0, ring) * k0 * rel + LAUNCHER_PX / 2)

  it('keeps the ring while it fits the stage and the camera is below the dock zoom', () => {
    expect(launchersDocked(1, 0.57, 196, fits(0.57, 196) + 1)).toBe(false)
    expect(launchersDocked(DOCK_REL, 0.57, 196, fits(0.57, 196, DOCK_REL) + 1)).toBe(false)
  })
  it('docks beyond the dock zoom', () => {
    expect(launchersDocked(DOCK_REL + 0.01, 1, 116, Infinity)).toBe(true)
  })
  it('docks when the ring the sector names push out no longer fits the stage', () => {
    expect(launchersDocked(1, 0.57, 196, fits(0.57, 196) - 1)).toBe(true)
    expect(launchersDocked(1.2, 0.57, 196, fits(0.57, 196) + 1)).toBe(true)
    // A crowded agent ring pushes the sector names out, and the launchers with them.
    expect(launchersDocked(1, 1, 455, 1090)).toBe(true)
    expect(launchersDocked(1, 1, 116, 1090)).toBe(false)
  })
})
