import clsx from 'clsx'
import { AlertTriangle, CheckCircle2, Gauge, Loader2, RefreshCcw, Timer, Zap } from 'lucide-react'

import BarList from '../../components/charts/BarList'
import Sparkline from '../../components/charts/Sparkline'
import { formatCount, formatExact, formatRate } from './usageFormat'

const TONE_CLASSES = {
    default: 'text-foreground',
    primary: 'text-primary',
    success: 'text-emerald-500',
    warning: 'text-amber-500',
    danger: 'text-destructive',
}

export function RangeSelector({ value, options, onChange, onRefresh, refreshing, t }) {
    return (
        <div className="flex flex-wrap items-center gap-2">
            <div className="inline-flex rounded-lg border border-border bg-secondary/50 p-0.5">
                {options.map(option => (
                    <button
                        key={option}
                        type="button"
                        onClick={() => onChange(option)}
                        className={clsx(
                            'rounded-md px-3 py-1.5 text-xs font-medium transition-colors',
                            value === option
                                ? 'bg-primary text-primary-foreground shadow-sm'
                                : 'text-muted-foreground hover:text-foreground'
                        )}
                    >
                        {t(`dashboard.ranges.${option}`)}
                    </button>
                ))}
            </div>
            <button
                type="button"
                onClick={() => onRefresh()}
                disabled={refreshing}
                className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-xs font-medium text-foreground transition-colors hover:bg-secondary disabled:opacity-60"
            >
                {refreshing ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCcw className="h-3.5 w-3.5" />}
                {t('dashboard.refresh')}
            </button>
        </div>
    )
}

export function MetricToggle({ value, options, onChange, t }) {
    return (
        <div className="inline-flex rounded-lg border border-border bg-secondary/50 p-0.5">
            {options.map(option => (
                <button
                    key={option}
                    type="button"
                    onClick={() => onChange(option)}
                    className={clsx(
                        'rounded-md px-3 py-1.5 text-xs font-medium transition-colors',
                        value === option
                            ? 'bg-primary text-primary-foreground shadow-sm'
                            : 'text-muted-foreground hover:text-foreground'
                    )}
                >
                    {t(`dashboard.metric.${option}`)}
                </button>
            ))}
        </div>
    )
}

export function StatCard({ icon: Icon, label, value, hint, tone = 'default' }) {
    return (
        <div className="rounded-2xl border border-border bg-card p-4 shadow-sm">
            <div className="flex items-center justify-between gap-2">
                <span className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">{label}</span>
                {Icon ? <Icon className="h-4 w-4 shrink-0 text-muted-foreground" /> : null}
            </div>
            <div className={clsx('mt-2 text-2xl font-bold leading-tight tabular-nums', TONE_CLASSES[tone] || TONE_CLASSES.default)}>
                {value}
            </div>
            {hint ? <div className="mt-1 text-[11px] text-muted-foreground">{hint}</div> : null}
        </div>
    )
}

function RateCard({ title, subtitle, value, unit, sparklinePoints, color, peakLabel, peakValue, t }) {
    return (
        <div className="rounded-2xl border border-border bg-card p-5 shadow-sm">
            <div className="flex items-start justify-between gap-3">
                <div>
                    <div className="text-sm font-semibold text-foreground">{title}</div>
                    <div className="mt-0.5 text-[11px] text-muted-foreground">{subtitle}</div>
                </div>
                <Gauge className="h-5 w-5 shrink-0 text-primary" />
            </div>
            <div className="mt-4 flex items-end justify-between gap-4">
                <div className="flex items-baseline gap-1.5">
                    <span className="text-3xl font-bold tabular-nums text-foreground">{formatRate(value)}</span>
                    <span className="text-xs font-medium text-muted-foreground">{unit}</span>
                </div>
                <Sparkline points={sparklinePoints} color={color} width={160} height={48} />
            </div>
            <div className="mt-3 flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <Zap className="h-3 w-3" />
                <span>{peakLabel}</span>
                <span className="font-medium tabular-nums text-foreground">{formatExact(peakValue)}</span>
                <span className="sr-only">{t('dashboard.peakHint')}</span>
            </div>
        </div>
    )
}

