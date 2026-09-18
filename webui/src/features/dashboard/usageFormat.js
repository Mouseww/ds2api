const COMPACT_STEPS = [
    { threshold: 1e12, suffix: 'T' },
    { threshold: 1e9, suffix: 'B' },
    { threshold: 1e6, suffix: 'M' },
    { threshold: 1e3, suffix: 'K' },
]

// Bucket timestamps are epoch milliseconds, but tolerate epoch seconds so the
// formatters keep working if the API switches units.
export function toMillis(value) {
    const numeric = Number(value) || 0
    if (numeric <= 0) return 0
    return numeric > 1e12 ? numeric : numeric * 1000
}

export function formatExact(value) {
    return (Number(value) || 0).toLocaleString()
}

export function formatCount(value) {
    const numeric = Number(value) || 0
    const magnitude = Math.abs(numeric)
    for (const step of COMPACT_STEPS) {
        if (magnitude >= step.threshold) {
            const scaled = numeric / step.threshold
            return `${scaled >= 100 ? scaled.toFixed(0) : scaled.toFixed(1)}${step.suffix}`
        }
    }
    return formatExact(numeric)
}

export function formatRate(value) {
    const numeric = Number(value) || 0
    if (numeric <= 0) return '0'
    if (numeric >= 100) return numeric.toFixed(0)
    if (numeric >= 10) return numeric.toFixed(1)
    return numeric.toFixed(2)
}

export function formatPercent(value) {
    const numeric = Number(value) || 0
    // The API may report a 0..1 fraction or an already-scaled percentage.
    const percent = numeric > 0 && numeric <= 1 ? numeric * 100 : numeric
    if (percent <= 0) return '0%'
    return `${percent.toFixed(percent >= 99.95 ? 0 : 1)}%`
}

export function formatLatency(value) {
    const numeric = Number(value) || 0
    if (numeric <= 0) return '0 ms'
    if (numeric >= 60000) return `${(numeric / 60000).toFixed(1)} min`
    if (numeric >= 1000) return `${(numeric / 1000).toFixed(2)} s`
    return `${Math.round(numeric)} ms`
}

export function formatBucketLabel(value, bucketMs) {
    const millis = toMillis(value)
    if (!millis) return ''
    const date = new Date(millis)
    const step = Number(bucketMs) || 0
    if (step >= 86400000) {
        return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
    }
    if (step >= 3600000) {
        return date.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit' })
    }
    return date.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

export function formatDateTime(value) {
    const millis = toMillis(value)
    if (!millis) return '-'
    return new Date(millis).toLocaleString()
}
