import { useState } from 'react'
import { useI18n } from '../../i18n'
import { useAccountsData } from './useAccountsData'
import { useAccountActions } from './useAccountActions'
import QueueCards from './QueueCards'
import ApiKeysPanel from './ApiKeysPanel'
import AccountsTable from './AccountsTable'
import AddKeyModal from './AddKeyModal'
import AddAccountModal from './AddAccountModal'
import EditAccountModal from './EditAccountModal'

export default function AccountManagerContainer({ config, onRefresh, onMessage, authFetch }) {
    const { t } = useI18n()
    const apiFetch = authFetch || fetch

    const {
        queueStatus,
        keysExpanded,
        setKeysExpanded,
        accounts,
        page,
        pageSize,
        totalPages,
        totalAccounts,
        loadingAccounts,
        quotaWindowHours,
        fetchAccounts,
        changePageSize,
        resolveAccountIdentifier,
        searchQuery,
        handleSearchChange,
        sortKey,
        sortOrder,
        changeSort,
        filters,
        changeFilter,
        resetFilters,
    } = useAccountsData({ apiFetch })

    const {
        showAddKey,
        openAddKey,
        openEditKey,
        closeKeyModal,
        editingKey,
        showAddAccount,
        openAddAccount,
        closeAddAccount,
        showEditAccount,
        editingAccount,
        editAccount,
        setEditAccount,
        openEditAccount,
        closeEditAccount,
        newKey,
        setNewKey,
        copiedKey,
        setCopiedKey,
        newAccount,
        setNewAccount,
        loading,
        testing,
        testingAll,
        batchProgress,
        sessionCounts,
        deletingSessions,
        updatingProxy,
        togglingEnabled,
        addKey,
        deleteKey,
        addAccount,
        updateAccount,
        deleteAccount,
        testAccount,
        testAllAccounts,
        deleteAllSessions,
        updateAccountProxy,
        toggleEnabled,
        batchDeleteAccounts,
        batchUpdateStatus,
        batchUpdateProxy,
        batchOperating,
    } = useAccountActions({
        apiFetch,
        t,
        onMessage,
        onRefresh,
        config,
        fetchAccounts,
        resolveAccountIdentifier,
    })

    const [quotaWindowSaving, setQuotaWindowSaving] = useState(false)

    // Adjusts the risk-control quota window straight from the account list, so
    // operators can tune it while looking at the per-account window usage. It
    // reuses the settings endpoint, which accepts partial runtime updates.
    const updateQuotaWindow = async (hours) => {
        const value = Number(hours)
        if (!Number.isFinite(value) || value < 0 || value > 168) {
            onMessage('error', t('accountManager.quotaWindowInvalid'))
            return
        }
        setQuotaWindowSaving(true)
        try {
            const res = await apiFetch('/admin/settings', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ runtime: { quota_window_hours: value } }),
            })
            const data = await res.json().catch(() => ({}))
            if (!res.ok) {
                onMessage('error', data.detail || t('messages.requestFailed'))
                return
            }
            onMessage('success', t('accountManager.quotaWindowUpdated'))
            fetchAccounts()
            onRefresh()
        } catch (_e) {
            onMessage('error', t('messages.networkError'))
        } finally {
            setQuotaWindowSaving(false)
        }
    }

    return (
        <div className="space-y-6">
            {Boolean(config?.env_source_present) && (
                <div className={`rounded-xl border px-4 py-3 text-sm ${
                    config?.env_writeback_enabled
                        ? (config?.env_backed ? 'border-amber-500/30 bg-amber-500/10 text-amber-600' : 'border-emerald-500/30 bg-emerald-500/10 text-emerald-600')
                        : 'border-amber-500/30 bg-amber-500/10 text-amber-600'
                }`}>
                    <p className="font-medium">
                        {config?.env_writeback_enabled
                            ? (config?.env_backed
                                ? t('accountManager.envModeWritebackPendingTitle')
                                : t('accountManager.envModeWritebackActiveTitle'))
                            : t('accountManager.envModeRiskTitle')}
                    </p>
                    <p className="mt-1 text-xs opacity-90">
                        {config?.env_writeback_enabled
                            ? t('accountManager.envModeWritebackDesc', { path: config?.config_path || 'config.json' })
                            : t('accountManager.envModeRiskDesc')}
                    </p>
                </div>
            )}

            <QueueCards queueStatus={queueStatus} t={t} />

            <ApiKeysPanel
                t={t}
                config={config}
                keysExpanded={keysExpanded}
                setKeysExpanded={setKeysExpanded}
                onAddKey={openAddKey}
                onEditKey={openEditKey}
                copiedKey={copiedKey}
                setCopiedKey={setCopiedKey}
                onDeleteKey={deleteKey}
            />

            <AccountsTable
                t={t}
                accounts={accounts}
                loadingAccounts={loadingAccounts}
                testing={testing}
                testingAll={testingAll}
                batchProgress={batchProgress}
                sessionCounts={sessionCounts}
                deletingSessions={deletingSessions}
                updatingProxy={updatingProxy}
                totalAccounts={totalAccounts}
                quotaWindowHours={quotaWindowHours}
                page={page}
                pageSize={pageSize}
                totalPages={totalPages}
                resolveAccountIdentifier={resolveAccountIdentifier}
                proxies={config?.proxies || []}
                onTestAll={testAllAccounts}
                onShowAddAccount={openAddAccount}
                onEditAccount={openEditAccount}
                onTestAccount={testAccount}
                onDeleteAccount={deleteAccount}
                onDeleteAllSessions={deleteAllSessions}
                onUpdateAccountProxy={updateAccountProxy}
                onToggleEnabled={toggleEnabled}
                togglingEnabled={togglingEnabled}
                onPrevPage={() => fetchAccounts(page - 1)}
                onNextPage={() => fetchAccounts(page + 1)}
                onPageSizeChange={changePageSize}
                searchQuery={searchQuery}
                onSearchChange={handleSearchChange}
                filters={filters}
                onFilterChange={changeFilter}
                onResetFilters={resetFilters}
                sortKey={sortKey}
                sortOrder={sortOrder}
                onSortChange={changeSort}
                onBatchDelete={batchDeleteAccounts}
                onBatchEnable={ids => batchUpdateStatus(ids, true)}
                onBatchDisable={ids => batchUpdateStatus(ids, false)}
                onBatchProxy={batchUpdateProxy}
                batchOperating={batchOperating}
                onUpdateQuotaWindow={updateQuotaWindow}
                quotaWindowSaving={quotaWindowSaving}
                envBacked={Boolean(config?.env_backed)}
            />

            <AddKeyModal
                show={showAddKey}
                t={t}
                editingKey={editingKey}
                newKey={newKey}
                setNewKey={setNewKey}
                loading={loading}
                onClose={closeKeyModal}
                onAdd={addKey}
            />

            <AddAccountModal
                show={showAddAccount}
                t={t}
                newAccount={newAccount}
                setNewAccount={setNewAccount}
                loading={loading}
                onClose={closeAddAccount}
                onAdd={addAccount}
            />

            <EditAccountModal
                show={showEditAccount}
                t={t}
                editingAccount={editingAccount}
                editAccount={editAccount}
                setEditAccount={setEditAccount}
                loading={loading}
                onClose={closeEditAccount}
                onSave={updateAccount}
            />
        </div>
    )
}
