import { useEffect, useState } from 'react'
import { api, type Config } from './api'
import Dashboard from './pages/Dashboard'
import MandateForm from './pages/MandateForm'
import MissionPage from './pages/Mission'
import Ledger from './pages/Ledger'

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

export default function App() {
  const { path, params } = useHashRoute()
  const [config, setConfig] = useState<Config | null>(null)
  useEffect(() => {
    api.config().then(setConfig).catch(() => setConfig(null))
  }, [])

  let page
  if (path === '/mandates/new') page = <MandateForm />
  else if (path.startsWith('/missions/')) page = <MissionPage id={path.split('/')[2]} params={params} key={path} />
  else if (path === '/ledger') page = <Ledger />
  else page = <Dashboard />

  const active = (p: string) => (path === p || (p !== '/' && path.startsWith(p)) ? 'active' : '')
  return (
    <div className="app">
      <header className="topbar">
        <a className="brand" href="#/">
          <span className="logo">M</span>
          <span>
            Mandate
            <small>AI shopping agent, human-approved, PayPal-paid</small>
          </span>
        </a>
        <nav>
          <a className={active('/')} href="#/">Dashboard</a>
          <a className={active('/mandates/new')} href="#/mandates/new">New mandate</a>
          <a className={active('/ledger')} href="#/ledger">Ledger</a>
        </nav>
        {config && (
          <div className="modes" title="Runtime configuration">
            <span className={`mode ${config.paypal_mode === 'mock' ? 'mode-mock' : 'mode-live'}`}>PayPal: {config.paypal_mode}</span>
            <span className={`mode ${config.llm_provider === 'fake' ? 'mode-mock' : 'mode-live'}`}>Agent: {config.llm_provider.replace('anthropic:', '')}</span>
            <span className={`mode ${config.catalog === 'mock' ? 'mode-mock' : 'mode-live'}`}>Catalog: {config.catalog}</span>
            {config.paypal_mcp?.enabled && (
              <span className="mode mode-live" title={`Read-only PayPal MCP tools: ${config.paypal_mcp.tools.join(', ')}`}>
                PayPal MCP: {config.paypal_mcp.tools.length} tools
              </span>
            )}
          </div>
        )}
      </header>
      <main>{page}</main>
      <footer>
        Built for the PayPal AI Hackathon. The agent can research and propose; only a human approval and PayPal checkout can move money.
      </footer>
    </div>
  )
}
