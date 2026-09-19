import { useCallback, useEffect, useMemo, useState } from 'react'
import clsx from 'clsx'
import { AlertTriangle, Loader2, RefreshCcw, Users, X } from 'lucide-react'

import AreaLineChart from '../../components/charts/AreaLineChart'
import BarList from '../../components/charts/BarList'
import { useI18n } from '../../i18n'
import { MetricToggle } from './DashboardPanels'
import useAccountUsage from './useAccountUsage'
import {
    formatBucketLabel,
    formatCount,
    formatDateTime,
    formatExact,
    formatLatency,
    formatPercent,
} from './usageFormat'

const DETAIL_METRICS = ['requests', 'tokens', 'errors']

function MetricCell({ label, value }) {
    return (
        <div className="rounded-lg border border-border bg-background/60 px-3 py-2">
            <div className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">{label}</div>
            <div className="mt-0.5 text-sm font-semibold tabular-nums text-foreground">{value}</div>
        </div>
    )
}

function DetailSection({ title, children }) {
    return (
        <section className="rounded-xl border border-border p-4">
            <div className="mb-3 text-xs font-semibold text-foreground">{title}</div>
            {children}
        </section>
    )
}

// AccountDetailModal drills into one account: range-scoped trend, model and
// surface mix, and the all-time totals for that account.
function AccountDetailModal({ apiFetch, range, accountId, t, onClose }) {
    const [detail, setDetail] = useState(null)
    const [loading, setLoading] = useState(true)
    const [error, setError] = useState('')
    const [metric, setMetric] = useState('requests')

    useEffect(() => {
        let disposed = false
        async function load() {
            setLoading(true)
            try {
                const res = await apiFetch(
                    `/admin/usage-stats/accounts/${encodeURIComponent(accountId)}?range=${encodeURIComponent(range)}`,
                    { headers: { 'Cache-Control': 'no-store' } }
                )
                const payload = await res.json().catch(() => null)
                if (disposed) return
                if (!res.ok) {
                    throw new Error(payload?.detail || t('dashboard.loadFailed'))
                }
                setDetail(payload)
                setError('')
            } catch (err) {
                if (!disposed) setError(err?.message || t('dashboard.loadFailed'))
            } finally {
                if (!disposed) setLoading(false)
            }
        }
        load()
        return () => {
            disposed = true
        }
    }, [accountId, apiFetch, range, t])

    useEffect(() => {
        const onKey = event => {
            if (event.key === 'Escape') onClose()
        }
        window.addEventListener('keydown', onKey)
        return () => window.removeEventListener('keydown', onKey)
    }, [onClose])

    const series = useMemo(() => detail?.series || [], [detail])
    const labels = useMemo(
        () => series.map(point => formatBucketLabel(point.t, detail?.bucket_ms)),
        [series, detail?.bucket_ms]
    )

    const chartSeries = useMemo(() => {
        if (metric === 'tokens') {
            return [
                {
                    key: 'prompt',
                    label: t('dashboard.promptTokens'),
                    color: '#3b82f6',
                    points: series.map(point => point.prompt_tokens),
                },
                {
                    key: 'completion',
                    label: t('dashboard.completionTokens'),
                    color: '#22c55e',
                    points: series.map(point => point.completion_tokens),
                },
            ]
        }
        if (metric === 'errors') {
            return [
                {
                    key: 'errors',
                    label: t('dashboard.errors'),
                    color: '#ef4444',
                    points: series.map(point => point.errors),
                },
            ]
        }
        return [
            {
                key: 'requests',
                label: t('dashboard.requests'),
                color: '#3b82f6',
                points: series.map(point => point.requests),
            },
        ]
    }, [metric, series, t])

    const chartFormat = metric === 'tokens' ? formatCount : formatExact
    const account = detail?.account
    const totals = detail?.totals

    return (
        <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-background/80 p-4 backdrop-blur-sm sm:p-8">
            <div className="w-full max-w-3xl rounded-2xl border border-border bg-card shadow-2xl">
                <div className="flex items-start justify-between gap-4 border-b border-border p-5">
                    <div className="min-w-0">
                        <h2 className="truncate text-sm font-semibold text-foreground" title={accountId}>
                            {accountId}
                        </h2>
                        <p className="mt-0.5 text-[11px] text-muted-foreground">
                            {t('dashboard.accounts.detailDesc', { range: t(`dashboard.ranges.${detail?.range || range}`) })}
                        </p>
                    </div>
                    <button
                        type="button"
                        onClick={onClose}
                        className="rounded-lg border border-border bg-card p-1.5 text-muted-foreground transition-colors hover:text-foreground"
                        aria-label={t('dashboard.accounts.close')}
                    >
                        <X className="h-4 w-4" />
                    </button>
                </div>

                <div className="space-y-4 p-5">
                    {error ? (
                        <div className="flex items-center gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-3 text-xs text-destructive">
                            <AlertTriangle className="h-4 w-4 shrink-0" />
                            <span>{error}</span>
                        </div>
                    ) : null}

                    {loading && !detail ? (
                        <div className="flex items-center gap-2 rounded-xl border border-border bg-background/60 px-4 py-3 text-xs text-muted-foreground">
                            <Loader2 className="h-4 w-4 animate-spin" />
                            <span>{t('dashboard.loading')}</span>
                        </div>
                    ) : null}

                    {detail ? (
                        <>
                            <div className="grid gap-2 sm:grid-cols-3 lg:grid-cols-4">
                                <MetricCell label={t('dashboard.requests')} value={formatExact(account?.requests)} />
                                <MetricCell label={t('dashboard.accounts.successRate')} value={formatPercent(account?.success_rate)} />
                                <MetricCell label={t('dashboard.totalTokens')} value={formatCount(account?.total_tokens)} />
                                <MetricCell label={t('dashboard.accounts.streams')} value={formatExact(account?.stream_requests)} />
                                <MetricCell label={t('dashboard.accounts.avgRpm')} value={formatExact(Math.round(account?.avg_rpm || 0))} />
                                <MetricCell label={t('dashboard.accounts.avgTpm')} value={formatCount(Math.round(account?.avg_tpm || 0))} />
                                <MetricCell label={t('dashboard.avgLatency')} value={formatLatency(account?.avg_elapsed_ms)} />
                                <MetricCell label={t('dashboard.accounts.lastSeen')} value={formatDateTime(account?.last_seen_at)} />
                            </div>

                            <DetailSection title={t('dashboard.trendTitle')}>
                                <div className="mb-3 flex justify-end">
                                    <MetricToggle value={metric} options={DETAIL_METRICS} onChange={setMetric} t={t} />
                                </div>
                                <AreaLineChart
                                    series={chartSeries}
                                    labels={labels}
                                    height={200}
                                    formatValue={chartFormat}
                                    emptyLabel={t('dashboard.empty')}
                                />
                            </DetailSection>

                            <div className="grid gap-4 sm:grid-cols-2">
                                <DetailSection title={t('dashboard.byModel')}>
                                    <BarList
                                        items={(detail.models || []).map(item => ({
                                            key: item.key,
                                            label: item.key,
                                            value: Number(item.total_tokens) || 0,
                                            hint: `${formatCount(item.requests)} ${t('dashboard.requestsUnit')}`,
                                        }))}
                                        formatValue={formatCount}
                                        emptyLabel={t('dashboard.empty')}
                                    />
                                </DetailSection>
                                <DetailSection title={t('dashboard.bySurface')}>
                                    <BarList
                                        items={(detail.surfaces || []).map(item => ({
                                            key: item.key,
                                            label: item.key,
                                            value: Number(item.requests) || 0,
                                        }))}
                                        formatValue={formatExact}
                                        emptyLabel={t('dashboard.empty')}
                                    />
                                </DetailSection>
                            </div>

                            <DetailSection title={t('dashboard.accounts.totals')}>
                                <div className="grid gap-2 sm:grid-cols-3 lg:grid-cols-4">
                                    <MetricCell label={t('dashboard.requests')} value={formatExact(totals?.requests)} />
                                    <MetricCell label={t('dashboard.accounts.successRate')} value={formatPercent(totals?.success_rate)} />
                                    <MetricCell label={t('dashboard.totalTokens')} value={formatCount(totals?.total_tokens)} />
                                    <MetricCell label={t('dashboard.accounts.streams')} value={formatExact(totals?.stream_requests)} />
                                </div>
                                <div className="mt-3 text-[11px] text-muted-foreground">
                                    {t('dashboard.accounts.firstSeen')}: {formatDateTime(totals?.first_seen_at)}
                                </div>
                            </DetailSection>
                        </>
                    ) : null}
                </div>
            </div>
        </div>
    )
}

