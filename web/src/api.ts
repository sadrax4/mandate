export type Mandate = {
  id: string
  name: string
  currency: string
  total_budget: number
  max_per_item: number
  auto_approve_under: number
  allowed_categories: string[] | null
  blocked_merchants: string[] | null
  min_rating: number
  require_approval: boolean
  spent: number
  reserved: number
  created_at: string
  save_payment_method?: boolean
  vault_id?: string
  vault_payer?: string
  vaulted_at?: string
}

export type NegotiationRound = { round: number; party: string; move: string; price?: number; message: string; at: string }
export type Negotiation = { service: string; seller: string; list_price: number; limit: number; rounds: NegotiationRound[]; outcome?: string; agreed_price?: number }

export type Mission = {
  id: string
  mandate_id: string
  goal: string
  status: string
  summary?: string
  error?: string
  proposal_id?: string
  order_id?: string
  turns: number
  suggestion?: { field: string; value: any; reason: string; applied_mission_id?: string }
  retry_of?: string
  repeat_days?: number
  next_run_at?: string
  last_run_id?: string
  repeat_of?: string
  kind?: string
  negotiation?: Negotiation
  created_at: string
  updated_at: string
}

export type CartItem = {
  product_id: string
  title: string
  merchant: string
  category: string
  unit_price: number
  currency: string
  quantity: number
  rating?: number
  reason?: string
}

export type Violation = { code: string; message: string; product_id?: string }

export type Decision = {
  allowed: boolean
  requires_approval: boolean
  total: number
  currency: string
  remaining_budget: number
  remaining_after: number
  violations: Violation[]
  checks_passed: string[]
}

export type Proposal = {
  id: string
  mission_id: string
  mandate_id: string
  cart: { items: CartItem[]; currency: string }
  decision: Decision
  summary: string
  status: string
  feedback?: string
  order_id?: string
  created_at: string
  decided_at?: string
}

export type Payment = {
  id: string
  proposal_id: string
  mission_id: string
  mandate_id: string
  status: string
  amount: number
  currency: string
  approve_url?: string
  capture_id?: string
  payer_email?: string
  mode: string
  vaulted?: boolean
  refunded?: number
  accounted?: boolean
  created_at: string
  updated_at: string
}

export type LedgerRow = Payment & { goal: string; mandate_name: string; items: number; budget: number }

export type Product = {
  id: string
  title: string
  brand?: string
  merchant: string
  category: string
  price: number
  currency: string
  rating?: number
  reviews?: number
  url?: string
  image_url?: string
  description?: string
  attributes?: Record<string, string>
  source: string
}

export type Event = {
  id: number
  mission_id: string
  type: string
  message: string
  data?: any
  at: string
}

export type MissionDetail = {
  mission: Mission
  mandate: Mandate | null
  proposals: Proposal[] | null
  payments: Payment[] | null
  events: Event[] | null
}

export type Config = {
  paypal_mode: string
  llm_provider: string
  catalog: string
  currency: string
  public_url: string
  version: string
  demo_buyer_hint?: string
  paypal_mcp?: { enabled: boolean; tools: string[]; available?: number; error?: string; url?: string; checking?: boolean }
}

export type InsightItem = {
  order_id: string
  capture_id?: string
  mission_id: string
  mandate: string
  goal: string
  product: string
  product_id: string
  merchant: string
  category: string
  unit_price: number
  quantity: number
  line_total: number
  currency: string
  status: string
  paid_via: string
  payer?: string
  mode: string
  created_at: string
}
export type InsightBucket = { key: string; amount: number; count: number }
export type Insights = {
  currency: string
  totals: { captured: number; orders: number; items: number; mandates: number; average_order: number }
  mandates: { id: string; name: string; currency: string; budget: number; spent: number; reserved: number; remaining: number; orders: number; items: number; saved_paypal: boolean }[]
  by_category: InsightBucket[]
  by_merchant: InsightBucket[]
  by_day: InsightBucket[]
  items: InsightItem[]
}

export type AskEvent = { type: string; message: string; data?: any }
export type AskResult = { answer: string; tools_used: string[]; events: AskEvent[]; warning?: string }

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...init })
  const text = await res.text()
  let body: any = null
  try {
    body = text ? JSON.parse(text) : null
  } catch {
    body = { error: text }
  }
  if (!res.ok) throw new Error(body?.error || `${res.status} ${res.statusText}`)
  return body as T
}

