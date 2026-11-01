import { useEffect, useRef, useState } from 'react'
import { api, type Config } from './api'
import Dashboard from './pages/Dashboard'
import MandateForm from './pages/MandateForm'
import MissionPage from './pages/Mission'
import Ledger from './pages/Ledger'
import Insights from './pages/Insights'
import { ConfigContext } from './config'

function useHashRoute() {
  const [hash, setHash] = useState(window.location.hash || '#/')
  useEffect(() => {
    const fn = () => setHash(window.location.hash || '#/')
    window.addEventListener('hashchange', fn)
    return () => window.removeEventListener('hashchange', fn)
  }, [])
  const [path, query] = hash.slice(1).split('?')
  return { path: path || '/', params: new URLSearchParams(query || '') }
}

/** One line that says what is real and what is simulated. */
function ModePill({ config }: { config: Config }) {
  const simulated: string[] = []
  if (config.paypal_mode === 'mock') simulated.push('PayPal')
  if (config.llm_provider === 'fake') simulated.push('agent')
  if (config.catalog === 'mock') simulated.push('catalog')
  const mcp = config.paypal_mcp?.enabled ? config.paypal_mcp.tools : []
  const title = `PayPal: ${config.paypal_mode} · Agent: ${config.llm_provider} · Catalog: ${config.catalog}${mcp.length ? ` · PayPal MCP: ${mcp.length} read-only tools: ${mcp.join(', ')}` : ''}`
  if (simulated.length) {
    return <span className="modepill mock" title={title}><i />Simulated: {simulated.join(', ')}</span>
  }
  const agent = config.llm_provider.startsWith('anthropic:') ? 'Claude' : config.llm_provider
  const catalog = config.catalog === 'channel3' ? 'Channel3' : config.catalog
  return <span className="modepill" title={title}><i />PayPal {config.paypal_mode} · {agent} · {catalog}{mcp.length ? ' · PayPal MCP' : ''}</span>
}

export default function App() {
  const { path, params } = useHashRoute()
  const [config, setConfig] = useState<Config | null>(null)
  useEffect(() => {
    api.config().then(setConfig).catch(() => setConfig(null))
  }, [])
  // On a route change (not the first load): name the page and move focus to it, so keyboard and screen-reader users land on the new view.
  const firstRoute = useRef(true)
  useEffect(() => {
    const name = path === '/ledger' ? 'Ledger' : path === '/insights' ? 'Insights' : path === '/mandates/new' ? 'New mandate' : path.startsWith('/missions/') ? 'Mission' : 'Mandates'
    document.title = `${name} · Mandate`
    if (firstRoute.current) {
      firstRoute.current = false
      return
    }
    window.scrollTo(0, 0)
    document.getElementById('main')?.focus({ preventScroll: true })
  }, [path])

  let page
  if (path === '/mandates/new') page = <MandateForm />
  else if (path.startsWith('/missions/')) page = <MissionPage id={path.split('/')[2]} params={params} key={path} />
  else if (path === '/ledger') page = <Ledger />
  else if (path === '/insights') page = <Insights />
  else page = <Dashboard />

  const active = (p: string) => (path === p || (p !== '/' && path.startsWith(p)) ? 'active' : '')
  const mandatesActive = path === '/' || path.startsWith('/missions/') || path === '/mandates/new' ? 'active' : ''
  return (
    <ConfigContext.Provider value={config}>
      <div className="app">
        <header className="topbar">
          <a className="brand" href="#/">
            <span className="logo">M</span>
            <span>Mandate</span>
          </a>
          <nav aria-label="Main">
            <a className={mandatesActive} aria-current={mandatesActive ? 'page' : undefined} href="#/">Mandates</a>
            <a className={active('/ledger')} aria-current={active('/ledger') ? 'page' : undefined} href="#/ledger">Ledger</a>
            <a className={active('/insights')} aria-current={active('/insights') ? 'page' : undefined} href="#/insights">Insights</a>
          </nav>
          {config && <ModePill config={config} />}
        </header>
        <main id="main" tabIndex={-1}>{page}</main>
        <footer>
          {config && <div className="footer-mode"><ModePill config={config} /></div>}
          Built for the PayPal AI Hackathon · the agent proposes, only you and PayPal move money.
        </footer>
      </div>
    </ConfigContext.Provider>
  )
}