// AccountUsageView ranks every account that served traffic in the selected
// range. It also surfaces the attribution gap: requests that failed before an
// account was acquired cannot belong to any account, so they show up as the
// difference between all traffic and attributed traffic rather than being
// silently blamed on a real account.
export default function AccountUsageView({ apiFetch, range, onRefresh, refreshing, t }) {
    const { data, loading, refreshing: polling, error, reload } = useAccountUsage({ apiFetch, range, t })
    const [selected, setSelected] = useState(null)

    const accounts = useMemo(() => data?.accounts || [], [data])
    const summary = data?.summary
    const attributed = data?.attributed
    const attributionGap = Math.max(0, (Number(summary?.requests) || 0) - (Number(attributed?.requests) || 0))

    const handleRefresh = useCallback(() => {
        reload({ silent: true })
        if (onRefresh) onRefresh()
    }, [onRefresh, reload])

    const busy = refreshing || polling

    return (
        <div className="space-y-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0">
                    <h2 className="flex items-center gap-2 text-sm font-semibold text-foreground">
                        <Users className="h-4 w-4 text-primary" />
                        {t('dashboard.accounts.rankTitle')}
                    </h2>
                    <p className="mt-0.5 text-[11px] text-muted-foreground">{t('dashboard.accounts.rankDesc')}</p>
                </div>
                <button
                    type="button"
                    onClick={handleRefresh}
                    disabled={busy}
                    className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-xs font-medium text-foreground transition-colors hover:bg-secondary disabled:opacity-60"
                >
                    {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCcw className="h-3.5 w-3.5" />}
                    {t('dashboard.refresh')}
                </button>
            </div>

            <div className="grid gap-2 sm:grid-cols-3">
                <MetricCell label={t('dashboard.requests')} value={formatExact(summary?.requests)} />
                <MetricCell label={t('dashboard.accounts.attributed')} value={formatExact(attributed?.requests)} />
                <MetricCell label={t('dashboard.accounts.accounts')} value={formatExact(accounts.length)} />
            </div>

            {attributionGap > 0 ? (
                <div className="flex items-start gap-2 rounded-xl border border-amber-500/30 bg-amber-500/10 px-4 py-3 text-[11px] text-amber-500">
                    <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                    <span>
                        {t('dashboard.accounts.attributionNote', {
                            count: formatExact(attributionGap),
                            range: t(`dashboard.ranges.${data?.range || range}`),
                        })}
                    </span>
                </div>
            ) : null}

            {error ? (
                <div className="flex items-center gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-3 text-xs text-destructive">
                    <AlertTriangle className="h-4 w-4 shrink-0" />
                    <span>{error}</span>
                </div>
            ) : null}

            <div className="overflow-x-auto rounded-xl border border-border">
                <table className="w-full min-w-[720px] text-xs">
                    <thead>
                        <tr className="border-b border-border bg-secondary/40 text-[10px] uppercase tracking-wider text-muted-foreground">
                            <th className="px-3 py-2 text-left font-semibold">{t('dashboard.accounts.accountId')}</th>
                            <th className="px-3 py-2 text-right font-semibold">{t('dashboard.requests')}</th>
                            <th className="px-3 py-2 text-right font-semibold">{t('dashboard.accounts.successRate')}</th>
                            <th className="px-3 py-2 text-right font-semibold">{t('dashboard.totalTokens')}</th>
                            <th className="px-3 py-2 text-right font-semibold">{t('dashboard.accounts.streams')}</th>
                            <th className="px-3 py-2 text-right font-semibold">{t('dashboard.accounts.avgRpm')}</th>
                            <th className="px-3 py-2 text-right font-semibold">{t('dashboard.accounts.avgTpm')}</th>
                            <th className="px-3 py-2 text-right font-semibold">{t('dashboard.accounts.lastSeen')}</th>
                        </tr>
                    </thead>
                    <tbody>
                        {accounts.map(account => (
                            <tr
                                key={account.account_id}
                                onClick={() => setSelected(account.account_id)}
                                className="cursor-pointer border-b border-border/60 transition-colors last:border-0 hover:bg-secondary/40"
                            >
                                <td className="max-w-[220px] truncate px-3 py-2 font-medium text-foreground" title={account.account_id}>
                                    {account.account_id}
                                </td>
                                <td className="px-3 py-2 text-right tabular-nums text-foreground">{formatExact(account.requests)}</td>
                                <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">
                                    {formatPercent(account.success_rate)}
                                </td>
                                <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">
                                    {formatCount(account.total_tokens)}
                                </td>
                                <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">
                                    {formatExact(account.stream_requests)}
                                </td>
                                <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">
                                    {formatExact(Math.round(account.avg_rpm || 0))}
                                </td>
                                <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">
                                    {formatCount(Math.round(account.avg_tpm || 0))}
                                </td>
                                <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">
                                    {formatDateTime(account.last_seen_at)}
                                </td>
                            </tr>
                        ))}
                    </tbody>
                </table>
                {accounts.length === 0 && !loading ? (
                    <div className={clsx('py-8 text-center text-xs text-muted-foreground')}>
                        {t('dashboard.accounts.empty')}
                    </div>
                ) : null}
                {loading && accounts.length === 0 ? (
                    <div className="flex items-center justify-center gap-2 py-8 text-xs text-muted-foreground">
                        <Loader2 className="h-4 w-4 animate-spin" />
                        <span>{t('dashboard.loading')}</span>
                    </div>
                ) : null}
            </div>

            {selected ? (
                <AccountDetailModal
                    apiFetch={apiFetch}
                    range={range}
                    accountId={selected}
                    t={t}
                    onClose={() => setSelected(null)}
                />
            ) : null}
        </div>
    )
}
