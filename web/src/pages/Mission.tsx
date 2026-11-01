import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { ColDef, RowClassRules } from 'ag-grid-community'
import { api, isCaptured, isFinalPayment, label, money, type CartItem, type Decision, type Event, type Mandate, type Mission, type MissionDetail, type Payment, type Product, type Proposal, type Violation } from '../api'
import { Grid, MONEY, NUM, useMediaQuery } from '../components/Grid'
import { Card, Empty, Glyph, Pill, Tracker, navigate, type TrackerStep } from '../components/ui'
import { Markdown } from '../components/Markdown'
import { StandingOrderButtons } from './Dashboard'
import { useConfig } from '../config'
import { NegotiationBlock, negotiationChip } from '../components/Negotiation'
import { remainingOf, shortDate } from '../format'

// Every event type the server emits (named SSE events need a listener each).
const EVENT_TYPES = ['status', 'assistant', 'tool_call', 'tool_result', 'mcp_tool_call', 'mcp_tool_result', 'candidates', 'check', 'proposal', 'policy_block', 'error', 'done', 'approved', 'payment', 'paid', 'rejected', 'cancelled', 'webhook', 'refunded', 'vault', 'suggestion', 'negotiation']
const FIELD_LABEL: Record<string, string> = {
  total_budget: 'total budget',
  max_per_item: 'per-item cap',
  min_rating: 'minimum rating',
  auto_approve_under: 'auto-approve threshold',
  allowed_categories: 'allowed categories',
  blocked_merchants: 'blocked merchants',
}
function describeSuggestion(s: { field: string; value: any }, currency: string) {
  const v = s.value
  switch (s.field) {
    case 'allowed_categories':
      return `allow ${Array.isArray(v) ? v.join(', ') : v}`
    case 'blocked_merchants':
      return `unblock ${Array.isArray(v) ? v.join(', ') : v}`
    case 'min_rating':
      return `lower the minimum rating to ${v}`
    default:
      return `set the ${FIELD_LABEL[s.field] ?? s.field} to ${typeof v === 'number' ? money(v, currency) : v}`
  }
}
const capitalize = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s)
/** Only web links are followed: a retailer feed must never hand the page a javascript: URL. */
const safeHref = (u?: string) => (u && /^https?:\/\//i.test(u) ? u : undefined)

/** Product name, linking to the merchant's page when the catalog provided one (Channel3 offers do). On a phone, merchant and price ride on a second line. */
function ProductCell(p: { value?: string; data?: Candidate; compactLine?: boolean; inCart?: (id: string) => boolean }) {
  if (!p.value) return null
  const href = safeHref(p.data?.url)
  const name = href ? (
    <a href={href} target="_blank" rel="noreferrer noopener" className="product-link" title="Open the merchant's product page" onClick={(e) => e.stopPropagation()}>
      {p.value} <span className="ext" aria-hidden="true">↗</span>
    </a>
  ) : (
    <span>{p.value}</span>
  )
  if (!p.compactLine || !p.data) return <div className="clamp2 cell-wrap">{name}</div>
  return (
    <div className="cart-item">
      <div className="cart-title">{name}</div>
      <div className="cart-reason meta num">
        <span>{p.data.merchant}</span>
        <span> · {money(p.data.price, p.data.currency)}</span>
        {p.data.rating ? <span> · {Number(p.data.rating).toFixed(1)}★</span> : null}
        {p.inCart?.(p.data.id) && <span className="in-cart"> · in cart</span>}
      </div>
    </div>
  )
}
/** Cart line: title (with the quantity on a phone) and the agent's reason underneath. */
function CartItemCell(p: { data?: CartItem; showQty?: boolean }) {
  if (!p.data) return null
  return (
    <div className="cart-item">
      <div className="cart-title">
        {p.data.title}
        {p.showQty && p.data.quantity > 1 && <span className="muted num"> × {p.data.quantity}</span>}
      </div>
      {p.data.reason && <div className="cart-reason">{p.data.reason}</div>}
    </div>
  )
}

const CHECK_LABEL: Record<string, string> = {
  within_budget: 'within budget',
  under_per_item_cap: 'under per-item cap',
  allowed_categories: 'allowed categories only',
  no_blocked_merchants: 'no blocked merchants',
  min_rating: 'meets minimum rating',
}
const ICON: Record<string, string> = {
  status: '•', assistant: '✎', tool_call: '⚙', tool_result: '↩', mcp_tool_call: '⇄', mcp_tool_result: '↩', candidates: '≡', check: '✓', proposal: '★', policy_block: '⊘',
  error: '!', done: '■', approved: '✓', payment: '$', paid: '✓', rejected: '✕', cancelled: '✕', webhook: '⇄', refunded: '↺', vault: '◉', suggestion: '✦', negotiation: '⇌',
}
const STORY_TYPES = new Set(['status', 'assistant', 'candidates', 'check', 'proposal', 'policy_block', 'error', 'done', 'approved', 'payment', 'paid', 'rejected', 'cancelled', 'webhook', 'refunded', 'vault', 'suggestion', 'negotiation'])
/** Hyphens inside words (USB-C, 27-inch) must not break a headline across lines. */
const keepHyphens = (s: string) => s.replace(/(\w)-(\w)/g, '$1\u2011$2')
const PAYABLE = ['pending_approval', 'auto_approved']

type Candidate = Product & { query: string }

const readFlag = (key: string) => {
  try {
    return localStorage.getItem(key) === '1'
  } catch {
    return false
  }
}

/** The violation sentences of a check, deduplicated, with the repeated "(allowed: ...)" list said once. */
function violationLines(vs: Violation[]) {
  let allowed = ''
  const lines: string[] = []
  for (const v of vs) {
    const m = /\s*\(allowed: ([^)]*)\)\s*$/.exec(v.message)
    if (m) allowed = m[1]
    const line = m ? v.message.slice(0, m.index) : v.message
    if (!lines.includes(line)) lines.push(line)
  }
  return { lines, allowed }
}

function Violations({ vs }: { vs: Violation[] }) {
  const { lines, allowed } = violationLines(vs)
  return (
    <ul className="violations">
      {lines.map((l) => <li key={l}><Glyph>⊘</Glyph> {l}</li>)}
      {allowed && <li className="allowed">Allowed categories: {allowed}</li>}
    </ul>
  )
}

