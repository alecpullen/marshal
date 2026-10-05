import { describe, expect, it } from 'vitest'
import { PRESETS, describe as describeCron, nextRun, nextRuns, parseCron, validCron } from './cron'

const utc = (s: string) => new Date(`${s.replace(' ', 'T')}:00Z`)
const fmt = (d: Date | null) => (d ? d.toISOString().slice(0, 16).replace('T', ' ') : null)

// Copied from web/bridge/cron_test.go (TestCronNext).
const NEXT: [string, string, string][] = [
  ['* * * * *', '2026-01-01 00:00', '2026-01-01 00:01'],
  ['*/15 * * * *', '2026-01-01 00:14', '2026-01-01 00:15'],
  ['*/15 * * * *', '2026-01-01 00:45', '2026-01-01 01:00'],
  ['5 4 * * *', '2026-01-01 04:05', '2026-01-02 04:05'],
  ['0 9-17/4 * * *', '2026-01-01 10:00', '2026-01-01 13:00'],
  ['0,30 8 * * *', '2026-01-01 08:00', '2026-01-01 08:30'],
  ['0 0 1 * *', '2026-01-15 12:00', '2026-02-01 00:00'],
  ['0 0 * 3 *', '2026-04-01 00:00', '2027-03-01 00:00'],
  ['30 23 31 12 *', '2026-12-31 23:30', '2027-12-31 23:30'],
  ['0 0 * * 0', '2026-01-01 00:00', '2026-01-04 00:00'],
  ['0 0 * * 7', '2026-01-01 00:00', '2026-01-04 00:00'],
  ['@hourly', '2026-01-01 00:10', '2026-01-01 01:00'],
  ['@daily', '2026-01-01 00:10', '2026-01-02 00:00'],
  ['@weekly', '2026-01-01 00:10', '2026-01-04 00:00'],
  ['0 0 15 * 1', '2026-01-01 00:00', '2026-01-05 00:00'],
  ['0 0 15 * 1', '2026-01-12 00:00', '2026-01-15 00:00'],
  ['0 0 29 2 *', '2026-01-01 00:00', '2028-02-29 00:00'],
]

describe('nextRun', () => {
  it.each(NEXT)('%s after %s is %s', (expr, after, want) => {
    expect(fmt(nextRun(expr, utc(after)))).toBe(want)
  })

  it('is strictly after the given minute', () => {
    expect(fmt(nextRun('0 * * * *', utc('2026-01-01 05:00')))).toBe('2026-01-01 06:00')
  })

  it('returns null for an impossible date', () => {
    expect(nextRun('0 0 30 2 *', utc('2026-01-01 00:00'))).toBeNull()
  })
})

describe('nextRuns', () => {
  it('lists successive runs', () => {
    expect(nextRuns('0 9 * * *', utc('2026-01-01 10:00'), 3).map(fmt)).toEqual(['2026-01-02 09:00', '2026-01-03 09:00', '2026-01-04 09:00'])
  })
})

describe('parseCron', () => {
  // Copied from TestParseCronInvalid.
  it.each(['', '* * * *', '* * * * * *', '60 * * * *', '* 24 * * *', '* * 0 * *', '* * 32 * *', '* * * 13 *', '* * * * 8', 'a * * * *', '*/0 * * * *', '5-1 * * * *', '1- * * * *', '*/x * * * *', '@yearly'])(
    'rejects %j',
    (expr) => {
      expect(() => parseCron(expr)).toThrow()
      expect(validCron(expr)).toBe(false)
    },
  )
})

describe('describe', () => {
  it.each([
    ['*/15 * * * *', 'Every 15 minutes'],
    ['0 * * * *', 'Hourly at :00'],
    ['30 * * * *', 'Hourly at :30'],
    ['0 9 * * *', 'Daily at 09:00'],
    ['0 9 * * 1-5', 'Weekdays at 09:00'],
    ['0 9 * * 1', 'Weekly on Monday at 09:00'],
    ['0 9 * * 7', 'Weekly on Sunday at 09:00'],
    ['@daily', 'Daily at 00:00'],
    ['0 0 15 * 1', '0 0 15 * 1'],
    ['nonsense', 'nonsense'],
  ])('%s → %s', (expr, text) => {
    expect(describeCron(expr)).toBe(text)
  })

  it('every preset is valid and described', () => {
    for (const p of PRESETS) {
      expect(validCron(p.cron)).toBe(true)
      expect(describeCron(p.cron)).not.toBe(p.cron)
    }
  })
})
