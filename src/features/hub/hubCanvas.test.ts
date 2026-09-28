import type { HubNote } from './composables/useObsidianGraph'
import type { LabelCandidate } from './hubCanvas'
import { describe, expect, it } from 'vitest'
import { labelSize } from './__tests__/labelMeasurement'
import { agentDotBox, agentLabelBox, agentLabelDirection, agentLabelKey, agentLabelOffset, agentPriority, boxesOverlap, cullLabels, hitNote, hubNoteSet, inwardUnit, isToday, NOTE_LABEL_OFFSET_PX, noteLabelBox, notePriority, sectorLabelBox, sectorLabelKey } from './hubCanvas'
import { polar } from './hubGeometry'

const measured = (c: LabelCandidate) => agentLabelBox(c, labelSize(c.text))
const measuredNote = (c: LabelCandidate) => noteLabelBox(c, labelSize(c.text).w)

describe('notePriority', () => {
  it('orders hub > touched > fresh > links', () => {
    const hub = notePriority({ hub: true, touched: false, fresh: false, linkCount: 0 })
    const touched = notePriority({ hub: false, touched: true, fresh: false, linkCount: 0 })
    const fresh = notePriority({ hub: false, touched: false, fresh: true, linkCount: 0 })
    const links = notePriority({ hub: false, touched: false, fresh: false, linkCount: 5 })
    expect(hub).toBeGreaterThan(touched)
    expect(touched).toBeGreaterThan(fresh)
    expect(fresh).toBeGreaterThan(links)
  })
})

describe('cullLabels', () => {
  it('keeps the higher-priority label of two overlapping ones', () => {
    const kept = cullLabels([
      { index: 0, sx: 100, sy: 100, text: 'a', priority: 10 },
      { index: 1, sx: 102, sy: 100, text: 'b', priority: 20 },
    ], measuredNote)
    expect(kept).toEqual(new Set([1]))
  })

  it('keeps both of two labels that are far apart', () => {
    const kept = cullLabels([
      { index: 0, sx: 0, sy: 0, text: 'a', priority: 10 },
      { index: 1, sx: 500, sy: 500, text: 'b', priority: 20 },
    ], measuredNote)
    expect(kept).toEqual(new Set([0, 1]))
  })

  it('places boxes with a caller-supplied shape instead of the note default', () => {
    const kept = cullLabels([
      { index: 0, sx: 100, sy: 100, text: 'a', priority: 10 },
      { index: 1, sx: 100, sy: 500, text: 'b', priority: 20 },
    ], measured)
    expect(kept).toEqual(new Set([0, 1]))
  })

  it('culls from the measured width where a character-count estimate would have kept both', () => {
    const text = 'Kontor Hub Working'
    const estimated = text.length * 6.3 + 24 // the estimate this unit replaced
    const apart = estimated + 10 // clear under the estimate, overlapping once really measured
    const kept = cullLabels([
      { index: 1, sx: 0, sy: 0, text, priority: 1 },
      { index: 2, sx: apart, sy: 0, text, priority: 0 },
    ], c => agentLabelBox(c, { w: estimated + 40, h: 16 }))
    expect(kept).toEqual(new Set([1]))
  })

  it('draws a label whose size is not measured yet and lets it block nothing', () => {
    const unmeasured = { index: 1, sx: 100, sy: 100, text: 'Kontor Hub Working', priority: 10 }
    const neighbour = { index: 2, sx: 104, sy: 100, text: 'Web App Idle', priority: 0 }
    const boxOf = (c: LabelCandidate) => c.index === 1 ? agentLabelBox(c) : measured(c)
    expect(cullLabels([unmeasured, neighbour], boxOf)).toEqual(new Set([1, 2]))
    expect(cullLabels([unmeasured], boxOf, [{ box: agentDotBox(100, 112) }])).toEqual(new Set([1]))
  })
})

describe('noteLabelBox', () => {
  it('takes the measured width and centres on the point the canvas draws from', () => {
    const box = noteLabelBox({ index: 0, sx: 100, sy: 100, text: 'Alpha', priority: 0 }, 42)
    expect(box.x).toBe(100 + NOTE_LABEL_OFFSET_PX)
    expect(box.y + box.h / 2).toBe(100)
    expect(box.w).toBe(42)
  })
})

describe('agentPriority', () => {
  it('ranks needs-operator above working above the rest', () => {
    const needsOperator = agentPriority(true, false)
    const working = agentPriority(false, true)
    const rest = agentPriority(false, false)
    expect(needsOperator).toBeGreaterThan(working)
    expect(working).toBeGreaterThan(rest)
  })
})

describe('agentLabelBox', () => {
  it('centres the measured box under the dot', () => {
    const box = agentLabelBox({ index: 0, sx: 100, sy: 100, text: 'Kontor Hub', priority: 0 }, { w: 80, h: 16 })
    expect(box.x + box.w / 2).toBeCloseTo(100)
    expect(box.y).toBeGreaterThan(100)
    expect(box.w).toBe(80)
    expect(box.h).toBe(16)
  })

  it('has no box at all while its size is unmeasured', () => {
    const box = agentLabelBox({ index: 0, sx: 100, sy: 100, text: 'Kontor Hub', priority: 0 })
    expect(boxesOverlap(box, agentDotBox(100, 112))).toBe(false)
  })
})

