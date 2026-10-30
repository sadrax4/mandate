import { useEffect, useMemo, useRef, useState } from 'react'
import type { ColDef, RowClassRules } from 'ag-grid-community'
import { api, isCaptured, isFinalPayment, label, money, type CartItem, type Event, type MissionDetail, type Payment, type Product, type Proposal } from '../api'
import { Grid } from '../components/Grid'
import { Card, Chip, Empty, navigate, Pill } from '../components/ui'
import { Markdown } from '../components/Markdown'
import { StandingOrderButtons } from './Dashboard'
import { useConfig } from '../config'
import { NegotiationCard } from '../components/Negotiation'

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
/** Product name, linking to the merchant's page when the catalog provided one (Channel3 offers do). */
function ProductCell(p: { value?: string; data?: { url?: string; source?: string } }) {
  if (!p.value) return null
  if (!p.data?.url) return <span>{p.value}</span>
  return (
    <a href={p.data.url} target="_blank" rel="noreferrer noopener" className="product-link" title="Open the merchant's product page" onClick={(e) => e.stopPropagation()}>
      {p.value} <span className="ext">↗</span>
    </a>
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

type Candidate = Product & { query: string }

export default function MissionPage({ id, params }: { id: string; params: URLSearchParams }) {
  const [detail, setDetail] = useState<MissionDetail | null>(null)
  const [events, setEvents] = useState<Event[]>([])
  // loadErr comes and goes with refreshes; actionErr (approve, reject, ...) stays until the next action.
  const [loadErr, setLoadErr] = useState('')
  const [actionErr, setActionErr] = useState('')
  const [busy, setBusy] = useState('')
  const [rejecting, setRejecting] = useState(false)
  const [reason, setReason] = useState('')
  const config = useConfig()
  const timelineRef = useRef<HTMLDivElement>(null)
  const statusRef = useRef('')

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

  // Follow the log while the agent works; a finished mission opens at the top so the story reads in order.
  useEffect(() => {
    const el = timelineRef.current
    if (el && statusRef.current === 'running') el.scrollTop = el.scrollHeight
  }, [events.length])

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
  // payments arrive newest-first; prefer the one for the latest proposal
  // Only the latest proposal's own order is shown; older orders live under "Earlier proposals".
  const payment: Payment | undefined = latest ? payments.find((p) => p.proposal_id === latest.id) : payments[0]
  const mission = detail?.mission
  const mandate = detail?.mandate

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
      { field: 'category', headerName: 'Category', minWidth: 110 },
      { field: 'price', headerName: 'Price', valueFormatter: (p) => money(p.value, p.data?.currency), filter: 'agNumberColumnFilter', type: 'rightAligned', minWidth: 100 },
      { field: 'rating', headerName: 'Rating', valueFormatter: (p) => (p.value ? Number(p.value).toFixed(1) : '—'), filter: 'agNumberColumnFilter', maxWidth: 100 },
      { field: 'reviews', headerName: 'Reviews', valueFormatter: (p) => (p.value ? Number(p.value).toLocaleString() : '—'), filter: 'agNumberColumnFilter', maxWidth: 110 },
      { field: 'query', headerName: 'Found by search', minWidth: 160 },
      { headerName: 'Outcome', valueGetter: (p) => (p.data && proposedIds.has(p.data.id) ? 'proposed' : 'considered'), maxWidth: 130, cellClass: (p) => (p.value === 'proposed' ? 'cell-status cell-paid' : 'muted') },
    ],
    [proposedIds],
  )
  const rowClassRules = useMemo<RowClassRules<Candidate>>(() => ({ 'row-proposed': (p) => !!p.data && proposedIds.has(p.data.id) }), [proposedIds])

  const itemCols = useMemo<ColDef<CartItem>[]>(
    () => [
      { field: 'title', headerName: 'Item', flex: 2, minWidth: 220, tooltipField: 'title' },
      { field: 'merchant', headerName: 'Merchant', minWidth: 100, maxWidth: 140 },
      { field: 'quantity', headerName: 'Qty', maxWidth: 64, type: 'rightAligned', filter: false },
      { headerName: 'Line', valueGetter: (p) => (p.data ? p.data.unit_price * p.data.quantity : 0), valueFormatter: (p) => money(p.value, p.data?.currency), type: 'rightAligned', maxWidth: 100, filter: false },
    ],
    [],
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
      // Paid with the PayPal account saved on the mandate: no checkout hop.
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

  const banner = params.get('payment')
  if (!detail || !mission) return <div className="page">{loadErr ? <div className="alert bad">{loadErr}</div> : <Empty>Loading mission…</Empty>}</div>
  const visibleEvents = events.filter((ev) => ev.type !== 'tool_result' || ev.data?.error)

  const unpaidOrder = latest?.status === 'approved' && payment && payment.proposal_id === latest.id && !isFinalPayment(payment.status) && !isCaptured(payment.status)
  const canDecide = latest && (latest.status === 'pending_approval' || latest.status === 'auto_approved' || unpaidOrder) && mission.status !== 'running'
  const remaining = mandate ? mandate.total_budget - mandate.spent - mandate.reserved : 0

  return (
    <div className="page">
      {banner === 'completed' && <div className="banner good">✓ Payment captured through PayPal. The mandate's spent total has been updated.</div>}
      {banner === 'cancelled' && <div className="banner warn">Checkout was cancelled at PayPal. The budget reservation was released.</div>}
      {banner === 'failed' && <div className="banner bad">PayPal capture failed. See the activity log for details.</div>}
      {banner === 'denied' && <div className="banner bad">PayPal declined the capture. The budget reservation was released; approve the cart again to pay through a new checkout.</div>}
      {banner === 'pending' && <div className="banner warn">PayPal accepted the payment but the capture is still pending (for example an eCheck or a review). The mandate books it as soon as PayPal completes it.</div>}
      <div className="mission-head">
        <div>
          <div className="mission-meta">
            <a href="#/">← Dashboard</a>
            <span>·</span>
            <span>Mission {mission.id}</span>
          </div>
          <h1>{mission.goal}</h1>
          <div className="mission-meta">
            <Pill status={mission.status} />
            {mandate && (
              <Chip>
                {mandate.name}: {money(remaining, mandate.currency)} left of {money(mandate.total_budget, mandate.currency)}
              </Chip>
            )}
            <Chip>{mission.turns} agent turn{mission.turns === 1 ? '' : 's'}</Chip>
            {mandate && mandate.max_per_item > 0 && <Chip>≤ {money(mandate.max_per_item, mandate.currency)} per item</Chip>}
          </div>
        </div>
      </div>
      {actionErr && <div className="alert bad">{actionErr}</div>}
      {loadErr && <div className="alert bad">Could not refresh this mission: {loadErr}</div>}

      <div className="two-col">
        <Card title="Agent activity" action={mission.status === 'running' ? <Pill status="running">working…</Pill> : <span className="muted">{visibleEvents.length} {visibleEvents.length === 1 ? 'event' : 'events'}</span>}>
          <div className="timeline" ref={timelineRef}>
            {visibleEvents.length === 0 && <Empty>Waiting for the agent…</Empty>}
            {visibleEvents.map((ev) => (
              <EventRow key={ev.id} ev={ev} />
            ))}
          </div>
        </Card>

        <div>
          {mission.status === 'failed' && <div className="alert bad">The agent failed: {mission.error}</div>}
          {mission.negotiation && <NegotiationCard negotiation={mission.negotiation} currency={mandate?.currency ?? 'USD'} running={mission.status === 'running'} />}
          {mission.status === 'no_proposal' && (
            <Card title="No proposal" className="mt-0">
              <p className="muted" style={{ marginTop: 0 }}>The agent decided the goal cannot be met inside this mandate.</p>
              {mission.summary && <div className="summary md"><Markdown text={mission.summary} /></div>}
              {mission.suggestion && (
                <div className="suggestion-box">
                  <div>
                    <b>💡 The agent suggests:</b> {describeSuggestion(mission.suggestion, mandate?.currency ?? 'USD')}
                    <div className="muted" style={{ marginTop: 4 }}>{mission.suggestion.reason}</div>
                    <div className="muted" style={{ marginTop: 4, fontSize: 12.5 }}>The agent cannot change its own mandate; only you can.</div>
                  </div>
                  {mission.suggestion.applied_mission_id ? (
                    <a className="btn ghost" href={`#/missions/${mission.suggestion.applied_mission_id}`}>Applied · open the retry →</a>
                  ) : (
                    <button className="btn primary" onClick={applySuggestion} disabled={busy !== ''}>{busy === 'apply' ? 'Amending…' : 'Amend mandate & retry'}</button>
                  )}
                </div>
              )}
            </Card>
          )}
          {mission.retry_of && (
            <div className="muted" style={{ marginBottom: 10 }}>Retry of <a href={`#/missions/${mission.retry_of}`}>an earlier mission</a> after you amended the mandate.</div>
          )}
          {(mission.repeat_days ?? 0) > 0 && (
            <div className="suggestion-box" style={{ marginTop: 0, marginBottom: 12 }}>
              <div>
                <b>⟳ Standing order:</b> this goal runs again every {mission.repeat_days} days, next on {mission.next_run_at ? new Date(mission.next_run_at).toLocaleDateString() : '—'}.
                <div className="muted" style={{ fontSize: 12.5, marginTop: 4 }}>Every run is a new mission checked against the mandate; with a saved PayPal account and a cart under the threshold it completes without you.</div>
              </div>
              <StandingOrderButtons mission={mission} onChanged={refresh} />
            </div>
          )}
          {mission.repeat_of && (
            <div className="muted" style={{ marginBottom: 10 }}>Run of <a href={`#/missions/${mission.repeat_of}`}>a standing order</a>.</div>
          )}

          {latest ? (
            <Card title={`Proposal ${proposals.length > 1 ? `#${proposals.length}` : ''}`} action={<Pill status={latest.status} />}>
              <Grid<CartItem> rowData={latest.cart.items} columnDefs={itemCols} getRowId={(p) => p.data.product_id} />
              {latest.cart.items.some((i) => i.reason) && (
                <ul className="reasons">
                  {latest.cart.items.map((i, idx) => (
                    <li key={`${i.product_id}-${idx}`}>
                      <b>{i.title}</b>
                      {i.reason ? <span> · {i.reason}</span> : null}
                    </li>
                  ))}
                </ul>
              )}
              <div className="totals">
                <span>
                  Total <b>{money(latest.decision.total, latest.decision.currency)}</b>
                </span>
                <span className="muted">
                  Remaining after: <b>{money(latest.decision.remaining_after, latest.decision.currency)}</b>
                </span>
                {latest.decision.requires_approval ? <Chip>human approval required</Chip> : <Chip>under auto-approve threshold</Chip>}
              </div>
              <div className="decision">
                {latest.decision.checks_passed?.map((c) => (
                  <span key={c} className="check-ok">✓ {CHECK_LABEL[c] ?? c}</span>
                ))}
                {latest.decision.violations?.map((v, i) => (
                  <span key={i} className="check-bad">⛔ {v.message}</span>
                ))}
              </div>
              {latest.summary && (
                <div className="summary md">
                  <div className="summary-label">Why this cart</div>
                  <Markdown text={latest.summary} />
                </div>
              )}
              {latest.feedback && <div className="alert warn">Your feedback: {latest.feedback}</div>}

              {canDecide && (
                <div className="actions mt" style={{ justifyContent: 'flex-start' }}>
                  {!unpaidOrder && (
                    <button className="btn paypal lg" onClick={approve} disabled={busy !== ''}>
                      {busy === 'approve'
                        ? mandate?.vault_id ? 'Paying with saved PayPal…' : 'Creating PayPal order…'
                        : mandate?.vault_id ? 'Approve & pay with saved PayPal' : latest.status === 'auto_approved' ? 'Continue to PayPal' : 'Approve & pay with PayPal'}
                    </button>
                  )}
                  {!rejecting && (
                    <button className="btn danger" onClick={() => setRejecting(true)} disabled={busy !== ''}>
                      {unpaidOrder ? 'Withdraw order & send feedback' : 'Reject with feedback'}
                    </button>
                  )}
                  {config?.demo_buyer_hint && !unpaidOrder && <span className="muted hint">Sandbox buyer for testing: {config.demo_buyer_hint}</span>}
                </div>
              )}
              {canDecide && rejecting && (
                <div className="reject-box">
                  <textarea rows={2} value={reason} onChange={(e) => setReason(e.target.value)} aria-label="Feedback for the agent" placeholder="Tell the agent what to change, e.g. 'too expensive, stay under $80' or 'I need wireless'" />
                  <div className="actions" style={{ justifyContent: 'flex-start' }}>
                    <button className="btn danger" onClick={reject} disabled={busy !== ''}>{busy === 'reject' ? 'Sending…' : 'Send back to the agent'}</button>
                    <button className="btn ghost" onClick={() => setRejecting(false)}>Cancel</button>
                  </div>
                </div>
              )}
            </Card>
          ) : (
            mission.status === 'running' && (
              <Card title="Proposal">
                <Empty>The agent is still researching. A proposal will appear here for your approval.</Empty>
              </Card>
            )
          )}

          {payment && (
            <Card title="PayPal payment" className="mt" action={<Pill status={payment.status} />}>
              <dl className="receipt">
                <dt>Order</dt>
                <dd>{payment.id}</dd>
                <dt>Amount</dt>
                <dd>{money(payment.amount, payment.currency)}</dd>
                <dt>Environment</dt>
                <dd>{payment.mode}</dd>
                {payment.capture_id && (
                  <>
                    <dt>Capture</dt>
                    <dd>{payment.capture_id}</dd>
                  </>
                )}
                {payment.payer_email && (
                  <>
                    <dt>Payer</dt>
                    <dd>{payment.payer_email}</dd>
                  </>
                )}
                <dt>Checkout</dt>
                <dd>{payment.vaulted ? 'none: paid with the PayPal account saved on this mandate' : 'PayPal checkout, approved by the buyer'}</dd>
              </dl>
              {!isFinalPayment(payment.status) && (payment.approve_url || payment.status === 'PENDING') && (
                <div className="actions mt" style={{ justifyContent: 'flex-start' }}>
                  {/* the checkout link only helps while the buyer has not approved yet, or after a declined attempt that PayPal lets them retry */}
                  {payment.approve_url && !isCaptured(payment.status) && payment.status !== 'APPROVED' && <a className="btn paypal lg" href={payment.approve_url}>Continue to PayPal checkout</a>}
                  <button className="btn ghost" onClick={sync} disabled={busy !== ''}>{busy === 'sync' ? 'Checking…' : 'Check status with PayPal'}</button>
                  <span className="muted">
                    {payment.status === 'PENDING'
                      ? 'The capture is pending at PayPal; it is booked automatically once it completes.'
                      : payment.status === 'APPROVED' ? 'The buyer approved at PayPal; check the status to capture.'
                      : payment.mode === 'mock' ? 'Simulated checkout page' : config?.demo_buyer_hint ? `Sandbox buyer for testing: ${config.demo_buyer_hint}` : 'Log in with a sandbox buyer account'}
                  </span>
                </div>
              )}
            </Card>
          )}

          {mission.summary && mission.status !== 'no_proposal' && mission.summary.trim() !== (latest?.summary ?? '').trim() && (
            <Card title="Agent's final note" className="mt">
              <div className="summary md"><Markdown text={mission.summary} /></div>
            </Card>
          )}

          {proposals.length > 1 && (
            <Card title="Earlier proposals" className="mt">
              <table className="table">
                <tbody>
                  {proposals.slice(0, -1).map((p, i) => (
                    <tr key={p.id}>
                      <td>#{i + 1}</td>
                      <td>{p.cart.items.map((it) => it.title).join(', ')}</td>
                      <td>{money(p.decision.total, p.decision.currency)}</td>
                      <td><Pill status={p.status} /></td>
                      <td className="muted">{p.feedback}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Card>
          )}
        </div>
      </div>

      {mission.kind !== 'negotiate' && (
      <Card
        title="Products the agent considered"
        className="mt"
        action={
          <span className="muted">
            {candidates.length} {candidates.length === 1 ? 'product' : 'products'}{candidates[0]?.source === 'channel3' ? ' · live retail data via Channel3 (links open the merchant page)' : candidates[0]?.source === 'mock' ? ' · built-in demo catalog' : ''} · highlighted rows are in the proposed cart
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

function mergeEvents(prev: Event[], incoming: Event[]) {
  if (incoming.length === 0) return prev
  const seen = new Set(prev.map((e) => e.id))
  const add = incoming.filter((e) => !seen.has(e.id))
  if (add.length === 0) return prev
  return [...prev, ...add].sort((a, b) => a.id - b.id)
}

function EventRow({ ev }: { ev: Event }) {
  const time = new Date(ev.at).toLocaleTimeString()
  let detailsNode = null
  if (ev.type === 'tool_call' && ev.data?.input) {
    detailsNode = (
      <details>
        <summary>input</summary>
        <pre>{JSON.stringify(ev.data.input, null, 2)}</pre>
      </details>
    )
  } else if (ev.type === 'check' && ev.data?.decision) {
    const d = ev.data.decision
    detailsNode = (
      <div className="decision">
        {d.checks_passed?.map((c: string) => <span key={c} className="check-ok">✓ {CHECK_LABEL[c] ?? c}</span>)}
        {d.violations?.map((v: any, i: number) => <span key={i} className="check-bad">⛔ {v.message}</span>)}
      </div>
    )
  } else if (ev.type === 'candidates' && ev.data?.products) {
    detailsNode = (
      <details>
        <summary>{ev.data.products.length} products</summary>
        <pre>{(ev.data.products as Product[]).map((p) => `${money(p.price, p.currency).padStart(10)}  ${p.rating ?? '-'}★  ${p.title} (${p.merchant})`).join('\n')}</pre>
      </details>
    )
  }
  const msg = ev.type === 'tool_call' ? `calls ${ev.message}` : ev.message
  return (
    <div className={`ev ev-${ev.type}`}>
      <div className="ico">{ICON[ev.type] ?? '•'}</div>
      <div className="body">
        <div className={ev.type === 'assistant' ? 'msg md' : 'msg'}>{ev.type === 'assistant' ? <Markdown text={msg} /> : msg}</div>
        {detailsNode}
        <div className="meta">{label(ev.type)} · {time}</div>
      </div>
    </div>
  )
}
