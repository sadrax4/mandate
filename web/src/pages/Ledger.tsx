import { useEffect, useMemo, useRef, useState } from 'react'
import type { ColDef, GridApi } from 'ag-grid-community'
import { api, isFinalPayment, label, money, statusCellClass, type AskResult, type LedgerRow } from '../api'
import { useConfig } from '../config'
import { Grid } from '../components/Grid'
import { Markdown } from '../components/Markdown'
import { Card, Chip, Empty, navigate } from '../components/ui'

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

  const [details, setDetails] = useState(false) // payer and environment columns
  useEffect(() => {
    gridApi.current?.setColumnsVisible(['payer_email', 'mode'], details)
  }, [details])
  const cols = useMemo<ColDef<LedgerRow>[]>(
    () => [
      { field: 'created_at', headerName: 'When', valueFormatter: (p) => new Date(p.value).toLocaleString(), minWidth: 170, sort: 'desc' },
      { field: 'mandate_name', headerName: 'Mandate', minWidth: 130 },
      { field: 'goal', headerName: 'Mission goal', minWidth: 260, flex: 2 },
      { field: 'items', headerName: 'Items', maxWidth: 90, filter: 'agNumberColumnFilter' },
      { field: 'amount', headerName: 'Amount', valueFormatter: (p) => money(p.value, p.data?.currency), filter: 'agNumberColumnFilter', type: 'rightAligned', minWidth: 110 },
      { field: 'status', headerName: 'Status', valueFormatter: (p) => label(p.value), cellClass: (p) => statusCellClass(p.value), minWidth: 130 },
      { field: 'vaulted', headerName: 'Paid via', valueFormatter: (p) => (p.value ? 'saved PayPal' : 'checkout'), minWidth: 110 },
      { field: 'id', headerName: 'PayPal order', width: 160, flex: 0, tooltipField: 'id' },
      { field: 'capture_id', headerName: 'Capture', width: 150, flex: 0, tooltipField: 'capture_id' },
      { field: 'payer_email', headerName: 'Payer', width: 170, flex: 0, tooltipField: 'payer_email', hide: true },
      { field: 'mode', headerName: 'Mode', width: 90, flex: 0, hide: true },
    ],
    [],
  )

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Ledger</h1>
          <p className="lede">Every PayPal order the agent's proposals turned into, across all mandates. Sort, filter and audit.</p>
        </div>
      </div>
      <div className="stats">
        <div className="stat"><b>{totals.count}</b><span>orders</span></div>
        <div className="stat"><b>{totals.completed}</b><span>completed</span></div>
        <div className="stat"><b>{money(totals.paid, config?.currency)}</b><span>captured</span></div>
        <div className="stat"><b>{money(totals.open, config?.currency)}</b><span>{totals.openCount === 1 ? 'open order' : 'open orders'}, not yet captured</span></div>
      </div>
      {err && <div className="alert bad">{err}</div>}
      <Card
        title="Orders"
        action={
          rows.length > 0 ? (
            <div className="toolbar">
              <label className="check" style={{ fontSize: 13 }}>
                <input type="checkbox" checked={details} onChange={(e) => setDetails(e.target.checked)} /> More columns
              </label>
              <button className="btn ghost" onClick={() => gridApi.current?.exportDataAsCsv({ fileName: 'mandate-ledger.csv', allColumns: true })}>
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
            }}
            onRowClicked={(e) => e.data && navigate(`#/missions/${e.data.mission_id}`)}
            rowClass="row-link"
            pagination={rows.length > 25}
            paginationPageSize={25}
          />
        )}
        {rows.length > 0 && <p className="muted" style={{ marginBottom: 0, fontSize: 12.5 }}>Click a row to open its mission. The CSV export always includes every column.</p>}
      </Card>

      {mcp?.enabled && (
        <Card title="Ask about your orders" className="mt" action={<span className="muted">PayPal MCP server · {mcp.tools.length} read-only tools</span>}>
          <p className="muted" style={{ marginTop: 0 }}>
            The assistant reads this ledger and PayPal itself through PayPal's hosted MCP server (orders, transactions, shipment tracking, refunds, disputes,
            invoices). It can look, never pay: only the tools that read data are enabled.
          </p>
          <form className="goal-form" onSubmit={ask}>
            <input value={question} onChange={(e) => setQuestion(e.target.value)} placeholder="e.g. Did my last order get captured? What did I spend this week?" maxLength={600} aria-label="Question for the order assistant" />
            <button className="btn primary" disabled={asking || !question.trim()}>{asking ? 'Asking PayPal…' : 'Ask'}</button>
          </form>
          <div className="suggestions">
            {ASK_EXAMPLES.map((q) => (
              <button key={q} type="button" className="link" onClick={() => setQuestion(q)}>{q}</button>
            ))}
          </div>
          {askErr && <div className="alert bad">{askErr}</div>}
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
        </Card>
      )}
    </div>
  )
}
