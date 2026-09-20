import { useEffect, useMemo, useState } from 'react'
import { ChevronLeft, ChevronRight, Check, Copy, Pencil, Play, Plus, Trash2, FolderX, ToggleLeft, ToggleRight, AlertTriangle, X, ListFilter } from 'lucide-react'
import clsx from 'clsx'

function formatUnbanTime(muteUntil) {
    if (!muteUntil || muteUntil <= 0) return null
    const date = new Date(muteUntil * 1000)
    return date.toLocaleString()
}

function formatTokens(n) {
    if (!n) return '0'
    if (n >= 1_000_000_000) return (n / 1_000_000_000).toFixed(2) + 'B'
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M'
    if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
    return String(n)
}

export default function AccountsTable({
    t,
    accounts,
    loadingAccounts,
    testing,
    testingAll,
    batchProgress,
    sessionCounts,
    deletingSessions,
    updatingProxy,
    totalAccounts,
    quotaWindowHours = 24,
    page,
    pageSize,
    totalPages,
    resolveAccountIdentifier,
    proxies,
    onTestAll,
    onShowAddAccount,
    onEditAccount,
    onTestAccount,
    onDeleteAccount,
    onDeleteAllSessions,
    onUpdateAccountProxy,
    onPrevPage,
    onNextPage,
    onPageSizeChange,
    searchQuery,
    onSearchChange,
    filters,
    onFilterChange,
    onResetFilters,
    sortKey,
    sortOrder,
    onSortChange,
    onBatchDelete,
    onBatchEnable,
    onBatchDisable,
    onBatchProxy,
    batchOperating = false,
    onUpdateQuotaWindow,
    quotaWindowSaving = false,
    envBacked = false,
    onToggleEnabled,
    togglingEnabled = {},
}) {
    const [copiedId, setCopiedId] = useState(null)
    const [selected, setSelected] = useState(() => new Set())
    const [batchProxyId, setBatchProxyId] = useState('')
    const [windowInput, setWindowInput] = useState(quotaWindowHours)

    // Keep the inline window editor in sync when the effective window changes
    // after a save or a fresh list fetch.
    useEffect(() => {
        setWindowInput(quotaWindowHours)
    }, [quotaWindowHours])

    const windowValue = Number(windowInput)
    const windowDirty = Number.isFinite(windowValue) && windowValue !== Number(quotaWindowHours)
    const windowValid = Number.isFinite(windowValue) && windowValue >= 0 && windowValue <= 168

    const applyQuotaWindow = () => {
        if (!windowValid || !onUpdateQuotaWindow) return
        onUpdateQuotaWindow(windowValue)
    }

    const copyId = (id) => {
        navigator.clipboard.writeText(id).then(() => {
            setCopiedId(id)
            setTimeout(() => setCopiedId(null), 1500)
        })
    }

    const pageIds = useMemo(
        () => accounts.map(acc => resolveAccountIdentifier(acc)).filter(Boolean),
        [accounts, resolveAccountIdentifier],
    )
    const selectedOnPage = pageIds.filter(id => selected.has(id))
    const allOnPageSelected = pageIds.length > 0 && selectedOnPage.length === pageIds.length
    const someOnPageSelected = selectedOnPage.length > 0 && !allOnPageSelected
    const selectedIds = useMemo(() => Array.from(selected), [selected])

    const toggleSelected = (id) => {
        setSelected(prev => {
            const next = new Set(prev)
            if (next.has(id)) {
                next.delete(id)
            } else {
                next.add(id)
            }
            return next
        })
    }

    const toggleSelectAllOnPage = () => {
        setSelected(prev => {
            const next = new Set(prev)
            if (allOnPageSelected) {
                for (const id of pageIds) next.delete(id)
            } else {
                for (const id of pageIds) next.add(id)
            }
            return next
        })
    }

    const clearSelection = () => setSelected(new Set())

    const runBatchAction = async (fn, ids) => {
        const ok = await fn(ids)
        if (ok) clearSelection()
    }

    const hasActiveFilter = filters && Object.values(filters).some(value => value && value !== 'all')

    const filterSelect = (key, label, options) => (
        <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <span className="whitespace-nowrap">{label}</span>
            <select
                value={filters?.[key] || 'all'}
                onChange={e => onFilterChange(key, e.target.value)}
                className="px-2 py-1 text-xs bg-muted border border-border rounded-md focus:outline-none focus:ring-1 focus:ring-ring"
            >
                {options.map(([value, text]) => (
                    <option key={value} value={value}>{text}</option>
                ))}
            </select>
        </label>
    )

    const sortOptions = [
        ['default', t('accountManager.sortDefault')],
        ['usage_tokens', t('accountManager.sortUsageTokens')],
        ['usage_requests', t('accountManager.sortUsageRequests')],
        ['usage_today_tokens', t('accountManager.sortTodayTokens')],
        ['usage_today_requests', t('accountManager.sortTodayRequests')],
        ['test_status', t('accountManager.sortTestStatus')],
        ['enabled', t('accountManager.sortEnabled')],
        ['name', t('accountManager.sortName')],
        ['identifier', t('accountManager.sortIdentifier')],
    ]

    return (
        <div className="bg-card border border-border rounded-xl overflow-hidden shadow-sm">
            <div className="p-6 border-b border-border flex flex-col md:flex-row md:items-center justify-between gap-4">
                <div>
                    <h2 className="text-lg font-semibold">{t('accountManager.accountsTitle')}</h2>
                    <p className="text-sm text-muted-foreground">{t('accountManager.accountsDesc')}</p>
                </div>
                <div className="flex flex-wrap gap-2">
                    <input
                        type="text"
                        value={searchQuery}
                        onChange={e => onSearchChange(e.target.value)}
                        placeholder={t('accountManager.searchPlaceholder')}
                        className="px-3 py-1.5 text-sm bg-muted border border-border rounded-lg focus:outline-none focus:ring-1 focus:ring-ring placeholder:text-muted-foreground"
                    />
                    <button
                        onClick={onTestAll}
                        disabled={testingAll || totalAccounts === 0}
                        className="flex items-center px-3 py-2 bg-secondary text-secondary-foreground rounded-lg hover:bg-secondary/80 transition-colors text-xs font-medium border border-border disabled:opacity-50"
                    >
                        {testingAll ? <span className="animate-spin mr-2">⟳</span> : <Play className="w-3 h-3 mr-2" />}
                        {t('accountManager.testAll')}
                    </button>
                    <button
                        onClick={onShowAddAccount}
                        className="flex items-center gap-2 px-4 py-2 bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 transition-colors font-medium text-sm shadow-sm"
                    >
                        <Plus className="w-4 h-4" />
                        {t('accountManager.addAccount')}
                    </button>
                </div>
            </div>

            <div className="p-4 border-b border-border bg-muted/30 flex flex-wrap items-center gap-x-4 gap-y-2">
                <span className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                    <ListFilter className="w-3.5 h-3.5" />
                    {t('accountManager.filterSortLabel')}
                </span>
                {filterSelect('enabled', t('accountManager.filterEnabledLabel'), [
                    ['all', t('accountManager.filterAll')],
                    ['true', t('accountManager.filterEnabledOnly')],
                    ['false', t('accountManager.filterDisabledOnly')],
                ])}
                {filterSelect('test_status', t('accountManager.filterTestStatusLabel'), [
                    ['all', t('accountManager.filterAll')],
                    ['ok', t('accountManager.filterTestOk')],
                    ['failed', t('accountManager.filterTestFailed')],
                    ['unknown', t('accountManager.filterTestUnknown')],
                ])}
                {filterSelect('banned', t('accountManager.filterBannedLabel'), [
                    ['all', t('accountManager.filterAll')],
                    ['true', t('accountManager.filterBannedOnly')],
                    ['false', t('accountManager.filterNotBanned')],
                ])}
                {filterSelect('daily_limited', t('accountManager.filterDailyLimitedLabel'), [
                    ['all', t('accountManager.filterAll')],
                    ['true', t('accountManager.filterDailyLimitedOnly')],
                    ['false', t('accountManager.filterNotDailyLimited')],
                ])}
                {filterSelect('has_proxy', t('accountManager.filterProxyLabel'), [
                    ['all', t('accountManager.filterAll')],
                    ['true', t('accountManager.filterHasProxy')],
                    ['false', t('accountManager.filterNoProxy')],
                ])}
                {filterSelect('in_pool', t('accountManager.filterPoolLabel'), [
                    ['all', t('accountManager.filterAll')],
                    ['true', t('accountManager.filterInPool')],
                    ['false', t('accountManager.filterNotInPool')],
                ])}
                <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <span className="whitespace-nowrap">{t('accountManager.sortLabel')}</span>
                    <select
                        value={sortKey || 'default'}
                        onChange={e => onSortChange(e.target.value, sortOrder)}
                        className="px-2 py-1 text-xs bg-muted border border-border rounded-md focus:outline-none focus:ring-1 focus:ring-ring"
                    >
                        {sortOptions.map(([value, text]) => (
                            <option key={value} value={value}>{text}</option>
                        ))}
                    </select>
                </label>
                <label className={clsx(
                    "flex items-center gap-1.5 text-xs text-muted-foreground",
                    (!sortKey || sortKey === 'default') && "opacity-50 pointer-events-none",
                )}>
                    <span className="whitespace-nowrap">{t('accountManager.orderLabel')}</span>
                    <select
                        value={sortOrder || 'desc'}
                        onChange={e => onSortChange(sortKey, e.target.value)}
                        className="px-2 py-1 text-xs bg-muted border border-border rounded-md focus:outline-none focus:ring-1 focus:ring-ring"
                    >
                        <option value="desc">{t('accountManager.orderDesc')}</option>
                        <option value="asc">{t('accountManager.orderAsc')}</option>
                    </select>
                </label>
                <span className="flex items-center gap-1.5 text-xs text-muted-foreground ml-auto">
                    <span className="whitespace-nowrap font-medium" title={t('accountManager.quotaWindowHelp')}>
                        {t('accountManager.quotaWindowLabel')}
                    </span>
                    <input
                        type="number"
                        min={0}
                        max={168}
                        step={1}
                        value={windowInput}
                        onChange={e => setWindowInput(e.target.value)}
                        onKeyDown={e => {
                            if (e.key === 'Enter') applyQuotaWindow()
                        }}
                        disabled={quotaWindowSaving}
                        className="w-16 px-2 py-1 text-xs bg-muted border border-border rounded-md focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50"
                    />
                    <span className="whitespace-nowrap">{t('accountManager.quotaWindowUnit')}</span>
                    <button
                        onClick={applyQuotaWindow}
                        disabled={!windowDirty || !windowValid || quotaWindowSaving}
                        title={t('accountManager.quotaWindowHelp')}
                        className="flex items-center gap-1 px-2 py-1 text-xs font-medium bg-secondary text-secondary-foreground border border-border rounded-md hover:bg-secondary/80 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
                    >
                        {quotaWindowSaving ? <span className="animate-spin">⟳</span> : <Check className="w-3 h-3" />}
                        {t('accountManager.quotaWindowApply')}
                    </button>
                </span>
                {hasActiveFilter && (
                    <button
                        onClick={onResetFilters}
                        className="flex items-center gap-1 px-2 py-1 text-xs text-muted-foreground hover:text-foreground border border-border rounded-md hover:bg-secondary transition-colors"
                    >
                        <X className="w-3 h-3" />
                        {t('accountManager.resetFilters')}
                    </button>
                )}
            </div>

            {selectedIds.length > 0 && (
                <div className="p-3 border-b border-border bg-primary/5 flex flex-wrap items-center gap-2">
                    <span className="text-sm font-medium">{t('accountManager.batchSelected', { count: selectedIds.length })}</span>
                    <button
                        onClick={() => runBatchAction(onBatchEnable, selectedIds)}
                        disabled={batchOperating}
                        className="flex items-center gap-1 px-3 py-1.5 text-xs font-medium bg-emerald-500/10 text-emerald-600 border border-emerald-500/30 rounded-md hover:bg-emerald-500/20 transition-colors disabled:opacity-50"
                    >
                        <ToggleRight className="w-3.5 h-3.5" />
                        {t('accountManager.batchEnable')}
                    </button>
                    <button
                        onClick={() => runBatchAction(onBatchDisable, selectedIds)}
                        disabled={batchOperating}
                        className="flex items-center gap-1 px-3 py-1.5 text-xs font-medium bg-amber-500/10 text-amber-600 border border-amber-500/30 rounded-md hover:bg-amber-500/20 transition-colors disabled:opacity-50"
                    >
                        <ToggleLeft className="w-3.5 h-3.5" />
                        {t('accountManager.batchDisable')}
                    </button>
                    <span className="flex items-center gap-1.5">
                        <select
                            value={batchProxyId}
                            onChange={e => setBatchProxyId(e.target.value)}
                            disabled={batchOperating}
                            className="max-w-[180px] px-2.5 py-1.5 text-xs bg-secondary border border-border rounded-md focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50"
                        >
                            <option value="">{t('accountManager.proxyNone')}</option>
                            {proxies.map(proxy => (
                                <option key={proxy.id} value={proxy.id}>
                                    {proxy.name || `${proxy.host}:${proxy.port}`}
                                </option>
                            ))}
                        </select>
                        <button
                            onClick={() => runBatchAction(ids => onBatchProxy(ids, batchProxyId), selectedIds)}
                            disabled={batchOperating}
                            className="px-3 py-1.5 text-xs font-medium bg-secondary text-secondary-foreground border border-border rounded-md hover:bg-secondary/80 transition-colors disabled:opacity-50"
                        >
                            {t('accountManager.batchSetProxy')}
                        </button>
                    </span>
                    <button
                        onClick={() => runBatchAction(onBatchDelete, selectedIds)}
                        disabled={batchOperating}
                        className="flex items-center gap-1 px-3 py-1.5 text-xs font-medium bg-destructive/10 text-destructive border border-destructive/30 rounded-md hover:bg-destructive/20 transition-colors disabled:opacity-50"
                    >
                        <Trash2 className="w-3.5 h-3.5" />
                        {t('accountManager.batchDelete')}
                    </button>
                    <button
                        onClick={clearSelection}
                        disabled={batchOperating}
                        className="flex items-center gap-1 px-2 py-1.5 text-xs text-muted-foreground hover:text-foreground rounded-md hover:bg-secondary transition-colors disabled:opacity-50"
                    >
                        <X className="w-3 h-3" />
                        {t('accountManager.clearSelection')}
                    </button>
                </div>
            )}

            {testingAll && batchProgress.total > 0 && (
                <div className="p-4 border-b border-border bg-muted/30">
                    <div className="flex items-center justify-between text-sm mb-2">
                        <span className="font-medium">{t('accountManager.testingAllAccounts')}</span>
                        <span className="text-muted-foreground">{batchProgress.current} / {batchProgress.total}</span>
                    </div>
                    <div className="w-full bg-muted rounded-full h-2 overflow-hidden mb-4">
                        <div
                            className="bg-primary h-full transition-all duration-300"
                            style={{ width: `${(batchProgress.current / batchProgress.total) * 100}%` }}
                        />
                    </div>
                    {batchProgress.results.length > 0 && (
                        <div className="grid grid-cols-2 md:grid-cols-4 gap-2 max-h-32 overflow-y-auto custom-scrollbar">
                            {batchProgress.results.map((r, i) => (
                                <div key={i} className={clsx(
                                    "text-xs px-2 py-1 rounded border truncate",
                                    r.banned ? "bg-yellow-400/10 border-yellow-400/30 text-yellow-600" :
                                    r.success ? "bg-emerald-500/10 border-emerald-500/20 text-emerald-500" : "bg-destructive/10 border-destructive/20 text-destructive"
                                )}>
                                    {r.banned ? '⚠' : r.success ? '✓' : '✗'} {r.id}
                                </div>
                            ))}
                        </div>
                    )}
                </div>
            )}

            {accounts.length > 0 && !loadingAccounts && (
                <div className="px-4 py-2 border-b border-border flex items-center gap-3 bg-muted/20">
                    <label className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer select-none">
                        <input
                            type="checkbox"
                            checked={allOnPageSelected}
                            ref={el => {
                                if (el) el.indeterminate = someOnPageSelected
                            }}
                            onChange={toggleSelectAllOnPage}
                            className="w-4 h-4 accent-primary cursor-pointer"
                        />
                        {t('accountManager.selectAllOnPage')}
                    </label>
                    {selectedIds.length > 0 && (
                        <span className="text-xs text-muted-foreground">
                            {t('accountManager.batchSelected', { count: selectedIds.length })}
                        </span>
                    )}
                </div>
            )}

            <div className="divide-y divide-border">
                {loadingAccounts ? (
                    <div className="p-8 text-center text-muted-foreground">{t('actions.loading')}</div>
                ) : accounts.length > 0 ? (
                    accounts.map((acc, i) => {
                        const id = resolveAccountIdentifier(acc)
                        const assignedProxy = proxies.find(proxy => proxy.id === acc.proxy_id)
                        const runtimeUnknown = envBacked && !acc.test_status
                        const isActive = acc.test_status === 'ok' || acc.has_token
                        const isBanned = acc.ban_is_muted === 1
                        const isEnabled = acc.enabled !== false
                        const unbanTime = formatUnbanTime(acc.ban_mute_until)
                        return (
                            <div key={i} className={clsx("p-4 flex flex-col md:flex-row md:items-center justify-between gap-4 hover:bg-muted/50 transition-colors", !isEnabled && "opacity-60")}>
                                <div className="flex items-center gap-3 min-w-0">
                                    <input
                                        type="checkbox"
                                        checked={Boolean(id) && selected.has(id)}
                                        onChange={() => toggleSelected(id)}
                                        disabled={!id}
                                        className="w-4 h-4 accent-primary shrink-0 cursor-pointer disabled:opacity-30"
                                    />
                                    <div className={clsx(
                                        "w-2 h-2 rounded-full shrink-0",
                                        isBanned ? "bg-yellow-400 shadow-[0_0_8px_rgba(250,204,21,0.7)]" :
                                        acc.test_status === 'failed' ? "bg-red-500 shadow-[0_0_8px_rgba(239,68,68,0.5)]" :
                                        isActive ? "bg-emerald-500 shadow-[0_0_8px_rgba(16,185,129,0.5)]" :
                                        runtimeUnknown ? "bg-blue-500 shadow-[0_0_8px_rgba(59,130,246,0.5)]" : "bg-amber-500"
                                    )} />
                                    <div className="min-w-0">
                                        <div className="flex items-center gap-2">
                                            <div className="text-sm font-medium truncate">{acc.name || '-'}</div>
                                            {acc.in_pool && (
                                                <span className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-emerald-500/10 text-emerald-500 border border-emerald-500/20" title={t('accountManager.inPoolBadgeTitle')}>
                                                    {t('accountManager.inPoolBadge')}
                                                </span>
                                            )}
                                            {isBanned && (
                                                <span className="flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium bg-yellow-400/10 text-yellow-600 border border-yellow-400/30">
                                                    <AlertTriangle className="w-3 h-3" /> {t('accountManager.banned')}
                                                </span>
                                            )}
                                            {!isEnabled && !isBanned && acc.disabled_reason === 'manual' && (
                                                <span className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-amber-500/10 text-amber-500 border border-amber-500/20">
                                                    {t('accountManager.disabledManual')}
                                                </span>
                                            )}
                                            {!isEnabled && acc.disabled_reason === 'banned' && (
                                                <span className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-yellow-400/10 text-yellow-600 border border-yellow-400/30">
                                                    {t('accountManager.disabledByBan')}
                                                </span>
                                            )}
                                        </div>
                                        <div
                                            className="font-medium truncate flex items-center gap-1.5 cursor-pointer hover:text-primary transition-colors group"
                                            onClick={() => copyId(id)}
                                        >
                                            <span className="truncate">{id || '-'}</span>
                                            {copiedId === id
                                                ? <Check className="w-3 h-3 text-emerald-500 shrink-0" />
                                                : <Copy className="w-3 h-3 opacity-0 group-hover:opacity-50 shrink-0 transition-opacity" />
                                            }
                                        </div>
                                        {acc.remark && (
                                            <div className="text-xs text-muted-foreground truncate mt-0.5">{acc.remark}</div>
                                        )}
                                        {isBanned && unbanTime && (
                                            <div className="text-[10px] text-yellow-600 mt-0.5">{t('accountManager.unbanAt', { time: unbanTime })}</div>
                                        )}
                                        {isBanned && !unbanTime && (
                                            <div className="text-[10px] text-yellow-600/70 mt-0.5">{t('accountManager.noUnbanTime')}</div>
                                        )}
                                        {acc.ban_status > 0 && (
                                            <span className="text-[10px] text-muted-foreground mt-0.5">{t('accountManager.statusCode')}: {acc.ban_status}</span>
                                        )}
                                        <div className="flex items-center gap-2 text-xs text-muted-foreground mt-0.5 flex-wrap">
                                            <span>{acc.test_status === 'failed' ? t('accountManager.testStatusFailed') : isActive ? t('accountManager.sessionActive') : runtimeUnknown ? t('accountManager.runtimeStatusUnknown') : t('accountManager.reauthRequired')}</span>
                                            {acc.token_preview && (
                                                <span className="font-mono bg-muted px-1.5 py-0.5 rounded text-[10px]">
                                                    {acc.token_preview}
                                                </span>
                                            )}
                                            {sessionCounts && sessionCounts[id] !== undefined && (
                                                <span className="font-mono bg-blue-500/10 text-blue-500 px-1.5 py-0.5 rounded text-[10px]">
                                                    {t('accountManager.sessionCount', { count: sessionCounts[id] })}
                                                </span>
                                            )}
                                            <span className="font-mono bg-violet-500/10 text-violet-500 px-1.5 py-0.5 rounded text-[10px]" title={t('accountManager.usageTokens', { tokens: formatTokens(acc.usage_total_tokens || 0) })}>
                                                {t('accountManager.usageTokens', { tokens: formatTokens(acc.usage_total_tokens || 0) })}
                                            </span>
                                            <span className="font-mono bg-cyan-500/10 text-cyan-500 px-1.5 py-0.5 rounded text-[10px]">
                                                {t('accountManager.usageRequests', { count: acc.usage_requests || 0 })}
                                            </span>
                                            {(acc.daily_token_limit > 0 || acc.daily_request_limit > 0) && (
                                                <span
                                                    className={clsx(
                                                        "font-mono px-1.5 py-0.5 rounded text-[10px] border",
                                                        acc.daily_limited
                                                            ? "bg-yellow-400/10 text-yellow-600 border-yellow-400/30"
                                                            : "bg-muted text-muted-foreground border-border"
                                                    )}
                                                    title={t('accountManager.usageWindowTitle', {
                                                        hours: quotaWindowHours,
                                                        tokens: formatTokens(acc.usage_today_tokens || 0),
                                                        tokenLimit: acc.daily_token_limit > 0 ? formatTokens(acc.daily_token_limit) : '-',
                                                        requests: acc.usage_today_requests || 0,
                                                        requestLimit: acc.daily_request_limit > 0 ? acc.daily_request_limit : '-',
                                                    })}
                                                >
                                                    {t('accountManager.usageWindow', {
                                                        hours: quotaWindowHours,
                                                        tokens: formatTokens(acc.usage_today_tokens || 0),
                                                        requests: acc.usage_today_requests || 0,
                                                    })}
                                                </span>
                                            )}
                                            {acc.daily_limited && (
                                                <span className="font-mono bg-yellow-400/10 text-yellow-600 px-1.5 py-0.5 rounded text-[10px] border border-yellow-400/30">
                                                    {t('accountManager.dailyLimitReached')}
                                                </span>
                                            )}
                                            {sessionCounts && sessionCounts[id] !== undefined && sessionCounts[id] > 0 && (
                                                <button
                                                    onClick={() => onDeleteAllSessions(id)}
                                                    disabled={deletingSessions && deletingSessions[id]}
                                                    className="flex items-center gap-1 font-mono bg-red-500/10 text-red-500 hover:bg-red-500/20 px-1.5 py-0.5 rounded text-[10px] transition-colors disabled:opacity-50"
                                                    title={t('accountManager.deleteAllSessions')}
                                                >
                                                    {deletingSessions && deletingSessions[id] ? (
                                                        <span className="animate-spin">⟳</span>
                                                    ) : (
                                                        <FolderX className="w-3 h-3" />
                                                    )}
                                                </button>
                                            )}
                                            {acc.proxy_id && (
                                                <span className="font-mono bg-amber-500/10 text-amber-500 px-1.5 py-0.5 rounded text-[10px]">
                                                    {t('accountManager.proxyBadge', { name: assignedProxy ? (assignedProxy.name || `${assignedProxy.host}:${assignedProxy.port}`) : acc.proxy_id })}
                                                </span>
                                            )}
                                        </div>
                                    </div>
                                </div>
                                <div className="flex items-center gap-2 self-start lg:self-auto ml-5 lg:ml-0">
                                    <button
                                        onClick={() => onToggleEnabled && onToggleEnabled(id, !isEnabled)}
                                        disabled={togglingEnabled[id] || !id}
                                        className={clsx(
                                            "p-1 lg:p-1.5 rounded-md transition-colors disabled:opacity-40 disabled:cursor-not-allowed",
                                            isEnabled
                                                ? "text-emerald-500 hover:text-emerald-600 hover:bg-emerald-500/10"
                                                : "text-muted-foreground hover:text-amber-500 hover:bg-amber-500/10"
                                        )}
                                        title={isEnabled ? t('accountManager.disableAccount') : t('accountManager.enableAccount')}
                                    >
                                        {isEnabled ? <ToggleRight className="w-4 h-4" /> : <ToggleLeft className="w-4 h-4" />}
                                    </button>
                                    <select
                                        value={acc.proxy_id || ''}
                                        onChange={e => onUpdateAccountProxy(id, e.target.value)}
                                        disabled={updatingProxy?.[id]}
                                        className="max-w-[180px] px-2.5 py-1.5 text-[10px] lg:text-xs bg-secondary border border-border rounded-md focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50"
                                    >
                                        <option value="">{t('accountManager.proxyNone')}</option>
                                        {proxies.map(proxy => (
                                            <option key={proxy.id} value={proxy.id}>
                                                {proxy.name || `${proxy.host}:${proxy.port}`}
                                            </option>
                                        ))}
                                    </select>
                                    <button
                                        onClick={() => onEditAccount(acc)}
                                        disabled={!id}
                                        className="p-1 lg:p-1.5 text-muted-foreground hover:text-primary hover:bg-primary/10 rounded-md transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
                                        title={id ? t('accountManager.editAccountTitle') : t('accountManager.invalidIdentifier')}
                                    >
                                        <Pencil className="w-3.5 h-3.5 lg:w-4 lg:h-4" />
                                    </button>
                                    <button
                                        onClick={() => onTestAccount(id)}
                                        disabled={testing[id]}
                                        className="px-2 lg:px-3 py-1 lg:py-1.5 text-[10px] lg:text-xs font-medium border border-border rounded-md hover:bg-secondary transition-colors disabled:opacity-50"
                                    >
                                        {testing[id] ? t('actions.testing') : t('actions.test')}
                                    </button>
                                    <button
                                        onClick={() => onDeleteAccount(id)}
                                        className="p-1 lg:p-1.5 text-muted-foreground hover:text-destructive hover:bg-destructive/10 rounded-md transition-colors"
                                    >
                                        <Trash2 className="w-3.5 h-3.5 lg:w-4 lg:h-4" />
                                    </button>
                                </div>
                            </div>
                        )
                    })
                ) : (
                    <div className="p-8 text-center text-muted-foreground">{searchQuery || hasActiveFilter ? t('accountManager.searchNoResults') : t('accountManager.noAccounts')}</div>
                )}
            </div>

            {totalPages > 1 && (
                <div className="p-4 border-t border-border flex items-center justify-between">
                    <div className="flex items-center gap-3">
                        <div className="text-sm text-muted-foreground">
                            {t('accountManager.pageInfo', { current: page, total: totalPages, count: totalAccounts })}
                        </div>
                        <select
                            value={pageSize}
                            onChange={e => onPageSizeChange(Number(e.target.value))}
                            className="text-sm border border-border rounded-md px-2 py-1 bg-background text-foreground"
                        >
                            {[10, 20, 50, 100, 500, 1000, 2000, 5000].map(s => (
                                <option key={s} value={s}>{s}</option>
                            ))}
                        </select>
                    </div>
                    <div className="flex items-center gap-2">
                        <button
                            onClick={onPrevPage}
                            disabled={page <= 1 || loadingAccounts}
                            className="p-2 border border-border rounded-md hover:bg-secondary transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                        >
                            <ChevronLeft className="w-4 h-4" />
                        </button>
                        <span className="text-sm font-medium px-2">{page} / {totalPages}</span>
                        <button
                            onClick={onNextPage}
                            disabled={page >= totalPages || loadingAccounts}
                            className="p-2 border border-border rounded-md hover:bg-secondary transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                        >
                            <ChevronRight className="w-4 h-4" />
                        </button>
                    </div>
                </div>
            )}
        </div>
    )
}
