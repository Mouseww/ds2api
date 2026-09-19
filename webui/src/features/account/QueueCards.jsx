import { CheckCircle2, Server, ShieldCheck, AlertTriangle, Clock, Activity } from 'lucide-react'

export default function QueueCards({ queueStatus, t }) {
    if (!queueStatus) {
        return null
    }

    const activePoolSize = Number(queueStatus.active_pool_size ?? 0)
    const activePoolLabel = activePoolSize > 0 ? String(activePoolSize) : t('accountManager.activePoolSizeUnlimited')

    return (
        <div className="space-y-4">
            <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-5 gap-4">
                <div className="bg-card border border-border rounded-xl p-4 flex flex-col justify-between shadow-sm relative overflow-hidden group">
                    <div className="absolute right-0 top-0 p-4 opacity-5 group-hover:opacity-10 transition-opacity">
                        <CheckCircle2 className="w-16 h-16" />
                    </div>
                    <p className="text-xs font-medium text-muted-foreground uppercase tracking-widest">{t('accountManager.available')}</p>
                    <div className="mt-2 flex items-baseline gap-2">
                        <span className="text-3xl font-bold text-foreground">{queueStatus.available}</span>
                        <span className="text-xs text-muted-foreground">{t('accountManager.accountsUnit')}</span>
                    </div>
                </div>
                <div className="bg-card border border-border rounded-xl p-4 flex flex-col justify-between shadow-sm relative overflow-hidden group">
                    <div className="absolute right-0 top-0 p-4 opacity-5 group-hover:opacity-10 transition-opacity">
                        <Server className="w-16 h-16" />
                    </div>
                    <p className="text-xs font-medium text-muted-foreground uppercase tracking-widest">{t('accountManager.inUse')}</p>
                    <div className="mt-2 flex items-baseline gap-2">
                        <span className="text-3xl font-bold text-foreground">{queueStatus.in_use}</span>
                        <span className="text-xs text-muted-foreground">{t('accountManager.threadsUnit')}</span>
                    </div>
                </div>
                <div className="bg-card border border-border rounded-xl p-4 flex flex-col justify-between shadow-sm relative overflow-hidden group">
                    <div className="absolute right-0 top-0 p-4 opacity-5 group-hover:opacity-10 transition-opacity">
                        <Activity className="w-16 h-16" />
                    </div>
                    <p className="text-xs font-medium text-muted-foreground uppercase tracking-widest">{t('accountManager.activePool')}</p>
                    <div className="mt-2 flex items-baseline gap-2">
                        <span className="text-3xl font-bold text-foreground">{Number(queueStatus.active_pool_count ?? queueStatus.total)}</span>
                        <span className="text-xs text-muted-foreground">/ {activePoolLabel}</span>
                    </div>
                </div>
                <div className="bg-card border border-border rounded-xl p-4 flex flex-col justify-between shadow-sm relative overflow-hidden group">
                    <div className="absolute right-0 top-0 p-4 opacity-5 group-hover:opacity-10 transition-opacity">
                        <Clock className="w-16 h-16" />
                    </div>
                    <p className="text-xs font-medium text-muted-foreground uppercase tracking-widest">{t('accountManager.standbyPool')}</p>
                    <div className="mt-2 flex items-baseline gap-2">
                        <span className="text-3xl font-bold text-foreground">{Number(queueStatus.standby_count ?? 0)}</span>
                        <span className="text-xs text-muted-foreground">{t('accountManager.accountsUnit')}</span>
                    </div>
                </div>
                <div className="bg-card border border-border rounded-xl p-4 flex flex-col justify-between shadow-sm relative overflow-hidden group">
                    <div className="absolute right-0 top-0 p-4 opacity-5 group-hover:opacity-10 transition-opacity">
                        <AlertTriangle className="w-16 h-16" />
                    </div>
                    <p className="text-xs font-medium text-muted-foreground uppercase tracking-widest">{t('accountManager.bannedCount')}</p>
                    <div className="mt-2 flex items-baseline gap-2">
                        <span className="text-3xl font-bold text-red-600 dark:text-red-400">{Number(queueStatus.banned_count ?? 0)}</span>
                        <span className="text-xs text-muted-foreground">{t('accountManager.accountsUnit')}</span>
                    </div>
                </div>
            </div>
            <div className="flex gap-2 text-xs text-muted-foreground">
                <span>{t('accountManager.totalPool')}: {queueStatus.total}</span>
                {Number(queueStatus.disabled_count ?? 0) > 0 && (
                    <span>&middot; {t('accountManager.disabledCount')}: {queueStatus.disabled_count}</span>
                )}
            </div>
        </div>
    )
}