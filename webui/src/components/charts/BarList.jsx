import clsx from 'clsx'

/**
 * Ranked horizontal bar list used for the "top N" breakdown panels.
 * Each item is `{ key, label, value, hint }`.
 */
export default function BarList({ items = [], formatValue = value => String(value), emptyLabel = '', maxItems = 8, className }) {
    const rows = (Array.isArray(items) ? items : []).slice(0, maxItems)
    const peak = rows.reduce((max, item) => Math.max(max, Number(item.value) || 0), 0)

    if (rows.length === 0) {
        return <div className={clsx('py-6 text-center text-xs text-muted-foreground', className)}>{emptyLabel}</div>
    }

    return (
        <ul className={clsx('space-y-3', className)}>
            {rows.map(item => {
                const value = Number(item.value) || 0
                const ratio = peak > 0 ? Math.max(2, (value / peak) * 100) : 0
                return (
                    <li key={item.key} className="space-y-1.5">
                        <div className="flex items-baseline justify-between gap-3 text-xs">
                            <span className="truncate font-medium text-foreground" title={item.label || item.key}>
                                {item.label || item.key}
                            </span>
                            <span className="shrink-0 tabular-nums text-muted-foreground">
                                {formatValue(value)}
                                {item.hint ? <span className="ml-1.5 text-[10px] opacity-70">{item.hint}</span> : null}
                            </span>
                        </div>
                        <div className="h-1.5 overflow-hidden rounded-full bg-secondary">
                            <div className="h-full rounded-full bg-primary" style={{ width: `${ratio}%` }} />
                        </div>
                    </li>
                )
            })}
        </ul>
    )
}
