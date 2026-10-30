import React from 'react'
import ReactDOM from 'react-dom/client'
import { AllCommunityModule, ModuleRegistry } from 'ag-grid-community'
import { AllCommunityModule as AllChartsCommunityModule, ModuleRegistry as ChartsModuleRegistry } from 'ag-charts-community'
import App from './App'
import './styles.css'

ModuleRegistry.registerModules([AllCommunityModule])
ChartsModuleRegistry.registerModules([AllChartsCommunityModule])

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