export const api = {
  config: () => req<Config>('/api/config'),
  categories: () => req<string[]>('/api/categories'),
  mandates: () => req<Mandate[]>('/api/mandates'),
  mandate: (id: string) => req<{ mandate: Mandate; missions: Mission[] }>(`/api/mandates/${id}`),
  createMandate: (m: Partial<Mandate>) => req<Mandate>('/api/mandates', { method: 'POST', body: JSON.stringify(m) }),
  missions: () => req<Mission[]>('/api/missions'),
  mission: (id: string) => req<MissionDetail>(`/api/missions/${id}`),
  createMission: (mandateId: string, goal: string, repeatDays = 0, kind = 'buy') =>
    req<Mission>('/api/missions', { method: 'POST', body: JSON.stringify({ mandate_id: mandateId, goal, repeat_days: repeatDays, kind }) }),
  forgetPayment: (mandateId: string) => req<Mandate>(`/api/mandates/${mandateId}/forget-payment`, { method: 'POST' }),
  runNow: (missionId: string) => req<Mission>(`/api/missions/${missionId}/run-now`, { method: 'POST' }),
  stopRepeat: (missionId: string) => req<Mission>(`/api/missions/${missionId}/stop-repeat`, { method: 'POST' }),
  insights: () => req<Insights>('/api/insights'),
  applySuggestion: (missionId: string) =>
    req<{ mission: Mission; change: string }>(`/api/missions/${missionId}/apply-suggestion`, { method: 'POST' }),
  ask: (question: string, mandateId?: string) =>
    req<AskResult>('/api/ask', { method: 'POST', body: JSON.stringify({ question, mandate_id: mandateId || '' }) }),
  approve: (proposalId: string) =>
    req<{ payment: Payment; approve_url: string }>(`/api/proposals/${proposalId}/approve`, { method: 'POST' }),
  reject: (proposalId: string, reason: string) =>
    req<{ ok: boolean }>(`/api/proposals/${proposalId}/reject`, { method: 'POST', body: JSON.stringify({ reason }) }),
  ledger: () => req<LedgerRow[]>('/api/ledger'),
  syncPayment: (orderId: string) => req<Payment>(`/api/payments/${orderId}/sync`, { method: 'POST' }),
}

export const money = (v: number | undefined | null, cur = 'USD') =>
  v === undefined || v === null || Number.isNaN(v)
    ? ''
    : new Intl.NumberFormat('en-US', { style: 'currency', currency: cur || 'USD', maximumFractionDigits: 2 }).format(v)

export const STATUS_LABEL: Record<string, string> = {
  running: 'Agent working',
  awaiting_approval: 'Needs your approval',
  awaiting_payment: 'Pay with PayPal',
  paid: 'Paid',
  cancelled: 'Cancelled',
  no_proposal: 'No proposal',
  failed: 'Failed',
  pending_approval: 'Pending approval',
  auto_approved: 'Auto-approved',
  approved: 'Approved',
  rejected: 'Rejected',
  rejected_by_policy: 'Blocked by mandate',
  COMPLETED: 'Completed',
  PAYER_ACTION_REQUIRED: 'Awaiting buyer',
  APPROVED: 'Approved by buyer',
  CREATED: 'Created',
  CANCELLED: 'Cancelled',
  CAPTURE_FAILED: 'Capture failed',
  PENDING: 'Capture pending',
  VOIDED: 'Voided',
  EXPIRED: 'Expired',
  REFUNDED: 'Refunded',
  PARTIALLY_REFUNDED: 'Partially refunded',
  REVERSED: 'Reversed',
  DENIED: 'Denied',
  refunded: 'Refunded',
}

/** Payment statuses that can never become payable again (mirrors store.IsFinalPayment). */
export const FINAL_PAYMENT_STATUSES = ['COMPLETED', 'PARTIALLY_REFUNDED', 'CANCELLED', 'VOIDED', 'EXPIRED', 'REFUNDED', 'REVERSED', 'DENIED']
export const isFinalPayment = (s: string) => FINAL_PAYMENT_STATUSES.includes(s)
/** Payment statuses where PayPal has taken, or is taking, the money: no checkout, no withdrawal (mirrors store.IsCaptured). */
export const CAPTURED_PAYMENT_STATUSES = ['COMPLETED', 'PARTIALLY_REFUNDED', 'PENDING']
export const isCaptured = (s: string) => CAPTURED_PAYMENT_STATUSES.includes(s)

export const label = (s: string) => STATUS_LABEL[s] ?? s.replace(/_/g, ' ')

export const statusTone = (s: string) => {
  switch (s) {
    case 'paid':
    case 'COMPLETED':
    case 'approved':
    case 'auto_approved':
    case 'APPROVED':
      return 'good'
    case 'awaiting_approval':
    case 'pending_approval':
    case 'awaiting_payment':
    case 'PAYER_ACTION_REQUIRED':
    case 'CREATED':
    case 'PENDING':
    case 'PARTIALLY_REFUNDED':
      return 'warn'
    case 'failed':
    case 'rejected_by_policy':
    case 'CAPTURE_FAILED':
    case 'cancelled':
    case 'CANCELLED':
    case 'VOIDED':
    case 'EXPIRED':
    case 'REFUNDED':
    case 'REVERSED':
    case 'DENIED':
    case 'refunded':
    case 'rejected':
      return 'bad'
    case 'running':
      return 'live'
    default:
      return 'muted'
  }
}
