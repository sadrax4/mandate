import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { ColDef, RowClassRules } from 'ag-grid-community'
import { api, isCaptured, isFinalPayment, label, money, type CartItem, type Event, type Mandate, type Mission, type MissionDetail, type Payment, type Product, type Proposal } from '../api'
import { Grid, NUM, useMediaQuery } from '../components/Grid'
import { Card, Empty, Pill, Tracker, navigate, type TrackerStep } from '../components/ui'
import { Markdown } from '../components/Markdown'
import { StandingOrderButtons } from './Dashboard'
import { useConfig } from '../config'
import { NegotiationBlock, negotiationChip } from '../components/Negotiation'
import { shortDate } from '../format'

// Every event type the server emits (named SSE events need a listener each).
const EVENT_TYPES = ['status', 'assistant', 'tool_call', 'tool_result', 'mcp_tool_call', 'mcp_tool_result', 'candidates', 'check', 'proposal', 'policy_block', 'error', 'done', 'payment', 'paid', 'rejected', 'cancelled', 'webhook', 'refunded', 'vault', 'suggestion', 'negotiation']
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
/** Only web links are followed: a retailer feed must never hand the page a javascript: URL. */
const safeHref = (u?: string) => (u && /^https?:\/\//i.test(u) ? u : undefined)

/** Product name, linking to the merchant's page when the catalog provided one (Channel3 offers do). */
function ProductCell(p: { value?: string; data?: { url?: string; source?: string } }) {
  if (!p.value) return null
  const href = safeHref(p.data?.url)
  if (!href) return <span>{p.value}</span>
  return (
    <a href={href} target="_blank" rel="noreferrer noopener" className="product-link" title="Open the merchant's product page" onClick={(e) => e.stopPropagation()}>
      {p.value} <span className="ext">↗</span>
    </a>
  )
}
/** Cart line: title with the agent's reason underneath. */
function CartItemCell(p: { data?: CartItem }) {
  if (!p.data) return null
  return (
    <div className="cart-item">
      <div className="cart-title">{p.data.title}</div>
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
  status: '•', assistant: '✎', tool_call: '⚙', tool_result: '↩', mcp_tool_call: '⇄', mcp_tool_result: '↩', candidates: '≡', check: '✓', proposal: '★', policy_block: '⛔',
  error: '!', done: '■', payment: '$', paid: '✓', rejected: '✕', cancelled: '✕', webhook: '⇄', refunded: '↺', vault: '🔒', suggestion: '💡', negotiation: '🤝',
}
const STORY_TYPES = new Set(['status', 'assistant', 'candidates', 'check', 'proposal', 'policy_block', 'error', 'done', 'payment', 'paid', 'rejected', 'cancelled', 'webhook', 'refunded', 'vault', 'suggestion', 'negotiation'])

type Candidate = Product & { query: string }

const readFlag = (key: string) => {
  try {
    return localStorage.getItem(key) === '1'
  } catch {
    return false
  }
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

  const proposals = detail?.proposals ?? []
  const latest: Proposal | undefined = proposals[proposals.length - 1]
  const payments = detail?.payments ?? []
  // Only the latest proposal's own order is shown; older orders live under "Earlier proposals".
  const payment: Payment | undefined = latest ? payments.find((p) => p.proposal_id === latest.id) : payments[0]
  const mission = detail?.mission
  const mandate = detail?.mandate
  const currency = mandate?.currency ?? 'USD'

  const proposedIds = useMemo(() => {
    const s = new Set<string>()
    if (latest && ['pending_approval', 'auto_approved', 'approved', 'paid'].includes(latest.status)) latest.cart.items.forEach((i) => s.add(i.product_id))
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
    return [...map.values()]
  }, [events])

  const candidateCols = useMemo<ColDef<Candidate>[]>(
    () => [
      { field: 'title', headerName: 'Product', flex: 2.4, minWidth: 240, cellRenderer: ProductCell, tooltipField: 'title' },
      { field: 'merchant', headerName: 'Merchant', minWidth: 120 },
      { field: 'category', headerName: 'Category', minWidth: 110, hide: narrow },
      { field: 'price', headerName: 'Price', valueFormatter: (p) => money(p.value, p.data?.currency), filter: 'agNumberColumnFilter', type: 'rightAligned', minWidth: 100, cellClass: NUM },
      { field: 'rating', headerName: 'Rating', valueFormatter: (p) => (p.value ? Number(p.value).toFixed(1) : '—'), filter: 'agNumberColumnFilter', suppressHeaderFilterButton: true, minWidth: 88, maxWidth: 100, cellClass: NUM },
      { field: 'reviews', headerName: 'Reviews', valueFormatter: (p) => (p.value ? Number(p.value).toLocaleString() : '—'), filter: 'agNumberColumnFilter', suppressHeaderFilterButton: true, minWidth: 100, maxWidth: 110, cellClass: NUM, hide: narrow },
      { field: 'query', headerName: 'Found by search', minWidth: 160, hide: narrow },
      {
        headerName: 'Outcome',
        valueGetter: (p) => (p.data && proposedIds.has(p.data.id) ? 'in cart' : 'considered'),
        filter: false,
        minWidth: 120,
        maxWidth: 130,
        sort: 'asc',
        comparator: (a: string, b: string) => (a === 'in cart' ? 0 : 1) - (b === 'in cart' ? 0 : 1),
        cellClass: (p) => (p.value === 'in cart' ? 'cell-status cell-paid' : 'muted'),
        headerTooltip: 'Rows in the proposed cart are highlighted',
      },
    ],
    [proposedIds, narrow],
  )
  const rowClassRules = useMemo<RowClassRules<Candidate>>(() => ({ 'row-proposed': (p) => !!p.data && proposedIds.has(p.data.id) }), [proposedIds])

  const itemCols = useMemo<ColDef<CartItem>[]>(
    () => [
      { field: 'title', headerName: 'Item', flex: 2, minWidth: phone ? 140 : 220, cellRenderer: CartItemCell, autoHeight: true, wrapText: true },
      { field: 'merchant', headerName: 'Merchant', minWidth: 100, maxWidth: 140, hide: phone },
      { field: 'quantity', headerName: 'Qty', minWidth: phone ? 48 : 56, maxWidth: phone ? 52 : 64, type: 'rightAligned', filter: false, cellClass: NUM },
      { headerName: 'Line', valueGetter: (p) => (p.data ? p.data.unit_price * p.data.quantity : 0), valueFormatter: (p) => money(p.value, p.data?.currency), type: 'rightAligned', minWidth: 96, maxWidth: 104, filter: false, cellClass: NUM },
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
  const reject = async () => {
    if (!latest) return
    setBusy('reject')
    setActionErr('')
    try {
      await api.reject(latest.id, reason.trim() || 'rejected without a reason')
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

  const banner = params.get('payment')
  if (!detail || !mission) return <div className="page mission">{loadErr ? <div className="alert bad">{loadErr}</div> : <Empty>Loading mission…</Empty>}</div>

  const running = mission.status === 'running'
  const unpaidOrder = latest?.status === 'approved' && payment && payment.proposal_id === latest.id && !isFinalPayment(payment.status) && !isCaptured(payment.status)
  const canDecide = !!latest && (latest.status === 'pending_approval' || latest.status === 'auto_approved' || !!unpaidOrder) && !running
  const remaining = mandate ? mandate.total_budget - mandate.spent - mandate.reserved : 0
  const steps = deriveSteps(events, mission, proposals)
  const story = groupStory(events, verbose)
  const storyCount = story.reduce((n, it) => n + (it.kind === 'row' ? 1 : it.events.length), 0)

  return (
    <div className="page mission">
      {banner === 'completed' && <div className="banner good">✓ Payment captured through PayPal. The mandate's spent total is updated.</div>}
      {banner === 'cancelled' && <div className="banner warn">Checkout was cancelled at PayPal. The budget reservation was released.</div>}
      {banner === 'failed' && <div className="banner bad">PayPal capture failed. See how the agent got here for details.</div>}
      {banner === 'denied' && <div className="banner bad">PayPal declined the capture. The reservation was released; approve the cart again to pay through a new checkout.</div>}
      {banner === 'pending' && <div className="banner warn">PayPal accepted the payment but the capture is still pending; the mandate books it as soon as PayPal completes it.</div>}

      <div className="mission-top">
        <div className="crumbs">
          <a href="#/">← Mandates</a>
          <button type="button" className="link mono id" title="Copy mission ID" onClick={copyId}>{copied ? 'copied' : mission.id}</button>
        </div>
        <h1 title={mission.goal}>{mission.goal}</h1>
        <div className="status-line">
          <Pill status={mission.status} />
          {mandate && <span>{mandate.name}</span>}
          {mandate && <span className="num">· {money(remaining, currency)} left of {money(mandate.total_budget, currency)}</span>}
          {mandate && mandate.max_per_item > 0 && <span className="num">· ≤ {money(mandate.max_per_item, currency)} per item</span>}
          <span className="num">· {mission.turns} agent turn{mission.turns === 1 ? '' : 's'}</span>
        </div>
        {mission.retry_of && <div className="lineage">Retry of <a href={`#/missions/${mission.retry_of}`}>an earlier mission</a> after you amended the mandate.</div>}
        {mission.repeat_of && <div className="lineage">Run of <a href={`#/missions/${mission.repeat_of}`}>a standing order</a>.</div>}
        {(mission.repeat_days ?? 0) > 0 && (
          <div className="so-strip">
            <div>
              <b>⟳ Standing order:</b> every {mission.repeat_days} days, next {shortDate(mission.next_run_at)}.{' '}
              <button type="button" className="link why-toggle" onClick={() => setWhyOpen((v) => !v)} aria-expanded={whyOpen}>why?</button>
              <span className={`why${whyOpen ? ' open' : ''}`}>Every run is a new mission checked against the mandate; with a saved PayPal account and a cart under the threshold it completes without you.</span>
            </div>
            <StandingOrderButtons mission={mission} onChanged={refresh} />
          </div>
        )}
      </div>
      {actionErr && <div className="alert bad">{actionErr}</div>}
      {loadErr && <div className="alert bad">Could not refresh this mission: {loadErr}</div>}

      <Tracker steps={steps} />

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
        running={running}
        unpaidOrder={!!unpaidOrder}
        canDecide={canDecide}
        busy={busy}
        rejecting={rejecting}
        reason={reason}
        demoBuyerHint={config?.demo_buyer_hint}
        mock={config?.paypal_mode === 'mock'}
        itemCols={itemCols}
        onApprove={approve}
        onReject={reject}
        onRejecting={setRejecting}
        onReason={setReason}
        onApply={applySuggestion}
        onSync={sync}
      />

      {proposals.length > 1 && (
        <details className="earlier">
          <summary>Earlier proposals ({proposals.length - 1})</summary>
          <div className="table-wrap">
            <table className="table">
              <tbody>
                {proposals.slice(0, -1).map((p, i) => (
                  <tr key={p.id}>
                    <td className="num">#{i + 1}</td>
                    <td>{p.cart.items.map((it) => it.title).join(', ')}</td>
                    <td className="num">{money(p.decision.total, p.decision.currency)}</td>
                    <td><Pill status={p.status} /></td>
                    <td className="muted">{p.feedback}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </details>
      )}

      <Card
        title="How the agent got here"
        className="mt"
        action={
          <span className="actions" style={{ gap: 10 }}>
            {running && <Pill status="running">working…</Pill>}
            <span className="muted num">{storyCount} event{storyCount === 1 ? '' : 's'}</span>
            <button type="button" className="link" onClick={toggleVerbose}>{verbose ? 'Show the story' : 'Show every event'}</button>
          </span>
        }
      >
        <div className="story">
          {story.length === 0 && <Empty>{running ? <span><span className="pill pill-live"><span className="dot" /> working…</span> Waiting for the agent…</span> : 'No events.'}</Empty>}
          {story.map((it) =>
            it.kind === 'row' ? (
              <EventRow key={it.ev.id} ev={it.ev} currency={currency} finalNote={it.ev.type === 'done' && showFinalNote(mission, latest, events) ? mission.summary : undefined} />
            ) : (
              <details key={'g' + it.events[0].id} className="log-group">
                <summary><span className="ico">{it.mcp ? '⇄' : '⚙'}</span>{it.label}</summary>
                <div className="inner">
                  {it.events.map((ev) => <EventRow key={ev.id} ev={ev} currency={currency} raw />)}
                </div>
              </details>
            ),
          )}
        </div>
      </Card>

      {mission.kind !== 'negotiate' && (
        <Card
          title="Products compared"
          className="mt"
          action={
            <span className="muted num">
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

function deriveSteps(events: Event[], mission: Mission, proposals: Proposal[]): TrackerStep[] {
  const has = (f: (e: Event) => boolean) => events.some(f)
  const negotiate = mission.kind === 'negotiate'
  const done: Record<string, boolean> = {
    search: has((e) => e.type === 'candidates'),
    compare: has((e) => e.type === 'check' || (e.type === 'tool_call' && e.data?.tool === 'get_product')),
    negotiate: has((e) => e.type === 'negotiation'),
    check: has((e) => e.type === 'check' || e.type === 'policy_block'),
    propose: has((e) => e.type === 'proposal'),
    pay: mission.status === 'paid' || has((e) => e.type === 'paid'),
  }
  const keys = negotiate ? ['negotiate', 'check', 'propose', 'pay'] : ['search', 'compare', 'check', 'propose', 'pay']
  const labels: Record<string, string> = { search: 'Search', compare: 'Compare', negotiate: 'Negotiate', check: 'Check mandate', propose: 'Propose', pay: 'Pay' }
  const latest = proposals[proposals.length - 1]
  const revised = proposals.length > 1 && !!latest && latest.status !== 'rejected'
  const steps: TrackerStep[] = keys.map((k) => ({ key: k, label: labels[k], state: done[k] ? 'done' : 'future' }))
  for (let i = steps.length - 2; i >= 0; i--) if (steps[i + 1].state === 'done') steps[i].state = 'done'
  const firstOpen = steps.findIndex((s) => s.state !== 'done')
  const live = mission.status === 'running' || mission.status === 'awaiting_payment' || mission.status === 'awaiting_approval'
  if (live && firstOpen >= 0) {
    steps[firstOpen].state = 'current'
    if (mission.status === 'awaiting_approval' && steps[firstOpen].key === 'pay') steps[firstOpen].note = 'waiting for you'
  } else if (mission.status === 'no_proposal') {
    const p = steps.find((s) => s.key === 'propose')
    if (p && p.state !== 'done') Object.assign(p, { state: 'failed', label: 'No proposal' })
  } else if ((mission.status === 'failed' || mission.status === 'cancelled') && firstOpen >= 0) {
    Object.assign(steps[firstOpen], { state: 'failed', label: mission.status === 'failed' ? 'Failed' : 'Cancelled' })
  }
  if (revised) {
    const p = steps.find((s) => s.key === 'propose')
    if (p && p.state === 'done') Object.assign(p, { state: 'revised', label: 'Revised', note: 'after your feedback' })
  }
  return steps
}

// --- the story log ---

type StoryItem = { kind: 'row'; ev: Event } | { kind: 'group'; events: Event[]; label: string; mcp: boolean }

function isStory(ev: Event, verbose: boolean) {
  if (verbose) return true
  if (ev.type === 'tool_result' || ev.type === 'mcp_tool_result') return !!ev.data?.error
  if (ev.type === 'status' && ev.message === 'agent started') return false
  return STORY_TYPES.has(ev.type)
}

function groupStory(events: Event[], verbose: boolean): StoryItem[] {
  const out: StoryItem[] = []
  let run: Event[] = []
  const flush = () => {
    if (run.length === 0) return
    const calls = run.filter((e) => e.type === 'tool_call' || e.type === 'mcp_tool_call')
    if (calls.length === 0) {
      run = []
      return
    }
    const mcp = calls.every((e) => e.type === 'mcp_tool_call')
    const counts = new Map<string, number>()
    for (const e of calls) {
      const name = e.type === 'mcp_tool_call' ? e.message : (e.data?.tool ?? e.message)
      counts.set(name, (counts.get(name) ?? 0) + 1)
    }
    const names = [...counts.entries()].map(([n, c]) => (c > 1 ? `${n} ×${c}` : n)).join(', ')
    const n = calls.length
    out.push({ kind: 'group', events: run, mcp, label: `${n} ${mcp ? 'PayPal MCP call' : 'tool call'}${n === 1 ? '' : 's'}${names ? ' · ' + names : ''}` })
    run = []
  }
  for (const ev of events) {
    if (verbose && ev.type === 'status' && ev.message === 'agent started') {
      out.push({ kind: 'row', ev })
      continue
    }
    if (isStory(ev, verbose)) {
      flush()
      out.push({ kind: 'row', ev })
    } else if (ev.type === 'status' && ev.message === 'agent started') {
      continue
    } else {
      run.push(ev)
    }
  }
  flush()
  return out
}

/** The friendly first line of a story row; the raw message stays in the row's title. */
function friendly(ev: Event, currency: string): ReactNode {
  const d = ev.data ?? {}
  switch (ev.type) {
    case 'status':
      return ev.message.startsWith('mission created:') ? 'Mission started' : ev.message
    case 'candidates': {
      const n = Array.isArray(d.products) ? d.products.length : 0
      return <>Searched “{d.query}” · <span className="num">{n}</span> product{n === 1 ? '' : 's'}</>
    }
    case 'check': {
      const v = d.decision?.violations?.length ?? 0
      const p = d.decision?.checks_passed?.length ?? 0
      return v > 0 ? `Cart checked against the mandate · ${v} violation${v === 1 ? '' : 's'}` : `Cart passes the mandate · ${p} of ${p} checks`
    }
    case 'proposal': {
      const m = /(\d+) item/.exec(ev.message)
      const items = m ? `${m[1]} item${m[1] === '1' ? '' : 's'} · ` : ''
      return `Proposed ${items}${money(d.total, currency)} · ${d.status === 'auto_approved' ? 'under the auto-approve threshold' : 'needs your approval'}`
    }
    case 'payment':
      if (ev.message.startsWith('PayPal order') && ev.message.includes('created')) return `PayPal order created · ${d.vaulted ? 'saved PayPal account, no checkout' : 'waiting for you at PayPal'}`
      return ev.message
    case 'paid':
      return `Payment captured · ${money(d.amount, currency)} · capture ${d.capture_id ?? ''}${d.vaulted ? ' · saved PayPal account' : ''}`
    case 'rejected':
      return `You rejected the proposal: ${ev.message.replace(/^human rejected the proposal: /, '')}`
    case 'vault':
      if (ev.message.startsWith('the saved PayPal account could not be used')) return 'Saved PayPal account could not be used · removed from the mandate, the next checkout saves a new one'
      return ev.message
    case 'tool_call':
      return `calls ${ev.message}`
    default:
      return ev.message
  }
}

function EventRow({ ev, currency, raw, finalNote }: { ev: Event; currency: string; raw?: boolean; finalNote?: string }) {
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
    details = (
      <div className="check-results">
        {d.checks_passed?.map((c: string) => <span key={c} className="check-ok">✓ {CHECK_LABEL[c] ?? c}</span>)}
        {d.violations?.map((v: any, i: number) => <span key={i} className="check-bad">⛔ {v.message}</span>)}
      </div>
    )
  } else if (ev.type === 'candidates' && ev.data?.products) {
    const ps = ev.data.products as Product[]
    details = (
      <details>
        <summary>{ps.length} products ▾</summary>
        <pre>{ps.map((p) => `${money(p.price, p.currency).padStart(10)}  ${p.rating ?? '-'}★  ${p.title} (${p.merchant})`).join('\n')}</pre>
      </details>
    )
  }
  const isStatus = ev.type === 'status'
  const text = raw && ev.type === 'tool_call' ? `calls ${ev.message}` : friendly(ev, currency)
  return (
    <div className={`log-row ev-${ev.type}${isStatus ? ' status' : ''}`} title={ev.message}>
      <div className="ico" title={label(ev.type)}>{ICON[ev.type] ?? '•'}</div>
      <div className="body">
        {ev.type === 'assistant' ? (
          <div className="msg md bubble agent"><Markdown text={ev.message} /></div>
        ) : (
          <div className="msg">{text}</div>
        )}
        {details}
        {finalNote && (
          <div className="summary final-note">
            <div className="summary-label">Agent's final note</div>
            <div className="md"><Markdown text={finalNote} /></div>
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
  running: boolean
  unpaidOrder: boolean
  canDecide: boolean
  busy: string
  rejecting: boolean
  reason: string
  demoBuyerHint?: string
  mock: boolean
  itemCols: ColDef<CartItem>[]
  onApprove: () => void
  onReject: () => void
  onRejecting: (v: boolean) => void
  onReason: (v: string) => void
  onApply: () => void
  onSync: () => void
}) {
  const { mission, mandate, latest, proposals, payment, currency, running, unpaidOrder, canDecide, busy } = p
  const state = mission.status === 'failed' ? 'failed' : mission.status === 'no_proposal' ? 'no_proposal' : mission.negotiation ? 'negotiate' : latest?.status === 'paid' ? 'paid' : latest ? 'proposal' : 'running'
  const n = mission.negotiation

  const total = latest ? money(latest.decision.total, latest.decision.currency) : ''
  const after = latest ? money(latest.decision.remaining_after, latest.decision.currency) : ''

  const head = (title: ReactNode, pill?: ReactNode, amount?: boolean) => (
    <div className="decision-head">
      <div className="title"><h3>{title}</h3>{pill}</div>
      {amount && latest && (
        <div className="amount">
          <span className="num num-xl">{total}</span>
          <small className="num">{after} left after</small>
        </div>
      )}
    </div>
  )

  const proposalBlock = (readOnly: boolean) =>
    latest && (
      <>
        {proposals.length > 1 && (
          <div className="muted secondary" style={{ marginBottom: 8 }}>
            Proposal #{proposals.length}{proposals[proposals.length - 2]?.feedback ? <> · after your feedback: “{proposals[proposals.length - 2].feedback}”</> : null}
          </div>
        )}
        <Grid<CartItem> rowData={latest.cart.items} columnDefs={p.itemCols} getRowId={(r) => r.data.product_id} compact />
        <ChecksLine latest={latest} />
        {latest.summary && (
          <div className="summary md">
            <div className="summary-label">Why this cart</div>
            <Markdown text={latest.summary} />
          </div>
        )}
        {latest.feedback && <div className="alert warn">Your feedback: {latest.feedback}</div>}
        {!readOnly && canDecide && !unpaidOrder && (
          <div className="action-row">
            <button className="btn paypal lg" onClick={p.onApprove} disabled={busy !== ''}>
              {busy === 'approve'
                ? mandate?.has_saved_payment ? 'Paying with saved PayPal…' : 'Creating PayPal order…'
                : mandate?.has_saved_payment ? 'Approve & pay with saved PayPal' : latest.status === 'auto_approved' ? 'Continue to PayPal' : 'Approve & pay with PayPal'}
            </button>
            {!p.rejecting && (
              <button className="btn danger lg" onClick={() => p.onRejecting(true)} disabled={busy !== ''}>Reject with feedback</button>
            )}
            {p.demoBuyerHint && <span className="hint">Sandbox buyer for testing: {p.demoBuyerHint}</span>}
          </div>
        )}
        {unpaidOrder && payment && (
          <>
            <dl className="receipt" style={{ marginTop: 12 }}>
              <dt>Order</dt><dd>{payment.id}</dd>
              <dt>Amount</dt><dd className="num">{money(payment.amount, payment.currency)}</dd>
              <dt>Environment</dt><dd>{payment.mode}</dd>
            </dl>
            <FollowUp
              payment={payment}
              busy={busy}
              onSync={p.onSync}
              mock={p.mock}
              hint={p.demoBuyerHint}
              extra={!readOnly && canDecide && !p.rejecting ? <button className="btn danger" onClick={() => p.onRejecting(true)} disabled={busy !== ''}>Withdraw order & send feedback</button> : undefined}
            />
          </>
        )}
        {!readOnly && canDecide && p.rejecting && (
          <div className="reject-box">
            <textarea rows={2} value={p.reason} onChange={(e) => p.onReason(e.target.value)} aria-label="Feedback for the agent" placeholder="Tell the agent what to change, e.g. 'too expensive, stay under $80' or 'I need wireless'" />
            <div className="actions" style={{ justifyContent: 'flex-start' }}>
              <button className="btn danger" onClick={p.onReject} disabled={busy !== ''}>{busy === 'reject' ? 'Sending…' : 'Send back to the agent'}</button>
              <button className="btn ghost" onClick={() => p.onRejecting(false)}>Cancel</button>
            </div>
          </div>
        )}
      </>
    )

  const receipt = payment && (
    <>
      <div className="divider" />
      <div className="receipt-head">
        <h4>PayPal receipt</h4>
        <span className="mono muted">{payment.mode}</span>
      </div>
      <dl className="receipt">
        <dt>Order</dt><dd>{payment.id}</dd>
        <dt>Amount</dt><dd className="num">{money(payment.amount, payment.currency)}</dd>
        {payment.capture_id && <><dt>Capture</dt><dd>{payment.capture_id}</dd></>}
        {payment.payer_email && <><dt>Payer</dt><dd>{payment.payer_email}</dd></>}
        <dt>Paid via</dt><dd>{payment.vaulted ? 'saved PayPal account, no checkout' : 'PayPal checkout, approved by you'}</dd>
      </dl>
      {!payment.accounted && (isCaptured(payment.status) || !isFinalPayment(payment.status)) && <FollowUp payment={payment} busy={busy} onSync={p.onSync} mock={p.mock} hint={p.demoBuyerHint} />}
    </>
  )

  if (state === 'failed') {
    return (
      <Card tone="decision">
        {head('The agent failed')}
        <div className="alert bad" style={{ marginTop: 0 }}>The agent failed: {mission.error}</div>
        {latest && <><div className="divider" />{proposalBlock(true)}</>}
      </Card>
    )
  }
  if (state === 'no_proposal') {
    return (
      <Card tone="decision">
        {head('No proposal')}
        <p className="muted" style={{ marginTop: 0 }}>The agent decided the goal cannot be met inside this mandate.</p>
        {mission.summary && <div className="summary md"><Markdown text={mission.summary} /></div>}
        {mission.suggestion && (
          <div className="suggestion-box">
            <div>
              <b>💡 The agent suggests:</b> {describeSuggestion(mission.suggestion, currency)}
              <div className="muted" style={{ marginTop: 4 }}>{mission.suggestion.reason}</div>
              <div className="muted" style={{ marginTop: 4, fontSize: 12.5 }}>The agent cannot change its own mandate; only you can.</div>
            </div>
            {mission.suggestion.applied_mission_id ? (
              <a className="btn ghost" href={`#/missions/${mission.suggestion.applied_mission_id}`}>Applied · open the retry →</a>
            ) : (
              <button className="btn primary" onClick={p.onApply} disabled={busy !== ''}>{busy === 'apply' ? 'Amending…' : 'Amend mandate & retry'}</button>
            )}
          </div>
        )}
      </Card>
    )
  }
  if (state === 'negotiate' && n) {
    const paid = latest?.status === 'paid'
    return (
      <Card tone="decision">
        {head('Agent-to-agent negotiation', negotiationChip(n, currency, running))}
        <NegotiationBlock negotiation={n} currency={currency} running={running} />
        {latest && (
          <>
            <div className="divider" />
            <div className="subhead">
              <span>{paid ? 'Paid through PayPal' : unpaidOrder ? 'Approved, not yet paid' : 'The agent proposes'} <Pill status={unpaidOrder && payment ? payment.status : latest.status} /></span>
              <span className="num num-lg">{total}</span>
            </div>
            <div style={{ height: 10 }} />
            {proposalBlock(paid)}
            {paid && receipt}
          </>
        )}
        {mission.status === 'cancelled' && !latest && <p className="muted" style={{ marginTop: 12 }}>Cancelled.</p>}
      </Card>
    )
  }
  if (state === 'paid' && latest) {
    return (
      <Card tone="decision">
        {head('Paid through PayPal', undefined, true)}
        {proposalBlock(true)}
        {receipt}
      </Card>
    )
  }
  if (state === 'proposal' && latest) {
    return (
      <Card tone="decision">
        {head(unpaidOrder ? 'Approved, not yet paid' : 'The agent proposes', <Pill status={unpaidOrder && payment ? payment.status : latest.status} />, true)}
        {proposalBlock(false)}
      </Card>
    )
  }
  // running (or cancelled without a proposal)
  const last = [...p.story].reverse().find((it) => it.kind === 'row') as { kind: 'row'; ev: Event } | undefined
  const current = p.steps.find((s) => s.state === 'current')
  const idx = current ? p.steps.indexOf(current) + 1 : p.steps.length
  const searches = p.events.filter((e) => e.type === 'candidates')
  const found = searches.reduce((s, e) => s + (Array.isArray(e.data?.products) ? e.data.products.length : 0), 0)
  const checks = p.events.filter((e) => e.type === 'check').length
  const rounds = p.events.filter((e) => e.type === 'negotiation').length
  const tally = mission.kind === 'negotiate'
    ? rounds > 0 ? `${rounds} round${rounds === 1 ? '' : 's'}` : ''
    : [searches.length > 0 ? `${searches.length} search${searches.length === 1 ? '' : 'es'}` : '', found > 0 ? `${found} products found` : '', checks > 0 ? `${checks} mandate check${checks === 1 ? '' : 's'}` : ''].filter(Boolean).join(' · ')
  return (
    <Card tone="decision">
      {head(mission.status === 'cancelled' ? 'Cancelled' : 'Agent working', mission.status === 'cancelled' ? <Pill status="cancelled" /> : <Pill status="running">working…</Pill>)}
      <div className="live-line">
        {!last ? <span className="muted">Waiting for the agent…</span> : last.ev.type === 'assistant' ? <Markdown text={last.ev.message} /> : friendly(last.ev, currency)}
      </div>
      {running && (
        <div className="progress-line num">
          {current ? `Step ${idx} of ${p.steps.length} · ${current.label.toLowerCase()}` : 'Finishing up'}
          {tally ? ` · ${tally}` : ''}
        </div>
      )}
    </Card>
  )
}

function ChecksLine({ latest }: { latest: Proposal }) {
  const passed = latest.decision.checks_passed ?? []
  const violations = latest.decision.violations ?? []
  const total = passed.length + violations.length
  return (
    <div className="checks">
      <div>
        {violations.length > 0 ? (
          <>
            <span className="bad" style={{ color: 'var(--bad)', fontWeight: 600 }}>⛔ {violations.length} violation{violations.length === 1 ? '' : 's'}</span>
            {violations.map((v, i) => <div key={i} className="viol">⛔ {v.message}</div>)}
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
      <span className="mode">
        {latest.status === 'paid'
          ? latest.decision.requires_approval ? 'approved by you' : 'auto-approved under the threshold'
          : latest.status === 'rejected' ? 'rejected by you'
          : latest.decision.requires_approval ? 'needs your approval' : 'under the auto-approve threshold'}
      </span>
    </div>
  )
}

function FollowUp({ payment, busy, onSync, mock, hint, extra }: { payment: Payment; busy: string; onSync: () => void; mock: boolean; hint?: string; extra?: ReactNode }) {
  const showCheckout = payment.approve_url && !isCaptured(payment.status) && payment.status !== 'APPROVED'
  return (
    <div className="follow-up">
      <div className="actions" style={{ justifyContent: 'flex-start' }}>
        {showCheckout && <a className="btn paypal lg" href={payment.approve_url}>Continue to PayPal checkout</a>}
        <button className="btn ghost" onClick={onSync} disabled={busy !== ''}>{busy === 'sync' ? 'Checking…' : 'Check status with PayPal'}</button>
        {extra}
      </div>
      <div className="muted" style={{ fontSize: 12.5, marginTop: 8 }}>
        {payment.status === 'PENDING'
          ? 'The capture is pending at PayPal; it is booked automatically once it completes.'
          : isCaptured(payment.status) ? 'PayPal reports this order completed; check the status to book it in the ledger.'
          : payment.status === 'APPROVED' ? 'The buyer approved at PayPal; check the status to capture.'
          : mock ? 'The checkout is simulated: no real PayPal call is made.' : hint ? `Sandbox buyer for testing: ${hint}` : 'Log in with a sandbox buyer account.'}
      </div>
    </div>
  )
}
