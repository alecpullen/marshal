/*
  Five-field cron in UTC, mirroring the bridge's parser (web/bridge/cron.go):
  `*`, lists, ranges and `/step`; @hourly, @daily and @weekly; Sunday is 0 or
  7; and when both day fields are restricted a day matches either (POSIX).
  The bridge is the authority; this only previews.
*/

export interface Cron {
  min: Set<number>
  hour: Set<number>
  dom: Set<number>
  month: Set<number>
  dow: Set<number>
  domAny: boolean
  dowAny: boolean
}

const ALIASES: Record<string, string> = { '@hourly': '0 * * * *', '@daily': '0 0 * * *', '@weekly': '0 0 * * 0' }

export const PRESETS: { label: string; cron: string }[] = [
  { label: 'Every 15 minutes', cron: '*/15 * * * *' },
  { label: 'Hourly', cron: '0 * * * *' },
  { label: 'Daily at 09:00', cron: '0 9 * * *' },
  { label: 'Weekdays at 09:00', cron: '0 9 * * 1-5' },
  { label: 'Weekly on Monday at 09:00', cron: '0 9 * * 1' },
]

function parseField(s: string, lo: number, hi: number, name: string): { set: Set<number>; star: boolean } {
  const set = new Set<number>()
  let star = false
  for (const part of s.split(',')) {
    const slash = part.indexOf('/')
    const rng = slash < 0 ? part : part.slice(0, slash)
    const hasStep = slash >= 0
    let step = 1
    if (hasStep) {
      const t = part.slice(slash + 1)
      step = /^\d+$/.test(t) ? Number(t) : NaN
      if (!(step >= 1)) throw new Error(`cron: bad step "${t}" in ${name}`)
    }
    let a = lo
    let b = hi
    if (rng === '*') {
      if (!hasStep) star = true
    } else if (rng.includes('-')) {
      const [from, to] = rng.split('-', 2)
      a = /^\d+$/.test(from) ? Number(from) : NaN
      b = /^\d+$/.test(to) ? Number(to) : NaN
      if (Number.isNaN(a) || Number.isNaN(b)) throw new Error(`cron: bad range "${rng}" in ${name}`)
    } else {
      if (!/^\d+$/.test(rng)) throw new Error(`cron: bad value "${rng}" in ${name}`)
      a = b = Number(rng)
      if (hasStep) b = hi
    }
    if (a < lo || b > hi || a > b) throw new Error(`cron: ${name} must be within ${lo}-${hi}`)
    for (let v = a; v <= b; v += step) set.add(v)
  }
  return { set, star }
}

export function parseCron(expr: string): Cron {
  let e = expr.trim()
  if (ALIASES[e]) e = ALIASES[e]
  const f = e.split(/\s+/).filter(Boolean)
  if (f.length !== 5) throw new Error(`cron: want 5 fields, got ${f.length}`)
  const min = parseField(f[0], 0, 59, 'minute')
  const hour = parseField(f[1], 0, 23, 'hour')
  const dom = parseField(f[2], 1, 31, 'day of month')
  const month = parseField(f[3], 1, 12, 'month')
  const dow = parseField(f[4], 0, 7, 'day of week')
  if (dow.set.delete(7)) dow.set.add(0)
  return { min: min.set, hour: hour.set, dom: dom.set, month: month.set, dow: dow.set, domAny: dom.star, dowAny: dow.star }
}

export function validCron(expr: string): boolean {
  try {
    parseCron(expr)
    return true
  } catch {
    return false
  }
}

function dayMatches(c: Cron, t: Date): boolean {
  const domOK = c.dom.has(t.getUTCDate())
  const dowOK = c.dow.has(t.getUTCDay())
  if (c.domAny && c.dowAny) return true
  if (c.domAny) return dowOK
  if (c.dowAny) return domOK
  return domOK || dowOK
}

/** The first matching minute strictly after `after`, or null when nothing matches within five years. */
export function nextRun(expr: string | Cron, after: Date): Date | null {
  const c = typeof expr === 'string' ? parseCron(expr) : expr
  const t = new Date(Math.floor(after.getTime() / 60000) * 60000 + 60000)
  const limit = new Date(t)
  limit.setUTCFullYear(limit.getUTCFullYear() + 5)
  while (t < limit) {
    if (!c.month.has(t.getUTCMonth() + 1)) {
      t.setTime(Date.UTC(t.getUTCFullYear(), t.getUTCMonth() + 1, 1))
    } else if (!dayMatches(c, t)) {
      t.setTime(Date.UTC(t.getUTCFullYear(), t.getUTCMonth(), t.getUTCDate() + 1))
    } else if (!c.hour.has(t.getUTCHours())) {
      t.setTime(Date.UTC(t.getUTCFullYear(), t.getUTCMonth(), t.getUTCDate(), t.getUTCHours() + 1))
    } else if (!c.min.has(t.getUTCMinutes())) {
      t.setTime(t.getTime() + 60000)
    } else {
      return new Date(t)
    }
  }
  return null
}

export function nextRuns(expr: string, from: Date, n: number): Date[] {
  const c = parseCron(expr)
  const out: Date[] = []
  let at = from
  while (out.length < n) {
    const t = nextRun(c, at)
    if (!t) break
    out.push(t)
    at = t
  }
  return out
}

const DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
const pad = (n: number) => String(n).padStart(2, '0')

/** A human description of the common shapes (UTC); anything else comes back as written. */
export function describe(expr: string): string {
  const e = expr.trim()
  const f = (ALIASES[e] ?? e).split(/\s+/)
  if (f.length !== 5 || !validCron(e)) return expr
  const [m, h, dom, mon, dow] = f
  const num = (s: string) => /^\d+$/.test(s)
  if (dom !== '*' || mon !== '*') return expr
  if (m === '*' && h === '*' && dow === '*') return 'Every minute'
  const step = /^\*\/(\d+)$/.exec(m)
  if (step && h === '*' && dow === '*') return `Every ${step[1]} minutes`
  if (num(m) && h === '*' && dow === '*') return `Hourly at :${pad(Number(m))}`
  if (num(m) && num(h)) {
    const at = `${pad(Number(h))}:${pad(Number(m))}`
    if (dow === '*') return `Daily at ${at}`
    if (dow === '1-5') return `Weekdays at ${at}`
    if (num(dow) && Number(dow) <= 7) return `Weekly on ${DAYS[Number(dow) % 7]} at ${at}`
  }
  return expr
}
