import { AgGridReact, type AgGridReactProps } from 'ag-grid-react'
import { colorSchemeDark, themeQuartz, type ColDef } from 'ag-grid-community'
import { useEffect, useState } from 'react'

// autoHeightMinBodyHeight: 0 keeps a one-row cart from reserving 150px of empty grid.
const light = themeQuartz.withParams({ accentColor: '#0d9488', borderRadius: 8, wrapperBorderRadius: 12, fontFamily: 'inherit', autoHeightMinBodyHeight: 0 })
const dark = themeQuartz
  .withPart(colorSchemeDark)
  .withParams({ accentColor: '#2dd4bf', borderRadius: 8, wrapperBorderRadius: 12, fontFamily: 'inherit', backgroundColor: '#15181d', autoHeightMinBodyHeight: 0 })

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

/** Grid is a thin wrapper around AG Grid with the Mandate theme applied. */
export function Grid<T>(props: AgGridReactProps<T> & { height?: number | string }) {
  const isDark = usePrefersDark()
  const { height, ...rest } = props
  return (
    <div className="grid-wrap" style={height ? { height } : undefined}>
      <AgGridReact<T>
        theme={isDark ? dark : light}
        defaultColDef={defaultColDef}
        domLayout={height ? 'normal' : 'autoHeight'}
        animateRows
        suppressCellFocus
        paginationPageSizeSelector={[20, 25, 50, 100]}
        {...rest}
      />
    </div>
  )
}