export default function MissionPage({ id, params }: { id: string; params: URLSearchParams }) {
  const [detail, setDetail] = useState<MissionDetail | null>(null)
  const [events, setEvents] = useState<Event[]>([])
  // loadErr comes and goes with refreshes; actionErr (approve, reject, ...) stays until the next action.
  const [loadErr, setLoadErr] = useState('')
  const [actionErr, setActionErr] = useState('')
  const [busy, setBusy] = useState('')
  const [rejecting, setRejecting] = useState(false)
  const [reason, setReason] = useState('')
  const [verbose, setVerbose] = useState(() => readFlag('mandate.log.verbose'))
  const [copied, setCopied] = useState(false)
  const [whyOpen, setWhyOpen] = useState(false)
  const config = useConfig()
  const statusRef = useRef('')
  const narrow = useMediaQuery('(max-width: 899px)')
  const phone = useMediaQuery('(max-width: 720px)')

  const refresh = () =>
    api
      .mission(id)
      .then((d) => {
        setDetail(d)
        statusRef.current = d.mission.status
        setEvents((prev) => mergeEvents(prev, d.events ?? []))
        setLoadErr('')
      })
      .catch((e) => setLoadErr(e.message))

  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined
    refresh()
    const es = new EventSource(`/api/missions/${id}/events`)
    const onEv = (e: MessageEvent) => {
      try {
        const ev: Event = JSON.parse(e.data)
        setEvents((prev) => mergeEvents(prev, [ev]))
      } catch {
        /* ignore malformed frames */
      }
      clearTimeout(timer)
      timer = setTimeout(refresh, 250)
    }
    for (const t of EVENT_TYPES) es.addEventListener(t, onEv)
    const poll = setInterval(() => {
      const s = statusRef.current
      if (s === 'running' || s === 'awaiting_payment') refresh()
    }, 4000)
    return () => {
      es.close()
      clearInterval(poll)
      clearTimeout(timer)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id])

  // Coming back from PayPal with the browser's back button can restore this page from
  // the cache with "Creating PayPal order…" still showing: clear it.
  useEffect(() => {
    const onShow = (e: PageTransitionEvent) => {
      if (e.persisted) {
        setBusy('')
        refresh()
      }
    }
    window.addEventListener('pageshow', onShow)
    return () => window.removeEventListener('pageshow', onShow)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id])

  const mission = detail?.mission
  const mandate = detail?.mandate
  const currency = mandate?.currency ?? 'USD'
  const proposals = detail?.proposals ?? []
  const last: Proposal | undefined = proposals[proposals.length - 1]
  // While the agent revises a cart that was sent back (or blocked by the
  // mandate), that cart is history, not the current proposal.
  const revising = mission?.status === 'running' && !!last && (last.status === 'rejected' || last.status === 'rejected_by_policy')
  const latest: Proposal | undefined = revising ? undefined : last
  const payments = detail?.payments ?? []
  // Only the current proposal's own order is shown; older orders live under "Earlier proposals".
  const payment: Payment | undefined = latest ? payments.find((p) => p.proposal_id === latest.id) : undefined

  const proposedIds = useMemo(() => {
    const s = new Set<string>()
    if (latest && ['pending_approval', 'auto_approved', 'approved', 'paid', 'refunded'].includes(latest.status)) latest.cart.items.forEach((i) => s.add(i.product_id))
    return s
  }, [latest])

  const candidates = useMemo<Candidate[]>(() => {
    const map = new Map<string, Candidate>()
    for (const ev of events) {
      if (ev.type !== 'candidates' || !ev.data?.products) continue
      for (const p of ev.data.products as Product[]) {
        if (!map.has(p.id)) map.set(p.id, { ...p, query: ev.data.query })
      }
    }
    // What went into the cart first; the rest in the order the agent found them.
    return [...map.values()].sort((a, b) => Number(proposedIds.has(b.id)) - Number(proposedIds.has(a.id)))
  }, [events, proposedIds])

  // One search means the same query on every row: the column would only repeat it.
  const searchCount = useMemo(() => new Set(candidates.map((c) => c.query)).size, [candidates])
  const candidateCols = useMemo<ColDef<Candidate>[]>(
    () => [
      { field: 'title', headerName: 'Product', flex: 2.4, minWidth: phone ? 160 : 220, cellRenderer: ProductCell, cellRendererParams: { compactLine: phone, inCart: (id: string) => proposedIds.has(id) }, tooltipField: 'title', autoHeight: true, wrapText: true },
      { field: 'merchant', headerName: 'Merchant', minWidth: 120, suppressHeaderFilterButton: true, hide: phone },
      { field: 'category', headerName: 'Category', minWidth: 110, suppressHeaderFilterButton: true, hide: narrow },
      { field: 'price', headerName: 'Price', valueFormatter: (p) => money(p.value, p.data?.currency), filter: 'agNumberColumnFilter', ...MONEY, minWidth: 110, hide: phone },
      { field: 'rating', headerName: 'Rating', valueFormatter: (p) => (p.value ? Number(p.value).toFixed(1) : '—'), filter: 'agNumberColumnFilter', suppressHeaderFilterButton: true, minWidth: 88, maxWidth: 100, cellClass: NUM, hide: phone },
      { field: 'reviews', headerName: 'Reviews', valueFormatter: (p) => (p.value ? Number(p.value).toLocaleString() : '—'), filter: 'agNumberColumnFilter', suppressHeaderFilterButton: true, minWidth: 100, maxWidth: 110, cellClass: NUM, hide: narrow },
      { field: 'query', headerName: 'Found by search', minWidth: 170, tooltipField: 'query', suppressHeaderFilterButton: true, hide: narrow || searchCount <= 1 },
      {
        colId: 'outcome',
        headerName: 'Outcome',
        valueGetter: (p) => (p.data && proposedIds.has(p.data.id) ? 'in cart' : 'considered'),
        filter: false,
        minWidth: 115,
        maxWidth: 125,
        hide: phone,
        comparator: (a: string, b: string) => (a === 'in cart' ? 0 : 1) - (b === 'in cart' ? 0 : 1),
        cellClass: (p) => (p.value === 'in cart' ? 'cell-status cell-good' : 'muted'),
        headerTooltip: 'Rows in the proposed cart are highlighted',
      },
    ],
    [proposedIds, narrow, phone, searchCount],
  )
  const rowClassRules = useMemo<RowClassRules<Candidate>>(() => ({ 'row-proposed': (p) => !!p.data && proposedIds.has(p.data.id) }), [proposedIds])

  const itemCols = useMemo<ColDef<CartItem>[]>(
    () => [
      { field: 'title', headerName: 'Item', flex: 2, minWidth: phone ? 150 : 220, cellRenderer: CartItemCell, cellRendererParams: { showQty: phone }, autoHeight: true, wrapText: true },
      { field: 'merchant', headerName: 'Merchant', minWidth: 100, maxWidth: 140, hide: phone },
      { field: 'quantity', headerName: 'Qty', minWidth: 60, maxWidth: 70, ...MONEY, filter: false, hide: phone },
      { headerName: 'Line', valueGetter: (p) => (p.data ? p.data.unit_price * p.data.quantity : 0), valueFormatter: (p) => money(p.value, p.data?.currency), ...MONEY, minWidth: 92, maxWidth: 108, filter: false },
    ],
    [phone],
  )

  const approve = async () => {
    if (!latest) return
    setBusy('approve')
    setActionErr('')
    try {
      const r = await api.approve(latest.id)
      if (r.approve_url) {
        window.location.assign(r.approve_url)
        return
      }
      await refresh()
      setBusy('')
    } catch (e: any) {
      setActionErr(e.message)
      setBusy('')
    }
  }
  const reject = async (why?: string) => {
    if (!latest) return
    setBusy('reject')
    setActionErr('')
    try {
      await api.reject(latest.id, (why ?? reason).trim())
      setRejecting(false)
      setReason('')
      await refresh()
    } catch (e: any) {
      setActionErr(e.message)
    } finally {
      setBusy('')
    }
  }
  const applySuggestion = async () => {
    if (!mission) return
    setBusy('apply')
    setActionErr('')
    try {
      const r = await api.applySuggestion(mission.id)
      navigate(`#/missions/${r.mission.id}`)
    } catch (e: any) {
      setActionErr(e.message)
    } finally {
      setBusy('')
    }
  }
  const sync = async () => {
    if (!payment) return
    setBusy('sync')
    setActionErr('')
    try {
      await api.syncPayment(payment.id)
      await refresh()
    } catch (e: any) {
      setActionErr(e.message)
    } finally {
      setBusy('')
    }
  }
  const copyId = () => {
    if (!mission) return
    navigator.clipboard?.writeText(mission.id).catch(() => {})
    setCopied(true)
    setTimeout(() => setCopied(false), 1200)
  }
  const toggleVerbose = () => {
    setVerbose((v) => {
      try {
        localStorage.setItem('mandate.log.verbose', v ? '0' : '1')
      } catch {
        /* private mode */
      }
      return !v
    })
  }

  if (!detail || !mission) {
    return (
      <div className="page mission">
        <p className="sr-only" role="status" aria-live="polite">Loading mission…</p>
        {loadErr ? <div className="alert bad" role="alert">{loadErr}</div> : <Empty>Loading mission…</Empty>}
      </div>
    )
  }

  const running = mission.status === 'running'
  // An approved cart whose PayPal order is still open, or captured at PayPal but not yet booked.
  const orderOpen = latest?.status === 'approved' && !!payment && (!isFinalPayment(payment.status) || (isCaptured(payment.status) && !payment.accounted))
  const unpaidOrder = orderOpen && !isCaptured(payment!.status)
  const capturedUnbooked = orderOpen && isCaptured(payment!.status)
  const canDecide = !!latest && (PAYABLE.includes(latest.status) || unpaidOrder) && !running
  const remaining = mandate ? remainingOf(mandate) : 0
  // A cart still waiting for approval is judged against the mandate as it is now, not as it was when proposed.
  const live: Decision | undefined = latest ? detail.live_decisions?.[latest.id] : undefined
  const steps = deriveSteps(events, mission, proposals, revising, payment)
  const story = groupStory(events, verbose, proposals, !!mission.negotiation?.rounds?.length)
  const eventCount = events.filter((e) => !(e.type === 'status' && e.message === 'agent started')).length
  const lastDoneId = [...events].reverse().find((e) => e.type === 'done')?.id
  const settled = latest?.status === 'paid' || latest?.status === 'refunded'
  // A ?payment= banner only speaks while the data still agrees with it.
  const banner = params.get('payment')
  const showBanner =
    (banner === 'completed' && settled) ||
    (banner === 'pending' && payment?.status === 'PENDING') ||
    (banner === 'failed' && payment?.status === 'CAPTURE_FAILED') ||
    (banner === 'cancelled' && payment?.status === 'CANCELLED') ||
    (banner === 'denied' && payment?.status === 'DENIED')
  const announce = running
    ? revising ? 'The agent is revising the cart.' : 'The agent is working.'
    : settled ? `Paid through PayPal: ${money(latest!.decision.total, latest!.decision.currency)}.`
    : canDecide && !unpaidOrder ? `The agent proposes ${latest!.cart.items.length} item${latest!.cart.items.length === 1 ? '' : 's'} for ${money(latest!.decision.total, latest!.decision.currency)}. Your approval is needed.`
    : unpaidOrder ? 'Approved. Finish paying at PayPal.'
    : label(mission.status)

  return (
    <div className="page mission">
      <p className="sr-only" role="status" aria-live="polite" aria-atomic="true">{announce}</p>
      {showBanner && banner === 'completed' && <div className="banner good">✓ Payment captured through PayPal. The mandate's spent total is updated.</div>}
      {showBanner && banner === 'cancelled' && <div className="banner warn">Checkout was cancelled at PayPal. The budget reservation was released.</div>}
      {showBanner && banner === 'failed' && <div className="banner bad">PayPal could not capture the payment. Check the status below, or approve the cart again.</div>}
      {showBanner && banner === 'denied' && <div className="banner bad">PayPal declined the capture. The reservation was released; approve the cart again to pay through a new checkout.</div>}
      {showBanner && banner === 'pending' && <div className="banner warn">PayPal accepted the payment but the capture is still pending; the mandate books it as soon as PayPal completes it.</div>}

      <div className="mission-top">
        <div className="crumbs">
          <a href="#/">← Mandates</a>
          <button type="button" className="link mono id" aria-label="Copy mission ID" title="Copy mission ID" onClick={copyId}>{copied ? 'copied' : mission.id}</button>
        </div>
        <h1 title={mission.goal} tabIndex={-1} id="page-title">{keepHyphens(mission.goal)}</h1>
        <div className="status-line">
          <Pill status={mission.status} />
          {mandate && <span className="stat">{mandate.name}</span>}
          {mandate && (
            <span className="stat num">
              {money(remaining, currency)} left of {money(mandate.total_budget, currency)}
              {mandate.reserved > 0 && <span className="reserved"> · {money(mandate.reserved, currency)} reserved</span>}
            </span>
          )}
          {mandate && mandate.max_per_item > 0 && <span className="stat num">≤ {money(mandate.max_per_item, currency)} per item</span>}
        </div>
        {mission.retry_of && <div className="lineage">Retry of <a href={`#/missions/${mission.retry_of}`}>an earlier mission</a> after you amended the mandate.</div>}
        {mission.repeat_of && <div className="lineage">Run of <a href={`#/missions/${mission.repeat_of}`}>a standing order</a>.</div>}
        {(mission.repeat_days ?? 0) > 0 && (
          <div className="so-strip">
            <div>
              <b><Glyph>⟳</Glyph> Standing order:</b> every {mission.repeat_days} days, next {shortDate(mission.next_run_at)}.{' '}
              <button type="button" className="link why-toggle" onClick={() => setWhyOpen((v) => !v)} aria-expanded={whyOpen}>why?</button>
              <span className={`why${whyOpen ? ' open' : ''}`}>Every run is a new mission checked against the mandate; with a saved PayPal account and a cart under the threshold it completes without you.</span>
            </div>
            <StandingOrderButtons mission={mission} onChanged={refresh} />
          </div>
        )}
      </div>
      {actionErr && <div className="alert bad" role="alert">{actionErr}</div>}
      {loadErr && <div className="alert bad" role="alert">Could not refresh this mission: {loadErr}</div>}

      <Tracker steps={steps} size="full" label="Mission progress" />

      <DecisionCard
        mission={mission}
        mandate={mandate}
        latest={latest}
        proposals={proposals}
        payment={payment}
        events={events}
        story={story}
        steps={steps}
        currency={currency}
        remaining={remaining}
        live={live}
        phone={phone}
        running={running}
        revising={revising}
        unpaidOrder={unpaidOrder}
        capturedUnbooked={capturedUnbooked}
        settled={settled}
        canDecide={canDecide}
        busy={busy}
        rejecting={rejecting}
        reason={reason}
        demoBuyerHint={config?.demo_buyer_hint}
        mock={config?.paypal_mode === 'mock'}
        itemCols={itemCols}
        onApprove={approve}
        onReject={reject}
        onAskCheaper={() => reject(`The budget moved: stay under ${money(remaining, currency)}.`)}
        onRejecting={setRejecting}
        onReason={setReason}
        onApply={applySuggestion}
        onSync={sync}
      />

      {proposals.filter((p) => p !== latest).length > 0 && (
        <details className="earlier">
          <summary>Earlier proposals ({proposals.filter((p) => p !== latest).length})</summary>
          {phone ? (
            <ul className="earlier-list">
              {proposals.map((p, i) =>
                p === latest ? null : (
                  <li key={p.id}>
                    <div className="line num">#{i + 1} · {money(p.decision.total, p.decision.currency)} <Pill status={p.status} /></div>
                    <div>{p.cart.items.map((it) => it.title).join(', ')}</div>
                    {p.feedback && <div className="muted">“{p.feedback}”</div>}
                  </li>
                ),
              )}
            </ul>
          ) : (
          <div className="table-wrap">
            <table className="table">
              <tbody>
                {proposals.map((p, i) =>
                  p === latest ? null : (
                    <tr key={p.id}>
                      <td className="num">#{i + 1}</td>
                      <td>{p.cart.items.map((it) => it.title).join(', ')}</td>
                      <td className="num">{money(p.decision.total, p.decision.currency)}</td>
                      <td><Pill status={p.status} /></td>
                      <td className="muted">{p.feedback ? `“${p.feedback}”` : '—'}</td>
                    </tr>
                  ),
                )}
              </tbody>
            </table>
          </div>
          )}
        </details>
      )}

      <Card
        title="How the agent got here"
        className="mt"
        action={
          <span className="actions head-meta">
            {running && <Pill status="running">working…</Pill>}
            <button type="button" className="link" onClick={toggleVerbose}>{verbose ? 'Show the story' : `Show every event (${eventCount})`}</button>
          </span>
        }
      >
        <div className="story">
          {story.length === 0 && <Empty>{running ? 'Waiting for the agent…' : 'No events.'}</Empty>}
          {story.map((it) => <StoryRow key={storyKey(it)} it={it} currency={currency} finalNote={(ev) => (ev.id === lastDoneId && showFinalNote(mission, latest, events) ? mission.summary : undefined)} />)}
        </div>
      </Card>

      {mission.kind !== 'negotiate' && (
        <Card
          title="Products compared"
          className="mt"
          action={
            <span className="muted num head-meta">
              {candidates.length} product{candidates.length === 1 ? '' : 's'} · {candidates.filter((c) => proposedIds.has(c.id)).length} in the cart
              {candidates[0]?.source === 'channel3' ? ' · live retail data via Channel3' : candidates[0]?.source === 'mock' ? ' · demo catalog' : ''}
            </span>
          }
        >
          {candidates.length === 0 ? (
            <Empty>No searches yet.</Empty>
          ) : (
            <Grid<Candidate> rowData={candidates} columnDefs={candidateCols} getRowId={(p) => p.data.id} rowClassRules={rowClassRules} pagination={candidates.length > 20} paginationPageSize={20} />
          )}
        </Card>
      )}
    </div>
  )
}

