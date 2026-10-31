import { useEffect, useMemo, useRef, useState } from 'react'
import type { ColDef, ColGroupDef, GridApi } from 'ag-grid-community'
import { AgCharts } from 'ag-charts-react'
import type { AgCartesianChartOptions, AgPolarChartOptions } from 'ag-charts-community'
import { api, label, money, statusCellClass, type InsightItem, type Insights } from '../api'
import { Grid, NUM, usePrefersDark } from '../components/Grid'
import { Card, Empty, Statline, navigate } from '../components/ui'

const CategoryCell = (p: { value?: string }) => (p.value ? <span className="cell-cat">{p.value}</span> : null)

const PALETTE = ['#0d9488', '#2563eb', '#d97706', '#7c3aed', '#db2777', '#059669', '#64748b', '#ea580c']

/** Spending insights: AG Charts over the ledger, plus an AG Grid of every purchased line item. */
export default function InsightsPage() {
  const [data, setData] = useState<Insights | null>(null)
  const [err, setErr] = useState('')
  const [quick, setQuick] = useState('')
  const gridApi = useRef<GridApi<InsightItem> | null>(null)
  const isDark = usePrefersDark()
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
      series: [{ type: 'bar', direction: 'horizontal', xKey: 'key', yKey: 'amount', yName: 'Captured', fill: PALETTE[0], cornerRadius: 4,
        tooltip: { renderer: (p: any) => ({ title: p.datum.key, content: `${fmt(p.datum.amount)} across ${p.datum.count} item${p.datum.count === 1 ? '' : 's'}` }) } }],
      axes: { x: { type: 'number', label: { formatter: (p: any) => fmt(p.value) } }, y: { type: 'category' } },
    }),
    [base, data, cur],
  )
  const byMerchant = useMemo<AgPolarChartOptions>(
    () => ({
      ...base,
      data: data?.by_merchant ?? [],
      series: [{ type: 'donut', angleKey: 'amount', calloutLabelKey: 'key', sectorLabelKey: 'amount', innerRadiusRatio: 0.62, fills: PALETTE, strokeWidth: 0,
        sectorLabel: { formatter: (p: any) => fmt(p.datum.amount), color: 'white' },
        tooltip: { renderer: (p: any) => ({ title: p.datum.key, content: `${fmt(p.datum.amount)} · ${p.datum.count} item${p.datum.count === 1 ? '' : 's'}` }) } }],
      legend: { position: 'bottom' },
    }),
    [base, data, cur],
  )
  const byDay = useMemo<AgCartesianChartOptions>(
    () => ({
      ...base,
      data: (data?.by_day ?? []).map((d) => ({ ...d, label: d.key.slice(5) })),
      series: [{ type: 'bar', xKey: 'label', yKey: 'amount', yName: 'Captured per day', fill: PALETTE[1], cornerRadius: 4,
        tooltip: { renderer: (p: any) => ({ title: p.datum.key, content: `${fmt(p.datum.amount)} · ${p.datum.count} order${p.datum.count === 1 ? '' : 's'}` }) } }],
      axes: { x: { type: 'category' }, y: { type: 'number', label: { formatter: (p: any) => fmt(p.value) } } },
    }),
    [base, data, cur],
  )
  const budgets = useMemo<AgCartesianChartOptions>(
    () => ({
      ...base,
      data: data?.mandates ?? [],
      series: [
        { type: 'bar', direction: 'horizontal', xKey: 'name', yKey: 'spent', yName: 'Spent', stacked: true, fill: PALETTE[0] },
        { type: 'bar', direction: 'horizontal', xKey: 'name', yKey: 'reserved', yName: 'Reserved', stacked: true, fill: PALETTE[2] },
        { type: 'bar', direction: 'horizontal', xKey: 'name', yKey: 'remaining', yName: 'Remaining', stacked: true, fill: isDark ? '#334155' : '#e2e8f0' },
      ],
      axes: { x: { type: 'number', label: { formatter: (p: any) => fmt(p.value) } }, y: { type: 'category' } },
      legend: { position: 'bottom' },
    }),
    [base, data, cur, isDark],
  )

  const cols = useMemo<(ColDef<InsightItem> | ColGroupDef<InsightItem>)[]>(
    () => [
      {
        headerName: 'Item',
        children: [
          { field: 'product', headerName: 'Product', minWidth: 240, flex: 2, tooltipField: 'product', filter: 'agTextColumnFilter', floatingFilter: true },
          { field: 'category', headerName: 'Category', minWidth: 120, cellRenderer: CategoryCell, filter: 'agTextColumnFilter', floatingFilter: true },
          { field: 'merchant', headerName: 'Merchant', minWidth: 130, filter: 'agTextColumnFilter', floatingFilter: true },
        ],
      },
      {
        headerName: 'Money',
        children: [
          { field: 'unit_price', headerName: 'Unit', type: 'rightAligned', valueFormatter: (p) => money(p.value, p.data?.currency), filter: 'agNumberColumnFilter', maxWidth: 110, cellClass: NUM },
          { field: 'quantity', headerName: 'Qty', type: 'rightAligned', maxWidth: 80, filter: 'agNumberColumnFilter', cellClass: NUM },
          { field: 'line_total', headerName: 'Line total', type: 'rightAligned', valueFormatter: (p) => money(p.value, p.data?.currency), filter: 'agNumberColumnFilter', minWidth: 125, cellClass: NUM },
        ],
      },
      {
        headerName: 'Order',
        children: [
          { field: 'status', headerName: 'Status', valueFormatter: (p) => label(p.value), cellClass: (p) => statusCellClass(p.value), minWidth: 130, filter: 'agTextColumnFilter', floatingFilter: true },
          { field: 'paid_via', headerName: 'Paid via', minWidth: 120, cellClass: (p) => (p.value === 'saved PayPal' ? 'cell-via-saved' : '') },
          { field: 'mandate', headerName: 'Mandate', minWidth: 130, filter: 'agTextColumnFilter', floatingFilter: true },
          { field: 'goal', headerName: 'Mission goal', minWidth: 220, flex: 2, tooltipField: 'goal' },
          { field: 'order_id', headerName: 'PayPal order', width: 160, flex: 0, tooltipField: 'order_id' },
          { field: 'capture_id', headerName: 'Capture', width: 150, flex: 0, tooltipField: 'capture_id', hide: true }, // in the CSV export
          { field: 'created_at', headerName: 'When', valueFormatter: (p) => new Date(p.value).toLocaleString(), minWidth: 170, sort: 'desc' },
        ],
      },
    ],
    [],
  )

  if (err) return <div className="page"><div className="alert bad">{err}</div></div>
  if (!data) return <div className="page"><Empty>Loading…</Empty></div>
  const items = data.items ?? []
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
          <div className="card chart-card"><h3>Budget use per mandate</h3><div className="sub">spent, reserved and still available</div><div className="chart"><AgCharts options={budgets} /></div></div>
          <div className="card chart-card"><h3>Spend by category</h3><div className="sub">captured line items</div><div className="chart"><AgCharts options={byCategory} /></div></div>
          <div className="card chart-card"><h3>Spend by merchant</h3><div className="sub">where the money went</div><div className="chart"><AgCharts options={byMerchant} /></div></div>
          <div className="card chart-card"><h3>Spend per day</h3><div className="sub">captured orders by date (UTC)</div><div className="chart"><AgCharts options={byDay} /></div></div>
        </div>
      )}

      <Card
        title="Purchased items"
        action={
          items.length > 0 ? (
            <div className="toolbar">
              <input value={quick} onChange={(e) => { setQuick(e.target.value); gridApi.current?.setGridOption('quickFilterText', e.target.value) }} placeholder="Quick filter: product, merchant, mandate…" aria-label="Quick filter for purchased items" />
              <button className="btn ghost" onClick={() => gridApi.current?.exportDataAsCsv({ fileName: 'mandate-items.csv', allColumns: true })}>Export CSV</button>
            </div>
          ) : undefined
        }
      >
        {items.length === 0 ? (
          <Empty>No orders yet.</Empty>
        ) : (
          <Grid<InsightItem>
            rowData={items}
            columnDefs={cols}
            getRowId={(p) => p.data.order_id + ':' + p.data.product_id}
            onGridReady={(e) => { gridApi.current = e.api }}
            onRowClicked={(e) => e.data && navigate(`#/missions/${e.data.mission_id}`)}
            rowClass="row-link"
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
