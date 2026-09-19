import { useCallback, useEffect, useRef, useState } from 'react'

// Per-account counters move on every request, but the ranked table is a
// monitoring surface rather than a live gauge, so it polls slower than the
// overview's live RPM/TPM strip.
const POLL_INTERVAL_MS = 15000

// useAccountUsage loads the ranked per-account snapshot for one range. The
// request is skipped entirely while the accounts view is not visible, so the
// dashboard does not pay for a tab the operator is not looking at.
export default function useAccountUsage({ apiFetch, range, t, enabled = true }) {
    const [data, setData] = useState(null)
    const [loading, setLoading] = useState(enabled)
    const [refreshing, setRefreshing] = useState(false)
    const [error, setError] = useState('')
    const inFlightRef = useRef(false)

    const load = useCallback(
        async ({ silent = false } = {}) => {
            if (!enabled || inFlightRef.current) return
            inFlightRef.current = true
            if (silent) {
                setRefreshing(true)
            } else {
                setLoading(true)
            }
            try {
                const res = await apiFetch(
                    `/admin/usage-stats/accounts?range=${encodeURIComponent(range)}`,
                    { headers: { 'Cache-Control': 'no-store' } }
                )
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
        [apiFetch, enabled, range, t]
    )

    useEffect(() => {
        if (!enabled) return
        load({ silent: false })
    }, [enabled, load])

    useEffect(() => {
        if (!enabled) return
        const timer = window.setInterval(() => {
            load({ silent: true })
        }, POLL_INTERVAL_MS)
        return () => window.clearInterval(timer)
    }, [enabled, load])

    return { data, loading, refreshing, error, reload: load }
}