export function LivePanel({ live, summary, t }) {
    const series = Array.isArray(live?.series) ? live.series : []
    const windowSeconds = Number(live?.window_seconds) || 0

    return (
        <div className="grid gap-4 lg:grid-cols-2">
            <RateCard
                title={t('dashboard.liveRpm')}
                subtitle={t('dashboard.liveWindow', { seconds: windowSeconds })}
                value={live?.rpm}
                unit={t('dashboard.unitRpm')}
                sparklinePoints={series.map(point => point.requests || 0)}
                color="#3b82f6"
                peakLabel={t('dashboard.peakRpm')}
                peakValue={summary?.peak_rpm}
                t={t}
            />
            <RateCard
                title={t('dashboard.liveTpm')}
                subtitle={t('dashboard.liveWindow', { seconds: windowSeconds })}
                value={live?.tpm}
                unit={t('dashboard.unitTpm')}
                sparklinePoints={series.map(point => point.total_tokens || 0)}
                color="#22c55e"
                peakLabel={t('dashboard.peakTpm')}
                peakValue={summary?.peak_tpm}
                t={t}
            />
        </div>
    )
}

export function BreakdownPanel({ title, description, items, valueKey, formatValue, t, icon: Icon }) {
    const rows = (Array.isArray(items) ? items : []).map(item => ({
        key: item.key,
        label: item.key,
        value: Number(item[valueKey]) || 0,
        hint: valueKey === 'requests' ? undefined : `${formatCount(item.requests)} ${t('dashboard.requestsUnit')}`,
    }))

    return (
        <div className="rounded-2xl border border-border bg-card p-5 shadow-sm">
            <div className="mb-4 flex items-start justify-between gap-3">
                <div>
                    <div className="text-sm font-semibold text-foreground">{title}</div>
                    {description ? <div className="mt-0.5 text-[11px] text-muted-foreground">{description}</div> : null}
                </div>
                {Icon ? <Icon className="h-4 w-4 shrink-0 text-muted-foreground" /> : null}
            </div>
            <BarList items={rows} formatValue={formatValue} emptyLabel={t('dashboard.empty')} />
        </div>
    )
}

export function StatusBanner({ error, loading, t }) {
    if (error) {
        return (
            <div className="flex items-center gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-3 text-xs text-destructive">
                <AlertTriangle className="h-4 w-4 shrink-0" />
                <span>{error}</span>
            </div>
        )
    }
    if (loading) {
        return (
            <div className="flex items-center gap-2 rounded-xl border border-border bg-card px-4 py-3 text-xs text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" />
                <span>{t('dashboard.loading')}</span>
            </div>
        )
    }
    return null
}

export function SuccessRateBadge({ summary, t }) {
    const requests = Number(summary?.requests) || 0
    const errors = Number(summary?.errors) || 0
    const healthy = errors === 0
    return (
        <span
            className={clsx(
                'inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[10px] font-medium',
                healthy
                    ? 'border-emerald-500/20 bg-emerald-500/10 text-emerald-500'
                    : 'border-amber-500/20 bg-amber-500/10 text-amber-500'
            )}
        >
            {healthy ? <CheckCircle2 className="h-3 w-3" /> : <AlertTriangle className="h-3 w-3" />}
            {requests === 0 ? t('dashboard.noTraffic') : `${errors} ${t('dashboard.errorsUnit')}`}
        </span>
    )
}

export function RetentionNote({ retention, path, generatedAt, t, formatDateTime }) {
    const live = Number(retention?.live_seconds) || 0
    const minutes = Number(retention?.minute_hours) || 0
    const hours = Number(retention?.hour_days) || 0
    const days = Number(retention?.day_days) || 0
    return (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
            <span className="inline-flex items-center gap-1.5">
                <Timer className="h-3 w-3" />
                {t('dashboard.retentionValue', { live, minutes, hours, days })}
            </span>
            {path ? (
                <span className="truncate" title={path}>
                    {t('dashboard.storage')}: {path}
                </span>
            ) : (
                <span>{t('dashboard.noPersistence')}</span>
            )}
            {generatedAt ? (
                <span>
                    {t('dashboard.updatedAt')}: {formatDateTime(generatedAt)}
                </span>
            ) : null}
        </div>
    )
}