function showFinalNote(mission: Mission, latest: Proposal | undefined, events: Event[]) {
  if (!mission.summary || mission.status === 'no_proposal') return false
  const note = mission.summary.trim()
  if (note === (latest?.summary ?? '').trim()) return false
  const lastSaid = [...events].reverse().find((e) => e.type === 'assistant')?.message.trim()
  return note !== lastSaid
}

function mergeEvents(prev: Event[], incoming: Event[]) {
  if (incoming.length === 0) return prev
  const seen = new Set(prev.map((e) => e.id))
  const add = incoming.filter((e) => !seen.has(e.id))
  if (add.length === 0) return prev
  return [...prev, ...add].sort((a, b) => a.id - b.id)
}

// --- the storyline tracker ---

function deriveSteps(events: Event[], mission: Mission, proposals: Proposal[], revising: boolean, payment?: Payment): TrackerStep[] {
  // After the human sends a cart back, the story starts over: only events of the current try count.
  const epoch = [...events].reverse().find((e) => e.type === 'rejected')?.id ?? -1
  const has = (f: (e: Event) => boolean) => events.some((e) => e.id > epoch && f(e))
  const negotiate = mission.kind === 'negotiate'
  const latest = proposals[proposals.length - 1]
  const settled = latest?.status === 'paid' || latest?.status === 'refunded'
  const done: Record<string, boolean> = {
    search: has((e) => e.type === 'candidates'),
    compare: has((e) => e.type === 'check' || (e.type === 'tool_call' && e.data?.tool === 'get_product')),
    // The haggling is over once a side accepts, walks away or declines, or the run ends.
    negotiate: has((e) => e.type === 'proposal' || e.type === 'done' || (e.type === 'negotiation' && ['accept', 'walk_away', 'decline'].includes(e.data?.move))),
    check: has((e) => e.type === 'check' || e.type === 'policy_block' || (negotiate && e.type === 'proposal')),
    propose: has((e) => e.type === 'proposal'),
    pay: mission.status === 'paid' || settled,
  }
  const keys = negotiate ? ['negotiate', 'check', 'propose', 'pay'] : ['search', 'compare', 'check', 'propose', 'pay']
  const labels: Record<string, string> = { search: 'Search', compare: 'Compare', negotiate: 'Negotiate', check: 'Check mandate', propose: revising ? 'Revise' : 'Propose', pay: latest?.status === 'refunded' ? 'Paid · refunded' : 'Approve & pay' }
  const steps: TrackerStep[] = keys.map((k) => ({ key: k, label: labels[k], state: done[k] ? 'done' : 'future' }))
  for (let i = steps.length - 2; i >= 0; i--) if (steps[i + 1].state === 'done') steps[i].state = 'done'
  const firstOpen = steps.findIndex((s) => s.state !== 'done')
  const live = mission.status === 'running' || mission.status === 'awaiting_payment' || mission.status === 'awaiting_approval'
  if (live && firstOpen >= 0) {
    const cur = steps[firstOpen]
    cur.state = 'current'
    if (cur.key === 'pay') {
      if (mission.status === 'awaiting_approval') Object.assign(cur, { note: 'your turn', yours: true })
      else if (payment?.status === 'PENDING') cur.note = 'capture pending at PayPal'
      else if (payment && !isCaptured(payment.status)) Object.assign(cur, { note: 'finish at PayPal', yours: true })
    }
    if (cur.key === (negotiate ? 'negotiate' : 'search') && revising) {
      cur.note = latest?.status === 'rejected_by_policy' ? 'after the mandate blocked a cart' : latest?.feedback ? 'after your feedback' : 'after you sent it back'
    }
  } else if (mission.status === 'no_proposal') {
    const p = steps.find((s) => s.key === 'propose')
    if (p && p.state !== 'done') Object.assign(p, { state: 'failed', label: negotiate ? 'No deal' : 'No proposal' })
  } else if ((mission.status === 'failed' || mission.status === 'cancelled') && firstOpen >= 0) {
    Object.assign(steps[firstOpen], { state: 'failed', label: mission.status === 'failed' ? 'Failed' : 'Cancelled' })
  }
  // "Revised" only when the human sent an earlier cart back, not when the mandate blocked a draft.
  const prior = proposals.slice(0, -1)
  if (!revising && latest && latest.status !== 'rejected' && prior.some((p) => p.status === 'rejected')) {
    const p = steps.find((s) => s.key === 'propose')
    if (p && p.state === 'done') Object.assign(p, { state: 'revised', label: 'Revised', note: prior.some((x) => x.feedback) ? 'after your feedback' : 'after you sent it back' })
  }
  return steps
}

