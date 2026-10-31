import { money, type Mandate } from './api'

/** money() without cents for whole amounts: $150, $39.99. */
export const moneyShort = (v: number, cur = 'USD') =>
  Number.isInteger(v)
    ? new Intl.NumberFormat('en-US', { style: 'currency', currency: cur || 'USD', maximumFractionDigits: 0 }).format(v)
    : money(v, cur)

/** 30 Nov 2026 */
export const shortDate = (iso?: string | null, now = new Date()) => {
  if (!iso) return '—'
  const d = new Date(iso)
  return d.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: d.getFullYear() === now.getFullYear() ? undefined : 'numeric' })
}

/** just now · 3 min ago · 2 h ago · yesterday · 4 days ago · 30 Nov 2026 */
export const relativeTime = (iso: string, now = Date.now()) => {
  const diff = now - new Date(iso).getTime()
  const s = Math.round(diff / 1000)
  if (!Number.isFinite(s) || s < 45) return 'just now'
  const m = Math.round(s / 60)
  if (m < 60) return `${m} min ago`
  const h = Math.round(m / 60)
  if (h < 24) return `${h} h ago`
  if (h < 48) return 'yesterday'
  const d = Math.round(h / 24)
  if (d < 7) return `${d} days ago`
  return shortDate(iso)
}

/** One line of rules: ≤ $150/item · computers, office · rating ≥ 4 · never temu.com */
export const describeRules = (m: Mandate) => {
  const parts: string[] = []
  if (m.max_per_item > 0) parts.push(`≤ ${moneyShort(m.max_per_item, m.currency)}/item`)
  if (m.allowed_categories && m.allowed_categories.length > 0) parts.push(m.allowed_categories.join(', '))
  if (m.min_rating > 0) parts.push(`rating ≥ ${m.min_rating}`)
  if (m.blocked_merchants && m.blocked_merchants.length > 0) parts.push(`never ${m.blocked_merchants.join(', ')}`)
  return parts.length ? parts.join(' · ') : 'No restrictions beyond the budget'
}

export const remainingOf = (m: Mandate) => Math.max(0, m.total_budget - m.spent - m.reserved)
