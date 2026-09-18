import { useCallback, useEffect, useRef, useState } from 'react'

export const RANGE_OPTIONS = ['1h', '24h', '7d', '30d', '90d', '1y']
export const DEFAULT_RANGE = '24h'

// Usage counters move on every request, so a short poll keeps the live RPM/TPM
// strip meaningful without hammering the admin API.
const POLL_INTERVAL_MS = 5000

export default function useUsageStats({ apiFetch, range, t }) {
    const [data, setData] = useState(null)
    const [loading, setLoading] = useState(true)
    const [refreshing, setRefreshing] = useState(false)
    const [error, setError] = useState('')
    const inFlightRef = useRef(false)

    const load = useCallback(
        async ({ silent = false } = {}) => {
            if (inFlightRef.current) return
            inFlightRef.current = true
            if (silent) {
                setRefreshing(true)
            } else {
                setLoading(true)
            }
            try {
                const res = await apiFetch(`/admin/usage-stats?range=${encodeURIComponent(range)}`, {
                    headers: { 'Cache-Control': 'no-store' },
                })
                const payload = await res.json().catch(() => null)
                if (!res.ok) {
                    throw new Error(payload?.detail || t('dashboard.loadFailed'))
                }
                setData(payload)
                setError('')
            } catch (err) {
                setError(err?.message || t('dashboard.loadFailed'))
            } finally {
                inFlightRef.current = false
                setLoading(false)
                setRefreshing(false)
            }
        },
        [apiFetch, range, t]
    )

    useEffect(() => {
        load({ silent: false })
    }, [load])

    useEffect(() => {
        const timer = window.setInterval(() => {
            load({ silent: true })
        }, POLL_INTERVAL_MS)
        return () => window.clearInterval(timer)
    }, [load])

    const reset = useCallback(async () => {
        const res = await apiFetch('/admin/usage-stats', { method: 'DELETE' })
        const payload = await res.json().catch(() => null)
        if (!res.ok) {
            throw new Error(payload?.detail || t('dashboard.resetFailed'))
        }
        await load({ silent: false })
    }, [apiFetch, load, t])

    return { data, loading, refreshing, error, reload: load, reset }
}