// --- the story log ---

type StoryItem =
  | { kind: 'row'; ev: Event }
  | { kind: 'group'; events: Event[]; label: string }
  | { kind: 'checks'; events: Event[] }
  | { kind: 'nego'; events: Event[] }
  | { kind: 'fold'; label: string; items: StoryItem[]; first: number }

const storyKey = (it: StoryItem): string =>
  it.kind === 'row' ? `r${it.ev.id}` : it.kind === 'fold' ? `f${it.first}` : `${it.kind}${it.events[0].id}`

const failingCheck = (ev: Event) => ev.type === 'check' && (ev.data?.decision?.violations?.length ?? 0) > 0
const negotiationEvent = (ev: Event) => ev.type === 'negotiation' || (ev.type === 'status' && ev.message.startsWith('negotiation started:'))

function isStory(ev: Event, verbose: boolean) {
  if (verbose) return true
  if (ev.type === 'tool_result' || ev.type === 'mcp_tool_result') return !!ev.data?.error
  if (ev.type === 'status' && ev.message === 'agent started') return false
  return STORY_TYPES.has(ev.type)
}

/** Story items for one try. With a negotiation shown above, its moves fold into one row. */
function storyItems(events: Event[], foldNegotiation: boolean): StoryItem[] {
  const out: StoryItem[] = []
  let run: Event[] = []
  const flush = () => {
    // Plain tool calls are already told by the rows around them; only PayPal MCP calls earn a row.
    const mcp = run.filter((e) => e.type === 'mcp_tool_call')
    if (mcp.length > 0) {
      const counts = new Map<string, number>()
      for (const e of mcp) counts.set(e.message, (counts.get(e.message) ?? 0) + 1)
      const names = [...counts.entries()].map(([n, c]) => (c > 1 ? `${n} ×${c}` : n)).join(', ')
      out.push({ kind: 'group', events: run, label: `${mcp.length} PayPal MCP call${mcp.length === 1 ? '' : 's'} · ${names}` })
    }
    run = []
  }
  for (const ev of events) {
    if (!isStory(ev, false)) {
      if (!(ev.type === 'status' && ev.message === 'agent started')) run.push(ev)
      continue
    }
    flush()
    const prev = out[out.length - 1]
    if (failingCheck(ev)) {
      if (prev?.kind === 'checks') prev.events.push(ev)
      else out.push({ kind: 'checks', events: [ev] })
      continue
    }
    if (foldNegotiation && negotiationEvent(ev)) {
      if (prev?.kind === 'nego') prev.events.push(ev)
      else out.push({ kind: 'nego', events: [ev] })
      continue
    }
    out.push({ kind: 'row', ev })
  }
  flush()
  return out
}

