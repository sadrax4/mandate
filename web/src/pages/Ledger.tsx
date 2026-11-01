import { useEffect, useMemo, useRef, useState } from 'react'
import type { ColDef, GridApi } from 'ag-grid-community'
import { api, isFinalPayment, label, money, statusCellClass, type AskResult, type LedgerRow } from '../api'
import { useConfig } from '../config'
import { Grid, MONEY, NUM, useMediaQuery } from '../components/Grid'
import { Markdown } from '../components/Markdown'
import { Card, Chip, Empty, Statline, SuggestionChip, navigate } from '../components/ui'
import { shortStamp } from '../format'

/** Phone: one cell tells the order (goal, when, status); the amount sits beside it. */
function OrderCell(p: { data?: LedgerRow }) {
  if (!p.data) return null
  return (
    <div className="cart-item">
      <div className="cart-title clamp2">{p.data.goal}</div>
      <div className="cart-reason">{shortStamp(p.data.created_at)}</div>
      <div className={`cart-reason ${statusCellClass(p.data.status)}`}>{label(p.data.status)}</div>
    </div>
  )
}

const ASK_EXAMPLES = [
  'Did my most recent order get captured? Confirm it with PayPal.',
  'How much have I spent this week, and on what?',
  'Are there any refunds, reversals or disputes on my orders?',
  'Which orders were paid with the saved PayPal account instead of checkout?',
]

