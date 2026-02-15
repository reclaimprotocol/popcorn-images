import { LoggerProvider, BatchLogRecordProcessor } from '@opentelemetry/sdk-logs'
import { OTLPLogExporter } from '@opentelemetry/exporter-logs-otlp-http'
import { resourceFromAttributes } from '@opentelemetry/resources'

// Optional: Add semantic conventions if we want standard attribute names, 
// but user asked to minimize deps. 
// We can just use string literals for resource attributes if we didn't install semantic-conventions.

const collectorUrl = process.env.VUE_APP_OTEL_COLLECTOR_URL || 'https://raven.reclaimprotocol.org:4318/v1/logs'

let sessionId = 'unknown'

try {
    // Expected URL format: /browser/test/<jwt>
    // Filter out empty strings to handle potential trailing slashes
    const pathSegments = window.location.pathname.split('/').filter(Boolean)
    console.log('OTel: Path segments:', pathSegments)
    const jwt = pathSegments[pathSegments.length - 1] // Last segment is usually the JWT
    console.log('OTel: Potential JWT:', jwt)

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
        console.log('OTel: Extracted claims:', claims)
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

const exporter = new OTLPLogExporter({
    url: collectorUrl,
})

const processor = new BatchLogRecordProcessor(exporter)

const loggerProvider = new LoggerProvider({
    resource: resource,
    processors: [processor],
})

export const logger = loggerProvider.getLogger('popcorn-client-logger')
