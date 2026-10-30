import { useEffect, useMemo, useRef, useState } from 'react'
import type { ColDef, GridApi } from 'ag-grid-community'
import { api, label, money, type LedgerRow } from '../api'
import { Grid } from '../components/Grid'
import { Card, Empty, navigate } from '../components/ui'

export default function Ledger() {
  const [rows, setRows] = useState<LedgerRow[]>([])
  const [err, setErr] = useState('')
  const gridApi = useRef<GridApi<LedgerRow> | null>(null)
  useEffect(() => {
    api.ledger().then(setRows).catch((e) => setErr(e.message))
  }, [])

  const totals = useMemo(() => {
    const completed = rows.filter((r) => r.status === 'COMPLETED')
    return {
      count: rows.length,
      completed: completed.length,
      paid: completed.reduce((s, r) => s + r.amount, 0),
      pending: rows.filter((r) => r.status === 'PAYER_ACTION_REQUIRED' || r.status === 'CREATED' || r.status === 'APPROVED').reduce((s, r) => s + r.amount, 0),
    }
  }, [rows])

  const cols = useMemo<ColDef<LedgerRow>[]>(
    () => [
      { field: 'created_at', headerName: 'When', valueFormatter: (p) => new Date(p.value).toLocaleString(), minWidth: 170, sort: 'desc' },
      { field: 'mandate_name', headerName: 'Mandate', minWidth: 130 },
      { field: 'goal', headerName: 'Mission goal', minWidth: 260, flex: 2 },
      { field: 'items', headerName: 'Items', maxWidth: 90, filter: 'agNumberColumnFilter' },
      { field: 'amount', headerName: 'Amount', valueFormatter: (p) => money(p.value, p.data?.currency), filter: 'agNumberColumnFilter', type: 'rightAligned', minWidth: 110 },
      { field: 'status', headerName: 'Status', valueFormatter: (p) => label(p.value), cellClass: (p) => `cell-status cell-${p.value}`, minWidth: 140 },
      { field: 'id', headerName: 'PayPal order', minWidth: 190 },
      { field: 'capture_id', headerName: 'Capture', minWidth: 170 },
      { field: 'payer_email', headerName: 'Payer', minWidth: 190 },
      { field: 'mode', headerName: 'Mode', maxWidth: 100 },
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
        <div className="stat"><b>{money(totals.paid)}</b><span>captured</span></div>
        <div className="stat"><b>{money(totals.pending)}</b><span>awaiting buyer</span></div>
      </div>
      {err && <div className="alert bad">{err}</div>}
      <Card
        title="Orders"
        action={
          rows.length > 0 ? (
            <button className="btn ghost" onClick={() => gridApi.current?.exportDataAsCsv({ fileName: 'mandate-ledger.csv' })}>
              Export CSV
            </button>
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
      </Card>
    </div>
  )
}
