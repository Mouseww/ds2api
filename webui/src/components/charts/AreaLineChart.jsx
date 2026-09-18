import clsx from 'clsx'
import { useId, useMemo, useState } from 'react'

const PALETTE = ['#3b82f6', '#22c55e', '#f59e0b', '#ef4444', '#a855f7', '#06b6d4']

function toNumber(value) {
    const numeric = typeof value === 'number' ? value : Number(value)
    return Number.isFinite(numeric) && numeric > 0 ? numeric : 0
}

/**
 * Dependency-free multi-series area chart.
 *
 * The SVG stretches horizontally (`preserveAspectRatio="none"`), so strokes are
 * pinned with `vector-effect="non-scaling-stroke"` and every piece of text lives
 * in HTML overlays instead of inside the stretched viewBox.
 *
 * `series` items are `{ key, label, color, points: number[] }` and `labels` is
 * the matching list of x-axis captions.
 */
export default function AreaLineChart({
    series = [],
    labels = [],
    height = 260,
    formatValue = value => String(value),
    emptyLabel = '',
    className,
}) {
    const gradientPrefix = useId().replace(/[^a-zA-Z0-9]/g, '')
    const [hoverIndex, setHoverIndex] = useState(null)

    const prepared = useMemo(
        () =>
            (Array.isArray(series) ? series : []).map((item, index) => ({
                key: item.key || `series-${index}`,
                label: item.label || item.key || `series-${index}`,
                color: item.color || PALETTE[index % PALETTE.length],
                values: Array.isArray(item.points) ? item.points.map(toNumber) : [],
            })),
        [series]
    )

    const pointCount = prepared.reduce((max, item) => Math.max(max, item.values.length), 0)
    const peak = prepared.reduce((max, item) => {
        for (const value of item.values) {
            if (value > max) max = value
        }
        return max
    }, 0)
    const scaleMax = peak > 0 ? peak : 1

    if (pointCount === 0) {
        return (
            <div
                className={clsx(
                    'flex items-center justify-center rounded-xl border border-dashed border-border text-xs text-muted-foreground',
                    className
                )}
                style={{ height }}
            >
                {emptyLabel}
            </div>
        )
    }

    const xAt = index => (pointCount > 1 ? (index / (pointCount - 1)) * 1000 : 500)
    const yAt = value => height - (value / scaleMax) * height

    const handlePointerMove = event => {
        const rect = event.currentTarget.getBoundingClientRect()
        if (!rect.width) return
        const ratio = Math.min(1, Math.max(0, (event.clientX - rect.left) / rect.width))
        const index = pointCount > 1 ? Math.round(ratio * (pointCount - 1)) : 0
        setHoverIndex(index)
    }

    const tooltipPercent = hoverIndex === null ? 0 : (xAt(hoverIndex) / 1000) * 100
    const tooltipAlign = tooltipPercent > 72 ? 'right' : tooltipPercent < 28 ? 'left' : 'center'
    const tooltipStyle =
        tooltipAlign === 'center'
            ? { left: `${tooltipPercent}%`, transform: 'translateX(-50%)' }
            : tooltipAlign === 'right'
              ? { right: 0 }
              : { left: 0 }

    return (
        <div className={clsx('space-y-3', className)}>
            {prepared.length > 1 && (
                <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground">
                    {prepared.map(item => (
                        <span key={item.key} className="inline-flex items-center gap-1.5">
                            <span className="h-2 w-2 rounded-full" style={{ backgroundColor: item.color }} />
                            {item.label}
                        </span>
                    ))}
                </div>
            )}

            <div className="relative" style={{ height }}>
                <svg
                    className="absolute inset-0 h-full w-full"
                    viewBox={`0 0 1000 ${height}`}
                    preserveAspectRatio="none"
                    role="img"
                    aria-label={prepared.map(item => item.label).join(', ')}
                >
                    <defs>
                        {prepared.map(item => (
                            <linearGradient key={item.key} id={`${gradientPrefix}-${item.key}`} x1="0" y1="0" x2="0" y2="1">
                                <stop offset="0%" stopColor={item.color} stopOpacity="0.3" />
                                <stop offset="100%" stopColor={item.color} stopOpacity="0.02" />
                            </linearGradient>
                        ))}
                    </defs>

                    {[0.25, 0.5, 0.75].map(ratio => (
                        <line
                            key={ratio}
                            x1="0"
                            x2="1000"
                            y1={height * ratio}
                            y2={height * ratio}
                            stroke="currentColor"
                            strokeOpacity="0.12"
                            strokeDasharray="4 6"
                            vectorEffect="non-scaling-stroke"
                        />
                    ))}

                    {prepared.map(item => {
                        if (item.values.length === 0) return null
                        const line = item.values
                            .map((value, index) => `${index === 0 ? 'M' : 'L'}${xAt(index).toFixed(2)},${yAt(value).toFixed(2)}`)
                            .join(' ')
                        const area = `${line} L1000,${height} L0,${height} Z`
                        return (
                            <g key={item.key}>
                                <path d={area} fill={`url(#${gradientPrefix}-${item.key})`} stroke="none" />
                                <path
                                    d={line}
                                    fill="none"
                                    stroke={item.color}
                                    strokeWidth="2"
                                    strokeLinecap="round"
                                    strokeLinejoin="round"
                                    vectorEffect="non-scaling-stroke"
                                />
                            </g>
                        )
                    })}

                    {hoverIndex !== null && (
                        <line
                            x1={xAt(hoverIndex)}
                            x2={xAt(hoverIndex)}
                            y1="0"
                            y2={height}
                            stroke="currentColor"
                            strokeOpacity="0.35"
                            vectorEffect="non-scaling-stroke"
                        />
                    )}
                </svg>

                <div
                    className="absolute inset-0 cursor-crosshair"
                    onPointerMove={handlePointerMove}
                    onPointerLeave={() => setHoverIndex(null)}
                />

                {hoverIndex !== null && (
                    <div
                        className="pointer-events-none absolute top-2 z-10 min-w-[150px] rounded-lg border border-border bg-card/95 p-3 text-xs shadow-lg backdrop-blur"
                        style={tooltipStyle}
                    >
                        <div className="mb-1.5 font-semibold text-foreground">{labels[hoverIndex] || ''}</div>
                        <div className="space-y-1">
                            {prepared.map(item => (
                                <div key={item.key} className="flex items-center justify-between gap-4">
                                    <span className="inline-flex items-center gap-1.5 text-muted-foreground">
                                        <span className="h-2 w-2 rounded-full" style={{ backgroundColor: item.color }} />
                                        {item.label}
                                    </span>
                                    <span className="font-medium tabular-nums text-foreground">
                                        {formatValue(item.values[hoverIndex] || 0)}
                                    </span>
                                </div>
                            ))}
                        </div>
                    </div>
                )}
            </div>

            {labels.length > 0 && (
                <div className="flex justify-between text-[10px] tabular-nums text-muted-foreground">
                    <span>{labels[0]}</span>
                    {labels.length > 2 && <span>{labels[Math.floor(labels.length / 2)]}</span>}
                    <span>{labels[labels.length - 1]}</span>
                </div>
            )}
        </div>
    )
}