export default function Ledger() {
  const [rows, setRows] = useState<LedgerRow[]>([])
  const [err, setErr] = useState('')
  const config = useConfig()
  const [question, setQuestion] = useState('')
  const [asking, setAsking] = useState(false)
  const [askErr, setAskErr] = useState('')
  const [answer, setAnswer] = useState<AskResult | null>(null)
  const gridApi = useRef<GridApi<LedgerRow> | null>(null)
  useEffect(() => {
    api.ledger().then(setRows).catch((e) => setErr(e.message))
  }, [])

  const ask = async (e?: { preventDefault: () => void }) => {
    e?.preventDefault()
    const q = question.trim()
    if (!q) return
    setAsking(true)
    setAskErr('')
    setAnswer(null)
    try {
      setAnswer(await api.ask(q))
    } catch (e: any) {
      setAskErr(e.message)
    } finally {
      setAsking(false)
    }
  }
  const mcp = config?.paypal_mcp

  const totals = useMemo(() => {
    const completed = rows.filter((r) => r.status === 'COMPLETED' || r.status === 'PARTIALLY_REFUNDED')
    // open: PayPal has not taken the money yet (waiting for the buyer, a retry, or a pending capture)
    const open = rows.filter((r) => !isFinalPayment(r.status))
    return {
      count: rows.length,
      completed: completed.length,
      paid: completed.reduce((s, r) => s + r.amount - (r.refunded ?? 0), 0),
      open: open.reduce((s, r) => s + r.amount, 0),
      openCount: open.length,
    }
  }, [rows])

  const [details, setDetails] = useState(false) // capture, payer and environment columns
  const phone = useMediaQuery('(max-width: 720px)')
  const EXTRA = ['capture_id', 'payer_email', 'mode']
  useEffect(() => {
    gridApi.current?.setColumnsVisible(EXTRA, details && !phone)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [details, phone])
  const cols = useMemo<ColDef<LedgerRow>[]>(
    () => [
      { colId: 'order', headerName: 'Order', field: 'goal', cellRenderer: OrderCell, autoHeight: true, wrapText: true, minWidth: 150, flex: 1, hide: !phone },
      { field: 'created_at', headerName: 'When', valueFormatter: (p) => shortStamp(p.value), tooltipValueGetter: (p) => new Date(p.value).toLocaleString(), width: 150, flex: 0, sort: 'desc', hide: phone },
      { field: 'mandate_name', headerName: 'Mandate', minWidth: 120, hide: phone },
      { field: 'goal', headerName: 'Mission goal', minWidth: 200, flex: 2, tooltipField: 'goal', wrapText: true, autoHeight: true, cellClass: 'cell-wrap2', hide: phone },
      { field: 'items', headerName: 'Items', minWidth: 76, maxWidth: 84, filter: 'agNumberColumnFilter', suppressHeaderFilterButton: true, cellClass: NUM, hide: phone },
      { field: 'amount', headerName: 'Amount', valueFormatter: (p) => money(p.value, p.data?.currency), filter: 'agNumberColumnFilter', ...MONEY, minWidth: phone ? 92 : 120, maxWidth: phone ? 110 : undefined },
      { field: 'status', headerName: 'Status', valueFormatter: (p) => label(p.value), cellClass: (p) => statusCellClass(p.value), minWidth: 170, hide: phone },
      // A value getter (not a formatter) so AG Grid never shows the boolean as a checkbox, and filters and CSV read words.
      { colId: 'vaulted', headerName: 'Paid via', cellDataType: 'text', valueGetter: (p) => (p.data?.vaulted ? 'saved PayPal' : 'PayPal checkout'), cellClass: (p) => (p.value === 'saved PayPal' ? 'cell-via-saved' : ''), minWidth: 145, hide: phone },
      { field: 'id', headerName: 'PayPal order', width: 160, flex: 0, tooltipField: 'id', hide: phone },
      { field: 'capture_id', headerName: 'Capture', width: 160, flex: 0, tooltipField: 'capture_id', hide: true },
      { field: 'payer_email', headerName: 'Payer', width: 170, flex: 0, tooltipField: 'payer_email', hide: true },
      { field: 'mode', headerName: 'Mode', width: 90, flex: 0, hide: true },
    ],
    [phone],
  )

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Ledger</h1>
          <p className="lede">Every PayPal order the agent's proposals turned into.</p>
        </div>
      </div>
      <Statline
        items={[
          { value: totals.count, label: 'orders' },
          { value: totals.completed, label: 'completed' },
          { value: money(totals.paid, config?.currency), label: 'captured' },
          { value: money(totals.open, config?.currency), label: totals.openCount === 1 ? 'open order, not yet captured' : 'open orders, not yet captured' },
        ]}
      />
      {err && <div className="alert bad" role="alert">{err}</div>}
      <Card
        title="Orders"
        action={
          rows.length > 0 ? (
            <div className="toolbar">
              <label className="check hide-phone" style={{ fontSize: 13 }}>
                <input type="checkbox" checked={details} onChange={(e) => setDetails(e.target.checked)} /> More columns
              </label>
              <button
                className="btn ghost"
                onClick={() => {
                  // Every column, hidden ones included, except the phone-only summary column.
                  const g = gridApi.current
                  g?.exportDataAsCsv({ fileName: 'mandate-ledger.csv', columnKeys: g.getColumns()?.filter((c) => c.getColId() !== 'order') })
                }}
              >
                Export CSV
              </button>
            </div>
          ) : undefined
        }
      >
        {rows.length === 0 ? (
          <Empty>No payments yet. Approve a proposal and complete PayPal checkout to see it here.</Empty>
        ) : (
          <Grid<LedgerRow>
            rowData={rows}
            columnDefs={cols}
            getRowId={(p) => p.data.id}
            onGridReady={(e) => {
              gridApi.current = e.api
              e.api.setColumnsVisible(EXTRA, details && !phone)
            }}
            onRowOpen={(r) => navigate(`#/missions/${r.mission_id}`)}
            pagination={rows.length > 25}
            paginationPageSize={25}
          />
        )}
        {rows.length > 0 && <p className="muted" style={{ marginBottom: 0, fontSize: 12.5 }}>Click a row, or press Enter on it, to open its mission. The CSV export always includes every column.</p>}
      </Card>

      {mcp?.enabled && (
        <Card title="Ask about your orders" className="mt" action={<span className="muted">PayPal MCP server · {mcp.tools.length} read-only tools</span>}>
          <p className="muted secondary" style={{ marginTop: 0 }}>Reads this ledger and PayPal itself through PayPal's MCP server. It can look, never pay.</p>
          <form className="ask-form" onSubmit={ask}>
            <input value={question} onChange={(e) => setQuestion(e.target.value)} placeholder="e.g. Did my last order get captured? What did I spend this week?" maxLength={600} aria-label="Question for the order assistant" />
            <button className="btn primary" disabled={asking || !question.trim()}>{asking ? 'Asking PayPal…' : 'Ask'}</button>
          </form>
          <div className="hero-try">
            <span className="try-label">Try:</span>
            {ASK_EXAMPLES.map((q) => <SuggestionChip key={q} text={q} onClick={() => setQuestion(q)} />)}
          </div>
          {askErr && <div className="alert bad" role="alert">{askErr}</div>}
          {/* stays mounted, so the answer is read out when it arrives */}
          <div aria-live="polite" aria-busy={asking}>
          {asking && <p className="sr-only">Asking PayPal…</p>}
          {answer && (
            <div className="mt">
              <div className="summary md"><Markdown text={answer.answer} /></div>
              {(answer.tools_used ?? []).length > 0 && (
                <div className="chips">
                  {(answer.tools_used ?? []).map((t) => <Chip key={t} tone="good">PayPal MCP · {t}</Chip>)}
                </div>
              )}
              {(answer.events ?? []).filter((ev) => ev.type === 'mcp_tool_call' || ev.type === 'mcp_tool_result').map((ev, i) => (
                <div key={i} className="muted" style={{ fontSize: 12.5 }}>
                  {ev.type === 'mcp_tool_call' ? '⚙ ' : '↩ '}{ev.message}{ev.data?.preview ? `: ${String(ev.data.preview).slice(0, 200)}` : ''}
                </div>
              ))}
              {answer.warning && <div className="alert warn">{answer.warning}</div>}
            </div>
          )}
          </div>
        </Card>
      )}
    </div>
  )
}
