import { useEffect, useMemo, useRef, useState } from 'react'
import type { ColDef, ColGroupDef, GridApi } from 'ag-grid-community'
import { AgCharts } from 'ag-charts-react'
import type { AgCartesianChartOptions, AgPolarChartOptions } from 'ag-charts-community'
import { api, isFinalPayment, label, money, statusCellClass, type InsightItem, type Insights } from '../api'
import { Grid, NUM, useChartTokens, useMediaQuery } from '../components/Grid'
import { Card, Empty, Statline, navigate } from '../components/ui'
import { shortStamp } from '../format'

const CategoryCell = (p: { value?: string }) => (p.value ? <span className="cell-cat">{p.value}</span> : null)

/** Phone: product with merchant and status underneath. */
function ItemCell(p: { data?: InsightItem }) {
  if (!p.data) return null
  return (
    <div className="cart-item">
      <div className="cart-title clamp2">{p.data.product}</div>
      <div className="cart-reason">{p.data.merchant} · <span className={statusCellClass(p.data.status)}>{label(p.data.status)}</span></div>
    </div>
  )
}

/** The last seven days up to the newest one with spend, so a single day sits in context. */
function lastWeek(days: { key: string; amount: number; count: number }[]) {
  const end = days.reduce((m, d) => (d.key > m ? d.key : m), new Date().toISOString().slice(0, 10))
  const byKey = new Map(days.map((d) => [d.key, d]))
  const out: { key: string; label: string; amount: number; count: number }[] = []
  const t = new Date(end + 'T00:00:00Z').getTime()
  for (let i = 6; i >= 0; i--) {
    const key = new Date(t - i * 86400000).toISOString().slice(0, 10)
    const d = byKey.get(key)
    out.push({ key, label: new Date(key + 'T00:00:00Z').toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' }), amount: d?.amount ?? 0, count: d?.count ?? 0 })
  }
  return out
}

/** Spending insights: AG Charts over the ledger, plus an AG Grid of every purchased line item. */
export default function InsightsPage() {
  const [data, setData] = useState<Insights | null>(null)
  const [err, setErr] = useState('')
  const [quick, setQuick] = useState('')
  const [showOpen, setShowOpen] = useState(false)
  const gridApi = useRef<GridApi<InsightItem> | null>(null)
  const tk = useChartTokens()
  const isDark = tk.isDark
  const phone = useMediaQuery('(max-width: 720px)')
  const PALETTE = [tk.accent, tk.live, tk.warn, ...tk.extra]
  useEffect(() => {
    api.insights().then(setData).catch((e) => setErr(e.message))
  }, [])

  const cur = data?.currency || 'USD'
  const base = useMemo<Pick<AgCartesianChartOptions, 'theme' | 'background' | 'padding' | 'height'>>(
    () => ({
      theme: isDark ? 'ag-default-dark' : 'ag-default',
      background: { fill: 'transparent' },
      padding: { top: 8, right: 12, bottom: 4, left: 8 },
      height: 280,
    }),
    [isDark],
  )
  const fmt = (v: number) => money(v, cur)

  const byCategory = useMemo<AgCartesianChartOptions>(
    () => ({
      ...base,
      data: data?.by_category ?? [],
      series: [{ type: 'bar', direction: 'horizontal', xKey: 'key', yKey: 'amount', yName: 'Captured', fill: tk.accent, cornerRadius: 4,
        tooltip: { renderer: (p: any) => ({ title: p.datum.key, content: `${fmt(p.datum.amount)} across ${p.datum.count} item${p.datum.count === 1 ? '' : 's'}` }) } }],
      axes: { x: { type: 'number', label: { formatter: (p: any) => fmt(p.value) } }, y: { type: 'category' } },
    }),
    [base, data, cur, tk],
  )
  const byMerchant = useMemo<AgPolarChartOptions>(
    () => ({
      ...base,
      data: data?.by_merchant ?? [],
      series: [{ type: 'donut', angleKey: 'amount', calloutLabelKey: 'key', innerRadiusRatio: 0.62, fills: PALETTE, strokeWidth: 0,
        calloutLabel: { formatter: (p: any) => `${p.datum.key} · ${fmt(p.datum.amount)}` },
        tooltip: { renderer: (p: any) => ({ title: p.datum.key, content: `${fmt(p.datum.amount)} · ${p.datum.count} item${p.datum.count === 1 ? '' : 's'}` }) } }],
      legend: { position: 'bottom' },
    }),
    [base, data, cur, tk],
  )
  const byDay = useMemo<AgCartesianChartOptions>(
    () => ({
      ...base,
      data: lastWeek(data?.by_day ?? []),
      series: [{ type: 'bar', xKey: 'label', yKey: 'amount', yName: 'Captured per day', fill: tk.accent, cornerRadius: 4,
        tooltip: { renderer: (p: any) => ({ title: p.datum.key, content: `${fmt(p.datum.amount)} · ${p.datum.count} order${p.datum.count === 1 ? '' : 's'}` }) } }],
      axes: { x: { type: 'category', paddingInner: 0.35 }, y: { type: 'number', label: { formatter: (p: any) => fmt(p.value) } } },
    }),
    [base, data, cur, tk],
  )
  const budgets = useMemo<AgCartesianChartOptions>(
    () => ({
      ...base,
      data: data?.mandates ?? [],
      series: [
        { type: 'bar', direction: 'horizontal', xKey: 'name', yKey: 'spent', yName: 'Spent', stacked: true, fill: tk.accent },
        { type: 'bar', direction: 'horizontal', xKey: 'name', yKey: 'reserved', yName: 'Reserved', stacked: true, fill: tk.warn },
        // Remaining reads as an empty track, like the budget bars on the dashboard.
        { type: 'bar', direction: 'horizontal', xKey: 'name', yKey: 'remaining', yName: 'Remaining', stacked: true, fill: tk.track, stroke: tk.line, strokeWidth: 1 },
      ],
      axes: { x: { type: 'number', label: { formatter: (p: any) => fmt(p.value) } }, y: { type: 'category' } },
      legend: { position: 'bottom' },
    }),
    [base, data, cur, tk],
  )

  const cols = useMemo<(ColDef<InsightItem> | ColGroupDef<InsightItem>)[]>(
    () => [
      {
        headerName: 'Item',
        children: [
          { field: 'product', headerName: 'Product', minWidth: phone ? 150 : 200, flex: 2, tooltipField: 'product', filter: 'agTextColumnFilter', floatingFilter: !phone, cellRenderer: phone ? ItemCell : undefined, autoHeight: phone, wrapText: phone },
          { field: 'category', headerName: 'Category', minWidth: 115, cellRenderer: CategoryCell, filter: 'agTextColumnFilter', floatingFilter: true, hide: phone },
          { field: 'merchant', headerName: 'Merchant', minWidth: 120, filter: 'agTextColumnFilter', floatingFilter: true, hide: phone },
        ],
      },
      {
        headerName: 'Money',
        children: [
          { field: 'unit_price', headerName: 'Unit', type: 'rightAligned', valueFormatter: (p) => money(p.value, p.data?.currency), filter: 'agNumberColumnFilter', minWidth: 100, maxWidth: 120, cellClass: NUM, hide: phone },
          { field: 'quantity', headerName: 'Qty', type: 'rightAligned', minWidth: 70, maxWidth: 80, filter: 'agNumberColumnFilter', cellClass: NUM, hide: phone },
          { field: 'line_total', headerName: phone ? 'Total' : 'Line total', type: 'rightAligned', valueFormatter: (p) => money(p.value, p.data?.currency), filter: 'agNumberColumnFilter', minWidth: phone ? 92 : 130, maxWidth: phone ? 110 : undefined, cellClass: NUM },
        ],
      },
      {
        headerName: 'Order',
        children: [
          { field: 'status', headerName: 'Status', valueFormatter: (p) => label(p.value), cellClass: (p) => statusCellClass(p.value), minWidth: 165, filter: 'agTextColumnFilter', floatingFilter: true, hide: phone },
          { field: 'paid_via', headerName: 'Paid via', minWidth: 145, cellClass: (p) => (p.value === 'saved PayPal' ? 'cell-via-saved' : ''), hide: phone },
          { field: 'mandate', headerName: 'Mandate', minWidth: 120, filter: 'agTextColumnFilter', floatingFilter: true, hide: phone },
          // In the CSV export and the Ledger; hidden here so the grid fits without sideways scrolling.
          { field: 'goal', headerName: 'Mission goal', minWidth: 220, flex: 2, tooltipField: 'goal', hide: true },
          { field: 'order_id', headerName: 'PayPal order', width: 160, flex: 0, tooltipField: 'order_id', hide: true },
          { field: 'capture_id', headerName: 'Capture', width: 150, flex: 0, tooltipField: 'capture_id', hide: true },
          { field: 'created_at', headerName: 'When', valueFormatter: (p) => shortStamp(p.value), minWidth: 150, sort: 'desc', hide: true },
        ],
      },
    ],
    [phone],
  )

  if (err) return <div className="page"><div className="alert bad">{err}</div></div>
  if (!data) return <div className="page"><Empty>Loading…</Empty></div>
  const all = data.items ?? []
  // Only captured payments count as purchases; open orders can be shown on request.
  const openItems = all.filter((it) => !isFinalPayment(it.status) || it.status === 'PENDING')
  const items = showOpen ? all : all.filter((it) => !openItems.includes(it))
  const t = data.totals
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Spending insights</h1>
          <p className="lede">What the agent bought, by mandate, category, merchant and day. Only captured PayPal payments count.</p>
        </div>
      </div>
      <Statline
        items={[
          { value: money(t.captured, cur), label: 'captured through PayPal' },
          { value: t.orders, label: 'completed orders' },
          { value: t.items, label: 'items bought' },
          { value: money(t.average_order, cur), label: 'average order' },
          { value: t.mandates, label: 'mandates' },
        ]}
      />

      {t.orders === 0 ? (
        <Empty>No captured payments yet. Approve a proposal and complete PayPal checkout; the charts fill in from the ledger.</Empty>
      ) : (
        <div className="charts">
          <div className="card chart-card"><h2 className="card-title">Budget use per mandate</h2><div className="sub">spent, reserved and still available</div><div className="chart" role="img" aria-label={`Budget use: ${(data.mandates ?? []).map((m) => `${m.name} ${fmt(m.spent)} spent of ${fmt(m.budget)}`).join('; ')}`}><AgCharts options={budgets} /></div></div>
          <div className="card chart-card"><h2 className="card-title">Spend by category</h2><div className="sub">captured line items</div><div className="chart" role="img" aria-label={`Spend by category: ${(data.by_category ?? []).map((b) => `${b.key} ${fmt(b.amount)}`).join('; ')}`}><AgCharts options={byCategory} /></div></div>
          <div className="card chart-card"><h2 className="card-title">Spend by merchant</h2><div className="sub">where the money went</div><div className="chart" role="img" aria-label={`Spend by merchant: ${(data.by_merchant ?? []).map((b) => `${b.key} ${fmt(b.amount)}`).join('; ')}`}><AgCharts options={byMerchant} /></div></div>
          <div className="card chart-card"><h2 className="card-title">Spend per day</h2><div className="sub">the last seven days, by date (UTC)</div><div className="chart" role="img" aria-label={`Spend per day: ${(data.by_day ?? []).map((b) => `${b.key} ${fmt(b.amount)}`).join('; ')}`}><AgCharts options={byDay} /></div></div>
        </div>
      )}

      <Card
        title="Purchased items"
        action={
          all.length > 0 ? (
            <div className="toolbar">
              {openItems.length > 0 && (
                <label className="check" style={{ fontSize: 13 }}>
                  <input type="checkbox" checked={showOpen} onChange={(e) => setShowOpen(e.target.checked)} /> Include open orders ({openItems.length})
                </label>
              )}
              <input value={quick} onChange={(e) => { setQuick(e.target.value); gridApi.current?.setGridOption('quickFilterText', e.target.value) }} placeholder="Quick filter: product, merchant, mandate…" aria-label="Quick filter for purchased items" />
              <button className="btn ghost" onClick={() => gridApi.current?.exportDataAsCsv({ fileName: 'mandate-items.csv', allColumns: true })}>Export CSV</button>
            </div>
          ) : undefined
        }
      >
        {items.length === 0 ? (
          <Empty>{all.length === 0 ? 'No orders yet.' : 'No captured purchases yet. Tick "Include open orders" to see orders still waiting at PayPal.'}</Empty>
        ) : (
          <Grid<InsightItem>
            rowData={items}
            columnDefs={cols}
            getRowId={(p) => p.data.order_id + ':' + p.data.product_id}
            onGridReady={(e) => { gridApi.current = e.api }}
            onRowOpen={(r) => navigate(`#/missions/${r.mission_id}`)}
            pagination={items.length > 25}
            paginationPageSize={25}
            enableCellTextSelection
            tooltipShowDelay={300}
          />
        )}
      </Card>
    </div>
  )
}
