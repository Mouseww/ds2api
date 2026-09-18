import clsx from 'clsx'
import { useId } from 'react'

function toNumber(value) {
    const numeric = typeof value === 'number' ? value : Number(value)
    return Number.isFinite(numeric) && numeric > 0 ? numeric : 0
}

/**
 * Dependency-free sparkline for the live RPM/TPM strip.
 *
 * The SVG stretches to fill its box, so the stroke is pinned with
 * `vector-effect="non-scaling-stroke"` and no text is rendered inside it.
 */
export default function Sparkline({ points = [], color = '#3b82f6', height = 44, width = 150, className }) {
    const gradientId = useId()
    const values = Array.isArray(points) ? points.map(toNumber) : []

    if (values.length === 0) {
        return <div className={clsx('rounded-md bg-secondary/40', className)} style={{ height, width }} />
    }

    const peak = Math.max(...values, 1)
    const step = values.length > 1 ? 100 / (values.length - 1) : 0
    const line = values
        .map((value, index) => {
            const x = values.length > 1 ? index * step : 50
            const y = height - (value / peak) * height
            return `${index === 0 ? 'M' : 'L'}${x.toFixed(2)},${y.toFixed(2)}`
        })
        .join(' ')
    const area = `${line} L100,${height} L0,${height} Z`

    return (
        <svg
            className={className}
            width={width}
            height={height}
            viewBox={`0 0 100 ${height}`}
            preserveAspectRatio="none"
            aria-hidden="true"
        >
            <defs>
                <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor={color} stopOpacity="0.35" />
                    <stop offset="100%" stopColor={color} stopOpacity="0" />
                </linearGradient>
            </defs>
            <path d={area} fill={`url(#${gradientId})`} stroke="none" />
            <path
                d={line}
                fill="none"
                stroke={color}
                strokeWidth="1.5"
                strokeLinecap="round"
                strokeLinejoin="round"
                vectorEffect="non-scaling-stroke"
            />
        </svg>
    )
}
