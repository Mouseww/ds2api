export default function RuntimeSection({ t, form, setForm }) {
    return (
        <div className="bg-card border border-border rounded-xl p-5 space-y-4">
            <h3 className="font-semibold">{t('settings.runtimeTitle')}</h3>
            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.activePoolSize')}</span>
                    <input
                        type="number"
                        min={0}
                        value={form.runtime.active_pool_size}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            runtime: { ...prev.runtime, active_pool_size: Number(e.target.value || 0) },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                    <span className="text-[10px] text-muted-foreground">{t('settings.activePoolSizeHelp')}</span>
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.accountMaxInflight')}</span>
                    <input
                        type="number"
                        min={1}
                        value={form.runtime.account_max_inflight}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            runtime: { ...prev.runtime, account_max_inflight: Number(e.target.value || 1) },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.accountMaxQueue')}</span>
                    <input
                        type="number"
                        min={1}
                        value={form.runtime.account_max_queue}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            runtime: { ...prev.runtime, account_max_queue: Number(e.target.value || 1) },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.globalMaxInflight')}</span>
                    <input
                        type="number"
                        min={1}
                        value={form.runtime.global_max_inflight}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            runtime: { ...prev.runtime, global_max_inflight: Number(e.target.value || 1) },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.tokenRefreshIntervalHours')}</span>
                    <input
                        type="number"
                        min={1}
                        max={720}
                        step={1}
                        value={form.runtime.token_refresh_interval_hours}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            runtime: { ...prev.runtime, token_refresh_interval_hours: Number(e.target.value || 1) },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.quotaWindowHours')}</span>
                    <input
                        type="number"
                        min={0}
                        max={168}
                        step={1}
                        value={form.runtime.quota_window_hours}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            runtime: { ...prev.runtime, quota_window_hours: Number(e.target.value || 0) },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                    <span className="text-[10px] text-muted-foreground">{t('settings.quotaWindowHoursHelp')}</span>
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.dailyTokenLimitM', { hours: form.runtime.quota_window_hours || 24 })}</span>
                    <input
                        type="number"
                        min={0}
                        value={form.runtime.daily_token_limit_m}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            runtime: { ...prev.runtime, daily_token_limit_m: Number(e.target.value || 0) },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                    <span className="text-[10px] text-muted-foreground">{t('settings.dailyTokenLimitMHelp', { hours: form.runtime.quota_window_hours || 24 })}</span>
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.dailyRequestLimit', { hours: form.runtime.quota_window_hours || 24 })}</span>
                    <input
                        type="number"
                        min={0}
                        value={form.runtime.daily_request_limit}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            runtime: { ...prev.runtime, daily_request_limit: Number(e.target.value || 0) },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                    <span className="text-[10px] text-muted-foreground">{t('settings.dailyRequestLimitHelp', { hours: form.runtime.quota_window_hours || 24 })}</span>
                </label>
            </div>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4 pt-2 border-t border-border">
                <label className="flex items-center gap-3 text-sm cursor-pointer">
                    <input
                        type="checkbox"
                        checked={form.runtime.auto_continue_fix}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            runtime: { ...prev.runtime, auto_continue_fix: e.target.checked },
                        }))}
                        className="h-4 w-4 rounded border-border bg-background"
                    />
                    <div className="space-y-1">
                        <span>{t('settings.autoContinueFix')}</span>
                        <span className="block text-[10px] text-muted-foreground">{t('settings.autoContinueFixHelp')}</span>
                    </div>
                </label>
                <label className="flex items-center gap-3 text-sm cursor-pointer">
                    <input
                        type="checkbox"
                        checked={form.runtime.strip_max_tokens}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            runtime: { ...prev.runtime, strip_max_tokens: e.target.checked },
                        }))}
                        className="h-4 w-4 rounded border-border bg-background"
                    />
                    <div className="space-y-1">
                        <span>{t('settings.stripMaxTokens')}</span>
                        <span className="block text-[10px] text-muted-foreground">{t('settings.stripMaxTokensHelp')}</span>
                    </div>
                </label>
            </div>
        </div>
    )
}