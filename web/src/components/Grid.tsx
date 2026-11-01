import { AgGridReact, type AgGridReactProps } from 'ag-grid-react'
import { colorSchemeDark, themeQuartz, type ColDef } from 'ag-grid-community'
import { useEffect, useMemo, useState } from 'react'

// A short table keeps a small body so the "no rows" overlay fits, not 150px of air.
const base = { borderRadius: 8, wrapperBorderRadius: 12, fontFamily: 'inherit', autoHeightMinBodyHeight: 72 }
const light = themeQuartz.withParams({ ...base, accentColor: '#0f766e' })
const dark = themeQuartz.withPart(colorSchemeDark).withParams({ ...base, accentColor: '#2dd4bf', backgroundColor: '#15181d' })
// A cart grid must not reserve empty body; it also gets no filters, so it can
// never be filtered down to the empty overlay, and tighter cells leave the
// item name room on a phone.
const lightCompact = light.withParams({ autoHeightMinBodyHeight: 0, cellHorizontalPadding: 10 })
const darkCompact = dark.withParams({ autoHeightMinBodyHeight: 0, cellHorizontalPadding: 10 })

/** True while the media query matches (re-renders on change). */
export function useMediaQuery(query: string) {
  const [matches, setMatches] = useState(() => window.matchMedia?.(query).matches ?? false)
  useEffect(() => {
    const mq = window.matchMedia(query)
    const fn = (e: MediaQueryListEvent) => setMatches(e.matches)
    mq.addEventListener('change', fn)
    setMatches(mq.matches)
    return () => mq.removeEventListener('change', fn)
  }, [query])
  return matches
}

/** Cell class for numbers: tabular figures. */
export const NUM = 'num'

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

/** Chart colours read from the stylesheet tokens, so charts match the page in both themes. */
export function useChartTokens() {
  const isDark = usePrefersDark()
  return useMemo(() => {
    const css = getComputedStyle(document.documentElement)
    const v = (name: string, fallback: string) => css.getPropertyValue(name).trim() || fallback
    return {
      isDark,
      accent: v('--accent', '#0f766e'),
      live: v('--live', '#1d4ed8'),
      warn: v('--warn', '#b45309'),
      bad: v('--bad', '#b91c1c'),
      muted: v('--muted', '#5f6b7a'),
      line: v('--line', '#e3e8ee'),
      track: v('--surface-2', '#f0f3f7'),
      // Categorical series after the first three: violet, pink, green, slate, orange.
      extra: isDark ? ['#c4b5fd', '#f9a8d4', '#6ee7b7', '#94a3b8', '#fdba74'] : ['#7c3aed', '#db2777', '#059669', '#64748b', '#ea580c'],
    }
  }, [isDark])
}

export const defaultColDef: ColDef = { sortable: true, filter: true, resizable: true, flex: 1, minWidth: 90 }
const compactColDef: ColDef = { ...defaultColDef, filter: false }

/**
 * Grid is a thin wrapper around AG Grid with the Mandate theme applied.
 * `compact` is for a cart of a few lines: no empty body, no filters.
 * `onRowOpen` makes rows open something: by click, or by Enter on a focused cell.
 */
export function Grid<T>(props: AgGridReactProps<T> & { height?: number | string; compact?: boolean; onRowOpen?: (row: T) => void }) {
  const isDark = usePrefersDark()
  const { height, compact = false, onRowOpen, ...rest } = props
  return (
    <div className="grid-wrap" style={height ? { height } : undefined}>
      <AgGridReact<T>
        theme={compact ? (isDark ? darkCompact : lightCompact) : isDark ? dark : light}
        defaultColDef={compact ? compactColDef : defaultColDef}
        domLayout={height ? 'normal' : 'autoHeight'}
        animateRows
        suppressCellFocus={!onRowOpen}
        rowClass={onRowOpen ? 'row-link' : undefined}
        onRowClicked={onRowOpen ? (e) => e.data && onRowOpen(e.data) : undefined}
        onCellKeyDown={
          onRowOpen
            ? (e) => {
                if ((e.event as KeyboardEvent | undefined)?.key === 'Enter' && e.data) onRowOpen(e.data)
              }
            : undefined
        }
        paginationPageSizeSelector={[20, 25, 50, 100]}
        {...rest}
      />
    </div>
  )
}
