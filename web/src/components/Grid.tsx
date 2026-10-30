import { AgGridReact, type AgGridReactProps } from 'ag-grid-react'
import { colorSchemeDark, themeQuartz, type ColDef } from 'ag-grid-community'
import { useEffect, useState } from 'react'

const base = { borderRadius: 8, wrapperBorderRadius: 12, fontFamily: 'inherit' }
const light = themeQuartz.withParams({ ...base, accentColor: '#0d9488' })
const dark = themeQuartz.withPart(colorSchemeDark).withParams({ ...base, accentColor: '#2dd4bf', backgroundColor: '#15181d' })
// A grid with a handful of rows (a cart) must not reserve 150px of empty body;
// it also gets no filters, so it can never be filtered down to the empty overlay.
const lightCompact = light.withParams({ autoHeightMinBodyHeight: 0 })
const darkCompact = dark.withParams({ autoHeightMinBodyHeight: 0 })

export function usePrefersDark() {
  const [isDark, setDark] = useState(() => window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false)
  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const fn = (e: MediaQueryListEvent) => setDark(e.matches)
    mq.addEventListener('change', fn)
    return () => mq.removeEventListener('change', fn)
  }, [])
  return isDark
}

export const defaultColDef: ColDef = { sortable: true, filter: true, resizable: true, flex: 1, minWidth: 90 }
const compactColDef: ColDef = { ...defaultColDef, filter: false }

/** Grid is a thin wrapper around AG Grid with the Mandate theme applied. */
export function Grid<T>(props: AgGridReactProps<T> & { height?: number | string }) {
  const isDark = usePrefersDark()
  const { height, ...rest } = props
  const compact = !height && (rest.rowData?.length ?? 0) <= 3
  return (
    <div className="grid-wrap" style={height ? { height } : undefined}>
      <AgGridReact<T>
        theme={compact ? (isDark ? darkCompact : lightCompact) : isDark ? dark : light}
        defaultColDef={compact ? compactColDef : defaultColDef}
        domLayout={height ? 'normal' : 'autoHeight'}
        animateRows
        suppressCellFocus
        paginationPageSizeSelector={[20, 25, 50, 100]}
        {...rest}
      />
    </div>
  )
}