describe('agentLabelBox placed inward', () => {
  const candidate = { index: 0, sx: 300, sy: 300, text: 'Kontor Hub Working', priority: 0 }
  const size = labelSize(candidate.text)

  it('hangs a southern agent\'s label toward the core and leaves a northern one below its dot', () => {
    const south = agentLabelBox(candidate, size, inwardUnit(...polar(240, 90)))
    const north = agentLabelBox(candidate, size, inwardUnit(...polar(240, -90)))
    expect(south.y + south.h).toBeLessThanOrEqual(candidate.sy)
    expect(north.y).toBeGreaterThanOrEqual(candidate.sy)
    // Straight down is the default, so every caller that has no direction keeps today's placement.
    expect(north).toEqual(agentLabelBox(candidate, size))
  })

  it('centres the label on the radial line and never covers its own dot, at any angle', () => {
    for (let deg = 0; deg < 360; deg += 5) {
      const [x, y] = polar(240, deg)
      const [ux, uy] = inwardUnit(x, y)
      const box = agentLabelBox(candidate, size, [ux, uy])
      const offCentre = (box.x + box.w / 2 - candidate.sx) * uy - (box.y + box.h / 2 - candidate.sy) * ux
      expect(offCentre, `${deg}° off the radial line`).toBeCloseTo(0)
      expect(boxesOverlap(box, agentDotBox(candidate.sx, candidate.sy)), `${deg}° covers its own dot`).toBe(false)
    }
  })

  it('frees a southern agent from the sector name drawn outside its dot', () => {
    // The sector name sits one agent-ring clearance beyond the dot: the collision that culls every
    // southern label while labels always hang below.
    const sector = sectorLabelBox(candidate.sx, candidate.sy + 24, labelSize(sectorLabelKey('Work', 120)))
    const inward = inwardUnit(...polar(240, 90))
    expect(cullLabels([candidate], c => agentLabelBox(c, size), [{ box: sector }])).toEqual(new Set())
    expect(cullLabels([candidate], c => agentLabelBox(c, size, inward), [{ box: sector }])).toEqual(new Set([0]))
  })
})

describe('agentLabelOffset', () => {
  it('is the offset agentLabelBox places the label centre at', () => {
    const c = { index: 0, sx: 300, sy: 300, text: 'Kontor Hub Working', priority: 0 }
    const size = labelSize(c.text)
    for (let deg = 0; deg < 360; deg += 45) {
      const inward = inwardUnit(...polar(240, deg))
      const box = agentLabelBox(c, size, inward)
      const [dx, dy] = agentLabelOffset(inward, size)
      expect(box.x + box.w / 2 - c.sx).toBeCloseTo(dx)
      expect(box.y + box.h / 2 - c.sy).toBeCloseTo(dy)
    }
  })
})

describe('agentLabelDirection', () => {
  const candidate = { index: 1, sx: 300, sy: 300, text: 'Kontor Hub Working', priority: 0 }
  const size = labelSize(candidate.text)
  const inward = inwardUnit(...polar(240, 90)) // straight up: the agent sits due south

  it('keeps the inward place while nothing stands there', () => {
    expect(agentLabelDirection(candidate, size, inward, [])).toEqual(inward)
  })

  it('turns outward when a tier-mate\'s dot sits inward of the agent', () => {
    const tierMate = { box: agentDotBox(candidate.sx, candidate.sy - 26), ownerIndex: 2 }
    expect(agentLabelDirection(candidate, size, inward, [tierMate])).toEqual([-inward[0], -inward[1]])
  })

  it('stays inward when both sides are taken, and lets the culler decide', () => {
    const boxed = [-26, 26].map(dy => ({ box: agentDotBox(candidate.sx, candidate.sy + dy), ownerIndex: 2 }))
    expect(agentLabelDirection(candidate, size, inward, boxed)).toEqual(inward)
  })

  it('never turns away from its own dot', () => {
    const own = { box: agentDotBox(candidate.sx, candidate.sy - 26), ownerIndex: candidate.index }
    expect(agentLabelDirection(candidate, size, inward, [own])).toEqual(inward)
  })
})

describe('inwardUnit', () => {
  it('points from the agent back to the core and is a unit vector', () => {
    const [ux, uy] = inwardUnit(...polar(240, 30))
    expect(Math.hypot(ux, uy)).toBeCloseTo(1)
    expect([ux, uy]).toEqual([-Math.cos(Math.PI / 6), -Math.sin(Math.PI / 6)].map(v => expect.closeTo(v)))
  })

  it('falls back to straight down for an agent sitting on the core', () => {
    expect(inwardUnit(0, 0)).toEqual([0, 1])
  })
})

