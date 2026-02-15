import { PluginObject } from 'vue'
import { logger } from './otel'

// SeverityNumber from @opentelemetry/api-logs
enum SeverityNumber {
  UNSPECIFIED = 0,
  TRACE = 1,
  TRACE2 = 2,
  TRACE3 = 3,
  TRACE4 = 4,
  DEBUG = 5,
  DEBUG2 = 6,
  DEBUG3 = 7,
  DEBUG4 = 8,
  INFO = 9,
  INFO2 = 10,
  INFO3 = 11,
  INFO4 = 12,
  WARN = 13,
  WARN2 = 14,
  WARN3 = 15,
  WARN4 = 16,
  ERROR = 17,
  ERROR2 = 18,
  ERROR3 = 19,
  ERROR4 = 20,
  FATAL = 21,
  FATAL2 = 22,
  FATAL3 = 23,
  FATAL4 = 24,
}

interface Logger {
  error(error: Error): void
  warn(...log: any[]): void
  info(...log: any[]): void
  debug(...log: any[]): void
}

declare global {
  const $log: Logger

  interface Window {
    $log: Logger
  }
}

declare module 'vue/types/vue' {
  interface Vue {
    $log: Logger
  }
}

const plugin: PluginObject<undefined> = {
  install(Vue) {
    window.$log = {
      error: (error: Error) => {
        console.error('[%cNEKO%c] %cERR', 'color: #498ad8;', '', 'color: #d84949;', error)
        try {
          logger.emit({
            severityNumber: SeverityNumber.ERROR,
            severityText: 'ERROR',
            body: error.message || error.toString(),
            attributes: {
              stack: error.stack
            }
          })
        } catch (e) {
          console.error('Failed to send log to OTel', e)
        }
      },
      warn: (...log: any[]) => {
        console.warn('[%cNEKO%c] %cWRN', 'color: #498ad8;', '', 'color: #eae364;', ...log)
        try {
          logger.emit({
            severityNumber: SeverityNumber.WARN,
            severityText: 'WARN',
            body: log.map(l => (typeof l === 'object' ? JSON.stringify(l) : String(l))).join(' '),
          })
        } catch (e) {
          console.error('Failed to send log to OTel', e)
        }
      },
      info: (...log: any[]) => {
        console.info('[%cNEKO%c] %cINF', 'color: #498ad8;', '', 'color: #4ac94c;', ...log)
        try {
          logger.emit({
            severityNumber: SeverityNumber.INFO,
            severityText: 'INFO',
            body: log.map(l => (typeof l === 'object' ? JSON.stringify(l) : String(l))).join(' '),
          })
        } catch (e) {
          console.error('Failed to send log to OTel', e)
        }
      },
      debug: (...log: any[]) => {
        console.log('[%cNEKO%c] %cDBG', 'color: #498ad8;', '', 'color: #eae364;', ...log)
        try {
          logger.emit({
            severityNumber: SeverityNumber.DEBUG,
            severityText: 'DEBUG',
            body: log.map(l => (typeof l === 'object' ? JSON.stringify(l) : String(l))).join(' '),
          })
        } catch (e) {
          console.error('Failed to send log to OTel', e)
        }
      },
    }

    Vue.prototype.$log = window.$log
  },
}

export default plugin
