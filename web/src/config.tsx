import { createContext, useContext } from 'react'
import type { Config } from './api'

/** Runtime configuration loaded once by App and shared with every page. */
export const ConfigContext = createContext<Config | null>(null)

export const useConfig = () => useContext(ConfigContext)
