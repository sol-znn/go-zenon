// Small things several benches need.

import { QSR_ZTS, ZNN_ZTS } from './znn/primitives.ts'

export const short = (hex: string, head = 10, tail = 6) =>
  hex.length <= head + tail + 1 ? hex : `${hex.slice(0, head)}…${hex.slice(-tail)}`

/** A unix second as a UTC clock time, with the date when it is not today. */
export function clock(timestamp: number, now = Date.now()): string {
  const iso = new Date(timestamp * 1000).toISOString()
  const today = new Date(now).toISOString().slice(0, 10)
  return iso.slice(0, 10) === today ? `${iso.slice(11, 19)} UTC` : `${iso.slice(0, 10)} ${iso.slice(11, 19)} UTC`
}

/** A number of seconds as a person would say it. */
export function span(seconds: number): string {
  const s = Math.abs(Math.round(seconds))
  if (s < 90) return `${s} s`
  if (s < 5400) return `${Math.floor(s / 60)} min ${s % 60 ? `${s % 60} s` : ''}`.trim()
  const h = Math.floor(s / 3600)
  const m = Math.round((s % 3600) / 60)
  return m ? `${h} h ${m} min` : `${h} h`
}

/** How znn-cli names a token on its command line. */
export function cliToken(zts: string): string {
  if (zts === ZNN_ZTS) return 'ZNN'
  if (zts === QSR_ZTS) return 'QSR'
  return zts
}

export function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}
