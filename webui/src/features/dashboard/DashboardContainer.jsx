import { useCallback, useMemo, useState } from 'react'
import clsx from 'clsx'
import {
    Activity,
    AlertTriangle,
    Clock,
    Cpu,
    Database,
    Layers,
    Loader2,
    RotateCcw,
    Server,
    Timer,
    Users,
} from 'lucide-react'

import AreaLineChart from '../../components/charts/AreaLineChart'
import { useI18n } from '../../i18n'
import {
    BreakdownPanel,
    LivePanel,
    MetricToggle,
    RangeSelector,
    RetentionNote,
    StatCard,
    StatusBanner,
    SuccessRateBadge,
} from './DashboardPanels'
import useUsageStats, { DEFAULT_RANGE, RANGE_OPTIONS } from './useUsageStats'
import {
    formatBucketLabel,
    formatCount,
    formatDateTime,
    formatExact,
    formatLatency,
    formatPercent,
} from './usageFormat'

const METRIC_OPTIONS = ['requests', 'tokens', 'errors']

export default function DashboardContainer({ authFetch }) {
    const { t } = useI18n()
    const apiFetch = authFetch || fetch
    const [range, setRange] = useState(DEFAULT_RANGE)
    const [metric, setMetric] = useState('requests')
    const [confirmReset, setConfirmReset] = useState(false)
    const [resetting, setResetting] = useState(false)
    const [resetError, setResetError] = useState('')

    const { data, loading, refreshing, error, reload, reset } = useUsageStats({ apiFetch, range, t })

    const summary = data?.summary
    const totals = data?.totals
    const series = useMemo(() => data?.series || [], [data])
    const labels = useMemo(
        () => series.map(point => formatBucketLabel(point.t, data?.bucket_ms)),
        [series, data?.bucket_ms]
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

    const handleReset = useCallback(async () => {
        setResetting(true)
        setResetError('')
        try {
            await reset()
            setConfirmReset(false)
        } catch (err) {
            setResetError(err?.message || t('dashboard.resetFailed'))
        } finally {
            setResetting(false)
        }
    }, [reset, t])

    return (
        <div className="space-y-5">
            <div className="flex flex-wrap items-start justify-between gap-4">
                <div className="min-w-0">
                    <h1 className="flex items-center gap-2 text-xl font-bold text-foreground">
                        <Activity className="h-5 w-5 text-primary" />
                        {t('dashboard.title')}
                    </h1>
                    <p className="mt-1 text-xs text-muted-foreground">{t('dashboard.desc')}</p>
                </div>
                <RangeSelector
                    value={range}
                    options={RANGE_OPTIONS}
                    onChange={setRange}
                    onRefresh={() => reload({ silent: true })}
                    refreshing={refreshing}
                    t={t}
                />
            </div>

            <StatusBanner error={error} loading={loading && !data} t={t} />

            <LivePanel live={data?.live} summary={summary} t={t} />

            <section className="space-y-3">
                <div className="flex items-center justify-between gap-3">
                    <div>
                        <h2 className="text-sm font-semibold text-foreground">{t('dashboard.kpiTitle')}</h2>
                        <p className="mt-0.5 text-[11px] text-muted-foreground">{t('dashboard.kpiDesc')}</p>
                    </div>
                    <div className="flex items-center gap-2">
                        <SuccessRateBadge summary={summary} t={t} />
                        <span className="rounded-full border border-border bg-secondary/50 px-2.5 py-1 text-[10px] font-medium text-muted-foreground">
                            {t(`dashboard.ranges.${range}`)}
                        </span>
                    </div>
                </div>
                <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
                    <StatCard
                        icon={Server}
                        label={t('dashboard.requests')}
                        value={formatExact(summary?.requests)}
                        hint={t('dashboard.successRateHint', { value: formatPercent(summary?.success_rate) })}
                    />
                    <StatCard
                        icon={Cpu}
                        label={t('dashboard.totalTokens')}
                        value={formatCount(summary?.total_tokens)}
                        hint={t('dashboard.exactHint', { value: formatExact(summary?.total_tokens) })}
                        tone="primary"
                    />
                    <StatCard
                        icon={Layers}
                        label={t('dashboard.promptTokens')}
                        value={formatCount(summary?.prompt_tokens)}
                        hint={t('dashboard.exactHint', { value: formatExact(summary?.prompt_tokens) })}
                    />
                    <StatCard
                        icon={Layers}
                        label={t('dashboard.completionTokens')}
                        value={formatCount(summary?.completion_tokens)}
                        hint={t('dashboard.exactHint', { value: formatExact(summary?.completion_tokens) })}
                    />
                    <StatCard
                        icon={Database}
                        label={t('dashboard.reasoningTokens')}
                        value={formatCount(summary?.reasoning_tokens)}
                        hint={t('dashboard.exactHint', { value: formatExact(summary?.reasoning_tokens) })}
                    />
                    <StatCard
                        icon={AlertTriangle}
                        label={t('dashboard.errors')}
                        value={formatExact(summary?.errors)}
                        hint={t('dashboard.successRateHint', { value: formatPercent(summary?.success_rate) })}
                        tone={Number(summary?.errors) > 0 ? 'danger' : 'success'}
                    />
                    <StatCard
                        icon={Timer}
                        label={t('dashboard.avgLatency')}
                        value={formatLatency(summary?.avg_elapsed_ms)}
                        hint={t('dashboard.latencyHint')}
                    />
                    <StatCard
                        icon={Clock}
                        label={t('dashboard.maxLatency')}
                        value={formatLatency(summary?.max_elapsed_ms)}
                        hint={t('dashboard.latencyHint')}
                    />
                </div>
            </section>

            <section className="rounded-2xl border border-border bg-card p-5 shadow-sm">
                <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
                    <div>
                        <h2 className="text-sm font-semibold text-foreground">{t('dashboard.trendTitle')}</h2>
                        <p className="mt-0.5 text-[11px] text-muted-foreground">{t('dashboard.trendDesc')}</p>
                    </div>
                    <MetricToggle value={metric} options={METRIC_OPTIONS} onChange={setMetric} t={t} />
                </div>
                <AreaLineChart
                    series={chartSeries}
                    labels={labels}
                    formatValue={chartFormat}
                    emptyLabel={t('dashboard.empty')}
                />
            </section>

            <section className="space-y-3">
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                    <div>
                        <h2 className="text-sm font-semibold text-foreground">{t('dashboard.breakdownTitle')}</h2>
                        <p className="mt-0.5 text-[11px] text-muted-foreground">{t('dashboard.breakdownDesc')}</p>
                    </div>
                    <div className="flex items-center gap-3 text-[11px] text-muted-foreground">
                        <span>
                            {t('dashboard.allTimeRequests')}:{' '}
                            <span className="font-medium tabular-nums text-foreground">{formatExact(totals?.requests)}</span>
                        </span>
                        <span>
                            {t('dashboard.allTimeTokens')}:{' '}
                            <span className="font-medium tabular-nums text-foreground">{formatCount(totals?.total_tokens)}</span>
                        </span>
                    </div>
                </div>
                <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
                    <BreakdownPanel
                        title={t('dashboard.byModel')}
                        items={data?.models}
                        valueKey="total_tokens"
                        formatValue={formatCount}
                        t={t}
                        icon={Cpu}
                    />
                    <BreakdownPanel
                        title={t('dashboard.bySurface')}
                        items={data?.surfaces}
                        valueKey="requests"
                        formatValue={formatExact}
                        t={t}
                        icon={Server}
                    />
                    <BreakdownPanel
                        title={t('dashboard.byAccount')}
                        items={data?.accounts}
                        valueKey="total_tokens"
                        formatValue={formatCount}
                        t={t}
                        icon={Users}
                    />
                    <BreakdownPanel
                        title={t('dashboard.byCaller')}
                        items={data?.callers}
                        valueKey="requests"
                        formatValue={formatExact}
                        t={t}
                        icon={Activity}
                    />
                </div>
            </section>

            <section className="flex flex-wrap items-center justify-between gap-4 rounded-2xl border border-border bg-card p-5 shadow-sm">
                <RetentionNote
                    retention={data?.retention}
                    path={data?.path}
                    generatedAt={data?.generated_at}
                    t={t}
                    formatDateTime={formatDateTime}
                />
                <div className="flex flex-col items-end gap-2">
                    {confirmReset ? (
                        <div className="flex items-center gap-2">
                            <span className="text-[11px] text-muted-foreground">{t('dashboard.resetConfirm')}</span>
                            <button
                                type="button"
                                onClick={handleReset}
                                disabled={resetting}
                                className="inline-flex items-center gap-1.5 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-1.5 text-xs font-medium text-destructive transition-colors hover:bg-destructive/20 disabled:opacity-60"
                            >
                                {resetting ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RotateCcw className="h-3.5 w-3.5" />}
                                {t('dashboard.resetConfirmAction')}
                            </button>
                            <button
                                type="button"
                                onClick={() => {
                                    setConfirmReset(false)
                                    setResetError('')
                                }}
                                className="rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
                            >
                                {t('dashboard.resetCancel')}
                            </button>
                        </div>
                    ) : (
                        <button
                            type="button"
                            onClick={() => setConfirmReset(true)}
                            className={clsx(
                                'inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:border-destructive/30 hover:text-destructive'
                            )}
                        >
                            <RotateCcw className="h-3.5 w-3.5" />
                            {t('dashboard.reset')}
                        </button>
                    )}
                    {resetError ? <span className="text-[11px] text-destructive">{resetError}</span> : null}
                </div>
            </section>
        </div>
    )
}