function groupStory(events: Event[], verbose: boolean, proposals: Proposal[], hasNegotiation: boolean): StoryItem[] {
  if (verbose) return events.map((ev) => ({ kind: 'row', ev }))
  // Earlier tries (before the last time the human sent a cart back) fold into one row.
  let cut = -1
  events.forEach((e, i) => {
    if (e.type === 'rejected') cut = i
  })
  if (cut < 0) return storyItems(events, hasNegotiation)
  const before = events.slice(0, cut + 1)
  const tries = before.filter((e) => e.type === 'rejected').length
  const rejected = proposals.filter((p) => p.status === 'rejected')
  const lastFb = rejected[rejected.length - 1]?.feedback
  const lastCart = rejected[rejected.length - 1]
  const what = lastCart ? `${lastCart.cart.items[0]?.title ?? 'a cart'}${lastCart.cart.items.length > 1 ? ` +${lastCart.cart.items.length - 1}` : ''} for ${money(lastCart.decision.total, lastCart.decision.currency)}` : 'a cart'
  const label = tries === 1
    ? `First try · proposed ${what} · you sent it back${lastFb ? `: “${lastFb}”` : ''}`
    : `${tries} earlier tries · last one ${what}${lastFb ? ` · your feedback: “${lastFb}”` : ''}`
  return [{ kind: 'fold', label, items: storyItems(before, false), first: before[0]?.id ?? 0 }, ...storyItems(events.slice(cut + 1), hasNegotiation)]
}

/** One line for a folded negotiation: rounds and how far the price moved. */
function negotiationLine(events: Event[], currency: string) {
  const moves = events.filter((e) => e.type === 'negotiation')
  const rounds = moves.reduce((m, e) => Math.max(m, (e.data?.round ?? 0) + 1), 0)
  const first = moves.find((e) => e.data?.party === 'seller' && e.data?.price > 0)?.data?.price
  const last = [...moves].reverse().find((e) => e.data?.price > 0)
  const end = last?.data?.move === 'accept' ? `agreed at ${money(last.data.price, currency)}` : last ? `last price ${money(last.data.price, currency)}` : ''
  return `Negotiated ${rounds} round${rounds === 1 ? '' : 's'}${first ? ` · asked ${money(first, currency)}` : ''}${end ? ` → ${end}` : ''} · transcript above`
}

/** The friendly first line of a story row; the raw message stays in the row's title. */
function friendly(ev: Event, currency: string): ReactNode {
  const d = ev.data ?? {}
  switch (ev.type) {
    case 'status': {
      if (ev.message.startsWith('mission created:')) return 'Mission started'
      if (ev.message.startsWith('negotiation started:') && d.limit) return `Negotiation started · the seller asks ${money(d.list_price, currency)}, your agent's limit is ${money(d.limit, currency)}`
      if (ev.message.startsWith('standing order:') && d.repeat_days) return `Standing order · runs again every ${d.repeat_days} days, next ${shortDate(d.next_run_at)}`
      return capitalize(ev.message)
    }
    case 'candidates': {
      const n = Array.isArray(d.products) ? d.products.length : 0
      return <>Searched “{d.query}” · <span className="num">{n}</span> product{n === 1 ? '' : 's'}</>
    }
    case 'check': {
      const v = d.decision?.violations?.length ?? 0
      const p = d.decision?.checks_passed?.length ?? 0
      return v > 0 ? `Cart broke the mandate · ${v} problem${v === 1 ? '' : 's'}` : `Cart passes the mandate · ${p} of ${p} checks`
    }
    case 'proposal': {
      const m = /(\d+) items?/.exec(ev.message)
      const items = m ? `${m[1]} item${m[1] === '1' ? '' : 's'} · ` : ''
      return `Proposed ${items}${money(d.total, currency)} · ${d.status === 'auto_approved' ? 'under your auto-pay limit' : 'needs your approval'}`
    }
    case 'approved': {
      const items = `${d.items} item${d.items === 1 ? '' : 's'}`
      const how = d.vaulted ? 'paying with your saved PayPal' : 'sent to PayPal checkout'
      return d.by === 'threshold' ? `Auto-approved under your auto-pay limit · ${items} · ${money(d.total, currency)} · ${how}` : `You approved ${items} · ${money(d.total, currency)} · ${how}`
    }
    case 'policy_block':
      return ev.message.startsWith('approval refused') ? 'Approval refused: the cart no longer fits the mandate' : 'The mandate blocked the proposed cart'
    case 'payment':
      if (ev.message.startsWith('PayPal order') && ev.message.includes('created')) return ev.message.includes('saved on this mandate') ? 'PayPal order created with your saved account · no checkout' : 'PayPal checkout opened'
      return capitalize(ev.message)
    case 'paid':
      return `Payment captured · ${money(d.amount, currency)} · capture ${d.capture_id ?? ''}${d.vaulted ? ' · saved PayPal account' : ''}`
    case 'rejected': {
      const why = ev.message.replace(/^human rejected the proposal:?\s*/, '')
      return why ? `You sent the proposal back: “${why}”` : 'You sent the proposal back'
    }
    case 'suggestion':
      return d.field ? `Suggested a mandate change: ${describeSuggestion({ field: d.field, value: d.value }, currency)}` : capitalize(ev.message)
    case 'negotiation': {
      const who = d.party === 'buyer' ? 'Buyer agent' : 'Seller agent'
      const verb: Record<string, string> = { offer: 'offers', counter: 'asks', accept: 'accepts', decline: 'declines', walk_away: 'walks away' }
      const said = ev.message.includes(': ') ? ev.message.slice(ev.message.indexOf(': ') + 2) : ''
      return <>{who} {verb[d.move] ?? d.move}{d.price > 0 ? <> <span className="num">{money(d.price, currency)}</span></> : null}{said ? <span className="muted"> · {said}</span> : null}</>
    }
    case 'vault':
      if (ev.message.startsWith('the saved PayPal account could not be used')) return 'Saved PayPal account could not be used · removed from the mandate, the next checkout saves a new one'
      if (ev.message.startsWith('PayPal account saved on this mandate')) return 'PayPal account saved on this mandate · next carts pay with one click'
      return capitalize(ev.message)
    case 'done':
      return 'Agent finished'
    case 'tool_call':
      return `Calls ${ev.message}`
    default:
      return capitalize(ev.message)
  }
}

function rowTone(ev: Event) {
  if (failingCheck(ev)) return ' ev-check-bad'
  if (ev.type === 'negotiation') return ev.data?.party === 'buyer' ? ' ev-buyer' : ' ev-seller'
  return ''
}
function rowIcon(ev: Event) {
  if (failingCheck(ev)) return '⊘'
  if (ev.type === 'negotiation') return ev.data?.party === 'buyer' ? '→' : '←'
  return ICON[ev.type] ?? '•'
}

