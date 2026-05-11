import { PluginObject } from 'vue'
import { emitTelemetry } from './otel'

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
  interface Window {
    $log: Logger
  }
}

declare module 'vue/types/vue' {
  interface Vue {
    $log: Logger
  }
}

const noop = () => {}
const noopError = (_: Error) => {}

const realLoggers: Logger = {
  error: (error: Error) => console.error('[%cNEKO%c] %cERR', 'color: #498ad8;', '', 'color: #d84949;', error),
  warn: (...log: any[]) => console.warn('[%cNEKO%c] %cWRN', 'color: #498ad8;', '', 'color: #eae364;', ...log),
  info: (...log: any[]) => console.info('[%cNEKO%c] %cINF', 'color: #498ad8;', '', 'color: #4ac94c;', ...log),
  debug: (...log: any[]) => console.log('[%cNEKO%c] %cDBG', 'color: #498ad8;', '', 'color: #eae364;', ...log),
}

const offLoggers: Logger = {
  error: noopError,
  warn: noop,
  info: noop,
  debug: noop,
}

const LOG_METHODS: (keyof Logger)[] = ['error', 'warn', 'info', 'debug']
const DISABLED_LEVELS = ['off', 'none', '']

function createLoggerForLevel(level: string): Logger {
  const normalized = level.toLowerCase()
  if (DISABLED_LEVELS.includes(normalized)) return offLoggers

  const enabledIndex = LOG_METHODS.indexOf(normalized as keyof Logger)
  if (enabledIndex === -1) return offLoggers

  const logger: Logger = { ...offLoggers }
  for (let i = 0; i <= enabledIndex; i++) {
    const method = LOG_METHODS[i]
    ;(logger as any)[method] = realLoggers[method]
  }
  return logger
}

function getLogLevel(): string {
  const params = new URL(location.href).searchParams
  return params.get('log_level') ?? params.get('logLevel') ?? 'off'
}

const SENSITIVE_FIELD_PATTERNS = [/password/i, /token/i, /jwt/i, /secret/i, /authorization/i]

function sanitizeStringValue(value: string): string {
  if (!value) return value

  const withoutQueries = value.replace(/([a-zA-Z][a-zA-Z0-9+.-]*:\/\/\S+)\?[^\s]*/g, (_, match) => {
    const [base] = match.split('?')
    return base
  })
  return withoutQueries.replace(
    /\beyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]*\.[A-Za-z0-9_-]*\b/g,
    '[REDACTED_JWT]',
  )
}

function sanitizeLogArg(arg: any): any {
  if (typeof arg === 'string') {
    return sanitizeStringValue(arg)
  }

  if (arg instanceof Error) {
    return {
      name: arg.name,
      message: sanitizeStringValue(arg.message),
      stack: sanitizeStringValue(arg.stack || ''),
    }
  }

  if (Array.isArray(arg)) {
    return arg.map((entry) => sanitizeLogArg(entry))
  }

  if (typeof arg === 'object' && arg !== null) {
    const normalized: Record<string, unknown> = {}
    for (const [key, value] of Object.entries(arg)) {
      if (SENSITIVE_FIELD_PATTERNS.some((pattern) => pattern.test(key))) {
        normalized[key] = '[REDACTED]'
      } else {
        normalized[key] = sanitizeLogArg(value)
      }
    }
    return normalized
  }

  return arg
}

function serializeForTelemetry(log: any[]): string {
  return log
    .map((entry) => {
      if (typeof entry === 'object') {
        try {
          return JSON.stringify(sanitizeLogArg(entry))
        } catch (err) {
          return String(entry)
        }
      }
      return sanitizeStringValue(String(entry))
    })
    .join(' ')
}

function emitTelemetryLog(level: keyof Logger, body: string, attributes?: Record<string, string>) {
  const severityConfig: Record<keyof Logger, { number: SeverityNumber; text: string }> = {
    error: { number: SeverityNumber.ERROR, text: 'ERROR' },
    warn: { number: SeverityNumber.WARN, text: 'WARN' },
    info: { number: SeverityNumber.INFO, text: 'INFO' },
    debug: { number: SeverityNumber.DEBUG, text: 'DEBUG' },
  }

  emitTelemetry({
    severityNumber: severityConfig[level].number,
    severityText: severityConfig[level].text,
    body,
    attributes: {
      ...(attributes ?? {}),
    },
  })
}

function shouldEmitToTelemetry(baseLogger: Logger, method: keyof Logger) {
  return baseLogger[method] !== offLoggers[method]
}

const plugin: PluginObject<undefined> = {
  install(Vue) {
    const baseLoggers = createLoggerForLevel(getLogLevel())

    window.$log = {
      error: (error: Error) => {
        baseLoggers.error(error)
        if (shouldEmitToTelemetry(baseLoggers, 'error')) {
          try {
            emitTelemetryLog('error', sanitizeStringValue(error.message || error.toString()), {
              stack: sanitizeStringValue(error.stack || ''),
            })
          } catch (e) {
            console.error('Failed to send log to OTel', e)
          }
        }
      },
      warn: (...log: any[]) => {
        baseLoggers.warn(...log)
        if (shouldEmitToTelemetry(baseLoggers, 'warn')) {
          try {
            emitTelemetryLog('warn', sanitizeStringValue(serializeForTelemetry(log)))
          } catch (e) {
            console.error('Failed to send log to OTel', e)
          }
        }
      },
      info: (...log: any[]) => {
        baseLoggers.info(...log)
        if (shouldEmitToTelemetry(baseLoggers, 'info')) {
          try {
            emitTelemetryLog('info', sanitizeStringValue(serializeForTelemetry(log)))
          } catch (e) {
            console.error('Failed to send log to OTel', e)
          }
        }
      },
      debug: (...log: any[]) => {
        baseLoggers.debug(...log)
        if (shouldEmitToTelemetry(baseLoggers, 'debug')) {
          try {
            emitTelemetryLog('debug', sanitizeStringValue(serializeForTelemetry(log)))
          } catch (e) {
            console.error('Failed to send log to OTel', e)
          }
        }
      },
    }

    Vue.prototype.$log = window.$log
  },
}

export default plugin
