import { describe, expect, it } from 'vitest'
import { DAY_MS, sparkGeometry, sparkPath, thresholdOf } from './spark'

const NOW = 1_800_000_000_000
const at = (hoursAgo: number, value: number, tripped = false) => ({ at: NOW - hoursAgo * 3_600_000, value, tripped })

describe('sparkPath', () => {
  it('is empty for no samples', () => {
    expect(sparkPath([], 100, 20, NOW)).toBe('')
  })

  it('draws a constant series flat at mid height', () => {
    const g = sparkGeometry([at(3, 5), at(2, 5), at(1, 5)], 100, 20, { now: NOW })
    expect(g.points.map((p) => p.y)).toEqual([10, 10, 10])
    expect(g.path.startsWith('M')).toBe(true)
  })

  it('puts the maximum at the top and the minimum at the bottom', () => {
    const g = sparkGeometry([at(2, 0), at(1, 10)], 100, 20, { now: NOW, pad: 0 })
    expect(g.points[0].y).toBe(20)
    expect(g.points[1].y).toBe(0)
  })

  it('maps time onto x with now at the right edge and drops older samples', () => {
    const g = sparkGeometry([at(30, 1), at(12, 2), at(0, 3)], 240, 20, { now: NOW })
    expect(g.points).toHaveLength(2)
    expect(g.points[0].x).toBeCloseTo(120)
    expect(g.points[1].x).toBeCloseTo(240)
    expect(DAY_MS).toBe(86_400_000)
  })

  it('keeps the threshold inside the vertical scale', () => {
    const g = sparkGeometry([at(2, 1), at(1, 2)], 100, 20, { now: NOW, threshold: 10, pad: 0 })
    expect(g.thresholdY).toBe(0)
    // The samples sit below the threshold, so both are drawn under the line.
    expect(g.points.every((p) => p.y > g.thresholdY!)).toBe(true)
  })

  it('marks tripped points', () => {
    const g = sparkGeometry([at(2, 0), at(1, 1, true)], 100, 20, { now: NOW })
    expect(g.points.map((p) => p.tripped)).toEqual([false, true])
  })

  it('renders a lone sample as a dot-length segment', () => {
    expect(sparkPath([at(1, 3)], 100, 20, NOW)).toMatch(/^M[\d.]+ [\d.]+L[\d.]+ [\d.]+$/)
  })
})

describe('thresholdOf', () => {
  it('reads the operand of exit_code and numeric json conditions', () => {
    expect(thresholdOf('exit_code 0')).toBe(0)
    expect(thresholdOf('json a.b[0].c > 12.5')).toBe(12.5)
    expect(thresholdOf('json x < -3')).toBe(-3)
  })

  it('is null for everything else', () => {
    expect(thresholdOf('json name = alice')).toBeNull()
    expect(thresholdOf('regex foo.*')).toBeNull()
    expect(thresholdOf('change')).toBeNull()
    expect(thresholdOf('')).toBeNull()
    expect(thresholdOf(undefined)).toBeNull()
    expect(thresholdOf('exit_code abc')).toBeNull()
  })
})
