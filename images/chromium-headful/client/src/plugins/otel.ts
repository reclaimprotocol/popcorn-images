import { LoggerProvider, BatchLogRecordProcessor } from '@opentelemetry/sdk-logs'
import { OTLPLogExporter } from '@opentelemetry/exporter-logs-otlp-http'
import { resourceFromAttributes } from '@opentelemetry/resources'

const collectorUrl = process.env.VUE_APP_OTEL_COLLECTOR_URL

let sessionId = 'unknown'

try {
    // Expected URL format: /browser/test/<jwt>
    // Filter out empty strings to handle potential trailing slashes
    const pathSegments = window.location.pathname.split('/').filter(Boolean)
    const jwt = pathSegments[pathSegments.length - 1] // Last segment is usually the JWT

    if (jwt && jwt.split('.').length === 3) {
        const payload = jwt.split('.')[1]
        // Fix base64 padding if needed and decode
        const base64 = payload.replace(/-/g, '+').replace(/_/g, '/')
        const jsonPayload = decodeURIComponent(
            atob(base64)
                .split('')
                .map(function (c) {
                    return '%' + ('00' + c.charCodeAt(0).toString(16)).slice(-2)
                })
                .join('')
        )

        const claims = JSON.parse(jsonPayload)
        if (claims.sub) {
            sessionId = claims.sub
        }
    } else {
        console.warn('OTel: Last segment does not look like a JWT (3 parts needed)')
    }
} catch (e) {
    console.warn('Failed to extract SessionId from URL', e)
}

const resource = resourceFromAttributes({
    ['service.name']: 'popcorn-client',
    ['SessionId']: sessionId,
})

let logger = null as ReturnType<LoggerProvider['getLogger']> | null

if (collectorUrl) {
  const exporter = new OTLPLogExporter({
      url: collectorUrl,
  })

  const processor = new BatchLogRecordProcessor(exporter)

  const loggerProvider = new LoggerProvider({
    resource: resource,
    processors: [processor],
  })

  logger = loggerProvider.getLogger('popcorn-client-logger')
}

type OTelPayload = {
  severityNumber: number
  severityText: string
  body: string
  attributes?: Record<string, string>
}

export const isTelemetryEnabled = Boolean(collectorUrl)
export const emitTelemetry = (item: OTelPayload) => {
  if (!logger) return
  logger.emit(item)
}