/** The latest thing the agent did, for the "working" card. */
function liveLine(story: StoryItem[], currency: string): ReactNode {
  const last = [...story].reverse().find((it) => it.kind !== 'fold')
  if (!last) return <span className="muted">Waiting for the agent…</span>
  if (last.kind === 'row') return last.ev.type === 'assistant' ? <Markdown text={last.ev.message} /> : friendly(last.ev, currency)
  if (last.kind === 'checks') return last.events.length > 1 ? `Tried ${last.events.length} carts · each broke the mandate` : friendly(last.events[0], currency)
  if (last.kind === 'nego') return friendly(last.events[last.events.length - 1], currency)
  return last.label
}

function StoryRow({ it, currency, finalNote }: { it: StoryItem; currency: string; finalNote: (ev: Event) => string | undefined }) {
  if (it.kind === 'row') return <EventRow ev={it.ev} currency={currency} finalNote={finalNote(it.ev)} />
  if (it.kind === 'group') {
    return (
      <details className="log-group">
        <summary><span className="ico" aria-hidden="true">⇄</span>{it.label}</summary>
        <div className="inner">
          {it.events.map((ev) => <EventRow key={ev.id} ev={ev} currency={currency} />)}
        </div>
      </details>
    )
  }
  if (it.kind === 'nego') {
    return (
      <details className="log-group ev-nego">
        <summary><span className="ico" aria-hidden="true">⇌</span>{negotiationLine(it.events, currency)}</summary>
        <div className="inner">
          {it.events.map((ev) => <EventRow key={ev.id} ev={ev} currency={currency} />)}
        </div>
      </details>
    )
  }
  if (it.kind === 'checks') {
    const n = it.events.length
    const all = it.events.flatMap((e) => (e.data?.decision?.violations ?? []) as Violation[])
    const at = new Date(it.events[n - 1].at)
    return (
      <div className="log-row ev-check-bad">
        <div className="ico" aria-hidden="true">⊘</div>
        <div className="body">
          <div className="msg">{n === 1 ? friendly(it.events[0], currency) : `Tried ${n} carts · each broke the mandate`}</div>
          {n === 1 ? (
            <Violations vs={all} />
          ) : (
            <details>
              <summary>why</summary>
              <Violations vs={all} />
            </details>
          )}
        </div>
        <div className="time num" title={at.toLocaleString()}>{at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</div>
      </div>
    )
  }
  return (
    <details className="log-fold">
      <summary><span className="ico" aria-hidden="true">↺</span><span>{it.label}</span></summary>
      <div className="inner">
        {it.items.map((x) => <StoryRow key={storyKey(x)} it={x} currency={currency} finalNote={() => undefined} />)}
      </div>
    </details>
  )
}

function EventRow({ ev, currency, finalNote }: { ev: Event; currency: string; finalNote?: string }) {
  const time = new Date(ev.at)
  let details: ReactNode = null
  if ((ev.type === 'tool_call' || ev.type === 'mcp_tool_call') && ev.data?.input) {
    details = (
      <details>
        <summary>input</summary>
        <pre>{JSON.stringify(ev.data.input, null, 2)}</pre>
      </details>
    )
  } else if (ev.type === 'check' && ev.data?.decision) {
    const d = ev.data.decision
    details = d.violations?.length ? (
      <Violations vs={d.violations} />
    ) : (
      <div className="check-results">
        {d.checks_passed?.map((c: string) => <span key={c} className="check-ok"><Glyph>✓</Glyph> {CHECK_LABEL[c] ?? c}</span>)}
      </div>
    )
  } else if (ev.type === 'policy_block' && ev.data?.decision?.violations?.length) {
    details = <Violations vs={ev.data.decision.violations} />
  } else if (ev.type === 'candidates' && ev.data?.products) {
    const ps = ev.data.products as Product[]
    details = (
      <details>
        <summary>{ps.length} products</summary>
        <pre>{ps.map((p) => `${money(p.price, p.currency).padStart(10)}  ${p.rating ?? '-'}★  ${p.title} (${p.merchant})`).join('\n')}</pre>
      </details>
    )
  }
  const isStatus = ev.type === 'status'
  return (
    <div className={`log-row ev-${ev.type}${rowTone(ev)}${isStatus ? ' status' : ''}`} title={ev.message}>
      <div className="ico" aria-hidden="true">{rowIcon(ev)}</div>
      <div className="body">
        {ev.type === 'assistant' ? (
          <div className="msg md bubble agent"><Markdown text={ev.message} /></div>
        ) : (
          <div className="msg">{friendly(ev, currency)}</div>
        )}
        {details}
        {finalNote && (
          <div className="summary final-note md">
            <div className="summary-label">Agent's final note</div>
            <Markdown text={finalNote} />
          </div>
        )}
      </div>
      <div className="time num" title={time.toLocaleString()}>{time.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</div>
    </div>
  )
}

// --- the decision card ---

function DecisionCard(p: {
  mission: Mission
  mandate: Mandate | null | undefined
  latest?: Proposal
  proposals: Proposal[]
  payment?: Payment
  events: Event[]
  story: StoryItem[]
  steps: TrackerStep[]
  currency: string
  remaining: number
  live?: Decision
  phone: boolean
  running: boolean
  revising: boolean
  unpaidOrder: boolean
  capturedUnbooked: boolean
  settled: boolean
  canDecide: boolean
  busy: string
  rejecting: boolean
  reason: string
  demoBuyerHint?: string
  mock: boolean
  itemCols: ColDef<CartItem>[]
  onApprove: () => void
  onReject: () => void
  onAskCheaper: () => void
  onRejecting: (v: boolean) => void
  onReason: (v: string) => void
  onApply: () => void
  onSync: () => void
}) {
  const { mission, mandate, latest, proposals, payment, currency, running, revising, unpaidOrder, capturedUnbooked, settled, canDecide, busy, phone } = p
  const n = mission.negotiation
  const hasNeg = !!n?.rounds?.length
  // "Failed" only wins when nothing is left to act on: a waiting or paid cart keeps its card.
  const failed = mission.status === 'failed' && !settled && !canDecide && !unpaidOrder && !capturedUnbooked
  const state = failed ? 'failed' : hasNeg ? 'negotiate' : mission.status === 'no_proposal' ? 'no_proposal' : settled ? 'paid' : latest ? 'proposal' : 'running'
  // The decision as it stands now: a waiting cart is re-checked against today's mandate.
  const decision = (canDecide && !unpaidOrder && p.live) || latest?.decision
  const stale = !!p.live && !p.live.allowed && canDecide && !unpaidOrder
  // The head band's colour: green when paid, amber when it is your turn, blue while PayPal or the agent works, grey when nothing came of it.
  const yourTurn = canDecide || unpaidOrder
  const tone = settled ? 'good' : yourTurn ? 'warn' : capturedUnbooked || running ? 'live' : state === 'failed' || state === 'no_proposal' || mission.status === 'cancelled' ? 'neutral' : 'accent'

  const total = latest ? money(latest.decision.total, latest.decision.currency) : ''
  // One figure per meaning: a projection while you decide, a reservation while PayPal is open, the source once paid.
  const sub = !latest || !decision
    ? ''
    : settled ? (latest.status === 'refunded' ? 'refunded to the mandate' : `spent from ${mandate?.name ?? 'the mandate'}`)
    : unpaidOrder || capturedUnbooked ? `reserved · ${money(p.remaining, currency)} left`
    : stale ? `${money(Math.abs(decision.remaining_after), decision.currency)} over · only ${money(decision.remaining_budget, decision.currency)} left`
    : canDecide ? `${money(decision.remaining_after, decision.currency)} left after`
    : ''

  const titleFor = (pr: Proposal) =>
    settled ? (pr.status === 'refunded' ? 'Refunded through PayPal' : 'Paid through PayPal')
    : capturedUnbooked ? (payment?.status === 'PENDING' ? 'Payment pending at PayPal' : 'Paid at PayPal · not yet booked')
    : unpaidOrder ? 'Approved · finish paying at PayPal'
    : stale ? 'No longer fits the mandate'
    : canDecide ? 'The agent proposes'
    : pr.status === 'rejected_by_policy' ? 'The cart the mandate blocked'
    : 'The agent proposed'

  const head = (title: ReactNode, extra?: ReactNode, amount?: boolean) => (
    <div className="decision-head">
      <div className="title"><h3>{title}</h3>{extra}</div>
      {amount && latest && (
        <div className="amount">
          <span className="num num-xl">{total}</span>
          {sub && <small className={`num${stale ? ' over' : ''}`}>{sub}</small>}
        </div>
      )}
    </div>
  )

  // Why an auto-approved cart still needs a checkout: the saved PayPal account failed.
  const vaultFailed = unpaidOrder && latest && !latest.decision.requires_approval && p.events.some((e) => e.type === 'vault' && e.message.startsWith('the saved PayPal account could not be used'))
  const prev = latest ? proposals[proposals.indexOf(latest) - 1] : undefined
  // The scripted agent's summary only lists the items again, and a deal's summary repeats the negotiation above.
  const repeatSummary = latest ? /^Proposed \d+ items?:/.test(latest.summary) || state === 'negotiate' : false
  const errorNote = mission.error && !failed ? <div className="alert warn">The agent stopped with an error after this point: {mission.error}. The cart below is unaffected.</div> : null

  const approveLabel = busy === 'approve'
    ? mandate?.has_saved_payment ? 'Paying with saved PayPal…' : 'Creating PayPal order…'
    : phone ? (latest?.status === 'auto_approved' ? 'Continue to PayPal' : 'Approve & pay')
    : mandate?.has_saved_payment ? 'Approve & pay with saved PayPal' : latest?.status === 'auto_approved' ? 'Continue to PayPal' : 'Approve & pay with PayPal'

  // The one decision row: approve or reject, or finish an open checkout.
  const decisionRow = () =>
    latest && (
      <>
        {canDecide && !unpaidOrder && (
          <div className={`action-row${p.rejecting ? '' : ' is-sticky'}`}>
            {stale ? (
              <button className="btn primary lg grow" onClick={() => p.onAskCheaper()} disabled={busy !== ''}>{busy === 'reject' ? 'Sending…' : phone ? 'Find a cheaper cart' : 'Ask the agent for a cheaper cart'}</button>
            ) : (
              <button className="btn paypal lg grow" onClick={p.onApprove} disabled={busy !== ''}>{approveLabel}</button>
            )}
            {!p.rejecting && (
              <button className="btn danger lg" onClick={() => p.onRejecting(true)} disabled={busy !== ''}>{phone ? 'Reject' : 'Reject with feedback'}</button>
            )}
            {p.demoBuyerHint && !stale && <span className="hint">Sandbox buyer for testing: {p.demoBuyerHint}</span>}
          </div>
        )}
        {unpaidOrder && payment && (
          <PayActions
            payment={payment}
            busy={busy}
            onSync={p.onSync}
            mock={p.mock}
            hint={p.demoBuyerHint}
            sticky={!p.rejecting}
            withdraw={canDecide && !p.rejecting ? () => p.onRejecting(true) : undefined}
          />
        )}
        {canDecide && p.rejecting && (
          <div className="reject-box">
            <label className="sr-only" htmlFor="reject-reason">Feedback for the agent</label>
            <textarea id="reject-reason" rows={3} value={p.reason} onChange={(e) => p.onReason(e.target.value)} placeholder="What should change? e.g. too expensive, stay under $80" />
            <div className="actions" style={{ justifyContent: 'flex-start' }}>
              <button className="btn danger" onClick={() => p.onReject()} disabled={busy !== ''}>{busy === 'reject' ? 'Sending…' : p.reason.trim() ? 'Send back to the agent' : 'Send back without a reason'}</button>
              <button className="btn ghost" onClick={() => p.onRejecting(false)}>Cancel</button>
            </div>
          </div>
        )}
      </>
    )

  const proposalBody = () =>
    latest && decision && (
      <>
        {prev && prev.status === 'rejected' && (
          <div className="muted secondary" style={{ marginBottom: 8 }}>
            Proposal #{proposals.indexOf(latest) + 1} · {prev.feedback ? <>after your feedback: “{prev.feedback}”</> : <>after you sent #{proposals.indexOf(prev) + 1} back</>}
          </div>
        )}
        {errorNote}
        {stale && <div className="alert warn" style={{ marginTop: 0 }}>The budget moved since the agent proposed this cart: it no longer fits the mandate, so PayPal would refuse it. Ask the agent for a cheaper cart, or reject it.</div>}
        {vaultFailed && <div className="alert warn" style={{ marginTop: 0 }}>Your saved PayPal account could not be charged, so this cart needs one checkout. The checkout saves a new account for next time.</div>}
        <Grid<CartItem> rowData={latest.cart.items} columnDefs={p.itemCols} getRowId={(r) => r.data.product_id} compact />
        <ChecksLine latest={latest} decision={decision} vaultFailed={!!vaultFailed} />
        {latest.summary && !repeatSummary && (
          <div className="summary md">
            <div className="summary-label">Why this cart</div>
            <Markdown text={latest.summary} />
          </div>
        )}
        {latest.feedback && <div className="alert warn">Your feedback: {latest.feedback}</div>}
      </>
    )

  const receipt = payment && (settled || capturedUnbooked) && (
    <>
      <div className="divider" />
      <div className="receipt-head">
        <h4>PayPal receipt</h4>
        {p.mock && <Pill status="mock">simulated</Pill>}
      </div>
      <dl className="receipt">
        <dt>Order</dt><dd className="mono">{payment.id}</dd>
        <dt>Amount</dt><dd className="num">{money(payment.amount, payment.currency)}</dd>
        {payment.capture_id && <><dt>Capture</dt><dd className="mono">{payment.capture_id}</dd></>}
        {payment.payer_email && <><dt>Payer</dt><dd>{payment.payer_email}</dd></>}
        <dt>Paid via</dt><dd>{payment.vaulted ? 'saved PayPal account, no checkout' : 'PayPal checkout, approved by you'}</dd>
        {(payment.refunded ?? 0) > 0 && <><dt>Refunded</dt><dd className="num">{money(payment.refunded, payment.currency)}</dd></>}
      </dl>
      {!payment.accounted && (isCaptured(payment.status) || !isFinalPayment(payment.status)) && <PayActions payment={payment} busy={busy} onSync={p.onSync} mock={p.mock} hint={p.demoBuyerHint} />}
    </>
  )

  const mandateName = mandate?.name ?? 'this mandate'
  const suggestionBox = mission.suggestion && (
    <div className="suggestion-box">
      <div>
        <b><Glyph>✦</Glyph> The agent suggests: {describeSuggestion(mission.suggestion, currency)} on {mandateName}</b>
        <div className="muted" style={{ marginTop: 4 }}>{capitalize(mission.suggestion.reason)}.</div>
        <div className="muted" style={{ marginTop: 4, fontSize: 12.5 }}>Only you can change the mandate; the change also applies to future missions under {mandateName}.</div>
      </div>
      {mission.suggestion.applied_mission_id ? (
        <a className="btn ghost" href={`#/missions/${mission.suggestion.applied_mission_id}`}>Applied · open the retry →</a>
      ) : (
        <button className="btn primary" onClick={p.onApply} disabled={busy !== ''}>{busy === 'apply' ? 'Amending…' : `${capitalize(describeSuggestion(mission.suggestion, currency))} & retry`}</button>
      )}
    </div>
  )
  // Without a suggestion the agent's explanation is the content; with one, it is the reason behind it.
  const explanation = mission.summary && (
    mission.suggestion ? (
      <details className="why-stopped">
        <summary>Why the agent stopped</summary>
        <div className="summary md"><Markdown text={mission.summary} /></div>
      </details>
    ) : (
      <div className="summary md"><Markdown text={mission.summary} /></div>
    )
  )

  if (state === 'failed') {
    return (
      <Card tone="decision" state="neutral">
        {head('The agent failed')}
        <div className="alert bad" style={{ marginTop: 0 }}>{mission.error}</div>
        {latest && <><div className="divider" />{proposalBody()}</>}
      </Card>
    )
  }
  if (state === 'no_proposal') {
    // An auto-approved cart whose order could not be created can still be paid from here.
    const payable = latest && PAYABLE.includes(latest.status)
    return (
      <Card tone="decision" state={payable ? 'warn' : 'neutral'}>
        {head('No proposal')}
        {!mission.summary && !mission.suggestion && <p className="muted" style={{ marginTop: 0 }}>The agent decided the goal cannot be met inside this mandate.</p>}
        {suggestionBox}
        {explanation}
        {payable && (
          <>
            <div className="divider" />
            <div className="subhead"><span>{titleFor(latest!)}</span><span className="num num-lg">{total}</span></div>
            {proposalBody()}
            {decisionRow()}
          </>
        )}
      </Card>
    )
  }
  if (state === 'negotiate' && n) {
    const noDeal = mission.status === 'no_proposal'
    const withCart = !!latest && !noDeal
    return (
      <Card tone="decision" state={tone}>
        {head(withCart ? titleFor(latest!) : 'Agent-to-agent negotiation', negotiationChip(n, currency, running, withCart), withCart)}
        {withCart && proposalBody()}
        {(settled || capturedUnbooked) && receipt}
        {withCart && <div className="divider" />}
        <div className="subhead"><span>Agent-to-agent negotiation</span></div>
        <NegotiationBlock negotiation={n} currency={currency} running={running} />
        {noDeal && (
          <>
            <div className="divider" />
            {!mission.suggestion && <p className="muted" style={{ marginTop: 0 }}>{n.outcome === 'agreed' ? 'The agents agreed, but the deal does not fit this mandate.' : 'No deal could be reached under this mandate.'}</p>}
            {suggestionBox}
            {explanation}
          </>
        )}
        {/* last, so on a phone the sticky row stays in reach through the transcript and chart */}
        {withCart && decisionRow()}
      </Card>
    )
  }
  if ((state === 'paid' || state === 'proposal') && latest) {
    return (
      <Card tone="decision" state={tone}>
        {head(titleFor(latest), undefined, true)}
        {proposalBody()}
        {!(settled || capturedUnbooked) && decisionRow()}
        {receipt}
      </Card>
    )
  }
  // running (or revising, or cancelled without a proposal)
  const current = p.steps.find((s) => s.state === 'current')
  const idx = current ? p.steps.indexOf(current) + 1 : p.steps.length
  const epoch = [...p.events].reverse().find((e) => e.type === 'rejected')?.id ?? -1
  const fresh = p.events.filter((e) => e.id > epoch)
  const searches = fresh.filter((e) => e.type === 'candidates')
  const found = searches.reduce((s, e) => s + (Array.isArray(e.data?.products) ? e.data.products.length : 0), 0)
  const checks = fresh.filter((e) => e.type === 'check').length
  const moves = fresh.filter((e) => e.type === 'negotiation').length
  const tally = mission.kind === 'negotiate'
    ? moves > 0 ? `${moves} move${moves === 1 ? '' : 's'}` : ''
    : [searches.length > 0 ? `${searches.length} search${searches.length === 1 ? '' : 'es'}` : '', found > 0 ? `${found} products found` : '', checks > 0 ? `${checks} mandate check${checks === 1 ? '' : 's'}` : ''].filter(Boolean).join(' · ')
  return (
    <Card tone="decision" state={mission.status === 'cancelled' ? 'neutral' : 'live'}>
      {head(mission.status === 'cancelled' ? 'Cancelled' : revising ? 'Agent revising your cart' : 'Agent working')}
      <div className="live-line">{liveLine(p.story, currency)}</div>
      {running && (
        <div className="progress-line num">
          {current ? `Step ${idx} of ${p.steps.length} · ${current.label.toLowerCase()}` : 'Finishing up'}
          {tally ? ` · ${tally}` : ''}
        </div>
      )}
    </Card>
  )
}

const MODE: Record<string, (requires: boolean) => string> = {
  paid: (r) => (r ? 'approved by you' : 'paid automatically: under your auto-pay limit'),
  refunded: () => 'refunded',
  approved: (r) => (r ? 'approved by you · finish at PayPal' : 'auto-approved · finish at PayPal'),
  rejected: () => 'sent back by you',
  rejected_by_policy: () => 'blocked by the mandate',
  cancelled: () => 'cancelled',
}

function ChecksLine({ latest, decision, vaultFailed }: { latest: Proposal; decision: Decision; vaultFailed: boolean }) {
  const passed = decision.checks_passed ?? []
  const violations = decision.violations ?? []
  const total = passed.length + violations.length
  const requires = decision.requires_approval
  const mode = vaultFailed ? 'auto-approved · checkout needed' : (MODE[latest.status]?.(requires) ?? (requires ? 'needs your approval' : 'under your auto-pay limit'))
  return (
    <div className="checks">
      <div>
        {violations.length > 0 ? (
          <>
            <span className="bad-text">{violations.length} problem{violations.length === 1 ? '' : 's'} with the mandate</span>
            <Violations vs={violations} />
          </>
        ) : (
          <details>
            <summary>✓ Passed {passed.length} of {total} mandate checks</summary>
            <ul className="list">
              {passed.map((c) => <li key={c}>{CHECK_LABEL[c] ?? c}</li>)}
            </ul>
          </details>
        )}
      </div>
      <span className="mode">{mode}</span>
    </div>
  )
}

/** What to do about an open PayPal order: one primary action, the rest as quieter options. */
function PayActions({ payment, busy, onSync, mock, hint, withdraw, sticky }: { payment: Payment; busy: string; onSync: () => void; mock: boolean; hint?: string; withdraw?: () => void; sticky?: boolean }) {
  const showCheckout = !!payment.approve_url && !isCaptured(payment.status) && payment.status !== 'APPROVED'
  const syncLabel = busy === 'sync' ? 'Checking…' : 'Check status with PayPal'
  return (
    <div className="pay-actions">
      <p className="muted order-line">PayPal order <span className="mono">{payment.id}</span></p>
      <div className={`action-row${sticky ? ' is-sticky' : ''}`}>
        {showCheckout ? (
          <a className="btn paypal lg grow" href={payment.approve_url}>Continue to PayPal checkout</a>
        ) : (
          <button className="btn primary lg grow" onClick={onSync} disabled={busy !== ''}>{syncLabel}</button>
        )}
        {withdraw && <button className="btn danger lg" onClick={withdraw} disabled={busy !== ''}>Withdraw & send feedback</button>}
      </div>
      <p className="muted hint-line">
        {showCheckout && (
          <>
            Already paid at PayPal? <button type="button" className="link" onClick={onSync} disabled={busy !== ''}>{busy === 'sync' ? 'Checking…' : 'Check status'}</button>
            {' · '}
          </>
        )}
        {payment.status === 'PENDING'
          ? 'The capture is pending at PayPal; it is booked automatically once it completes.'
          : isCaptured(payment.status) ? 'PayPal reports the payment; checking the status books it in the ledger.'
          : payment.status === 'APPROVED' ? 'You approved at PayPal; checking the status captures the payment.'
          : mock ? 'The checkout is simulated: no real PayPal call is made.' : hint ? `Sandbox buyer for testing: ${hint}` : 'Log in with a sandbox buyer account.'}
      </p>
    </div>
  )
}