describe('cullLabels with agent boxes', () => {
  it('keeps only the highest-priority label of three agents whose boxes would collide', () => {
    const kept = cullLabels([
      { index: 1, sx: 100, sy: 100, text: 'Kontor Hub', priority: 0 },
      { index: 2, sx: 106, sy: 100, text: 'Web App', priority: 2 },
      { index: 3, sx: 112, sy: 100, text: 'Api Server', priority: 1 },
    ], measured)
    expect(kept).toEqual(new Set([2]))
  })

  it('always keeps a needs-operator agent even when it collides with others', () => {
    const kept = cullLabels([
      { index: 1, sx: 100, sy: 100, text: 'A', priority: agentPriority(true, false) },
      { index: 2, sx: 101, sy: 100, text: 'B', priority: agentPriority(false, true) },
      { index: 3, sx: 102, sy: 100, text: 'C', priority: agentPriority(false, false) },
    ], measured)
    expect(kept).toEqual(new Set([1]))
  })

  it('culls a label that would land on another agent\'s dot (defect 2)', () => {
    // Agent 2's dot sits right where agent 1's label would be drawn.
    const candidate = { index: 1, sx: 100, sy: 100, text: 'Kontor Hub', priority: 0 }
    const dotBox = agentDotBox(100, measured(candidate).y + 8)
    const kept = cullLabels([candidate], measured, [{ box: dotBox, ownerIndex: 2 }])
    expect(kept).toEqual(new Set())
  })

  it('never blocks a label with its own dot', () => {
    const candidate = { index: 1, sx: 100, sy: 100, text: 'Kontor Hub', priority: 0 }
    const ownDot = agentDotBox(candidate.sx, candidate.sy + 15) // close enough to the label to collide if not excluded
    const kept = cullLabels([candidate], measured, [{ box: ownDot, ownerIndex: 1 }])
    expect(kept).toEqual(new Set([1]))
  })

  it('culls a label that would land on a sector name (defect 3)', () => {
    const sector = sectorLabelBox(100, 116, labelSize(sectorLabelKey('Other', 4)))
    const kept = cullLabels([{ index: 1, sx: 100, sy: 100, text: 'Kontor Hub', priority: 0 }], measured, [{ box: sector }])
    expect(kept).toEqual(new Set())
  })
})

describe('agentDotBox', () => {
  it('centres an 18px box on the screen point', () => {
    const box = agentDotBox(100, 100)
    expect(box).toEqual({ x: 91, y: 91, w: 18, h: 18 })
  })
})

describe('sectorLabelBox', () => {
  it('centres the measured box on the screen point', () => {
    const box = sectorLabelBox(100, 100, { w: 84, h: 18 })
    expect(box.x + box.w / 2).toBeCloseTo(100)
    expect(box.y + box.h / 2).toBeCloseTo(100)
    expect(box).toEqual({ x: 58, y: 91, w: 84, h: 18 })
  })
})

describe('label keys', () => {
  it('carries the status word an agent label renders next to its name', () => {
    expect(agentLabelKey('kontor-hub', 'working')).toBe('Kontor Hub Working')
  })

  it('carries the weight badge a sector name renders', () => {
    expect(sectorLabelKey('Other', 4)).toBe('Other 4')
  })
})

describe('hitNote', () => {
  const cam = { k: 1, tx: 0, ty: 0 }
  const points: Array<[number, number]> = [[0, 0], [100, 100]]

  it('returns the nearest note within 8px', () => {
    expect(hitNote(points, cam, 3, 4)).toBe(0)
  })

  it('returns -1 beyond 8px', () => {
    expect(hitNote(points, cam, 20, 20)).toBe(-1)
  })
})

describe('isToday', () => {
  it('is false for yesterday 23:59', () => {
    const now = new Date(2026, 8, 22, 0, 1).getTime()
    const yesterday = new Date(2026, 8, 21, 23, 59).getTime()
    expect(isToday(yesterday, now)).toBe(false)
  })

  it('is true for today 00:01', () => {
    const now = new Date(2026, 8, 22, 12, 0).getTime()
    const today = new Date(2026, 8, 22, 0, 1).getTime()
    expect(isToday(today, now)).toBe(true)
  })
})

describe('hubNoteSet', () => {
  function note(index: number, backlinks: number[]): HubNote {
    return { index, path: `n${index}.md`, title: `n${index}`, mtimeMs: 0, links: [], backlinks }
  }

  it('caps at 3 per sector and ignores notes with fewer than 2 backlinks', () => {
    const notes = [
      note(0, [1, 2, 3, 4]),
      note(1, [1, 2, 3]),
      note(2, [1, 2]),
      note(3, [1]),
      note(4, [1, 2, 3, 4, 5]),
    ]
    expect(hubNoteSet(notes, () => 'a')).toEqual(new Set([4, 0, 1]))
  })

  it('caps each sector independently', () => {
    const notes = [
      note(0, [1, 2]),
      note(1, [1, 2]),
      note(2, [1, 2]),
      note(3, [1, 2]),
    ]
    const sectorOf = (n: HubNote) => (n.index < 2 ? 'a' : 'b')
    expect(hubNoteSet(notes, sectorOf)).toEqual(new Set([0, 1, 2, 3]))
  })
})
