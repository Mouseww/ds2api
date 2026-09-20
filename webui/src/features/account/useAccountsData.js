import { useEffect, useState } from 'react'

export const ACCOUNT_FILTER_KEYS = ['enabled', 'test_status', 'banned', 'daily_limited', 'has_proxy', 'in_pool']

export const DEFAULT_ACCOUNT_FILTERS = {
    enabled: 'all',
    test_status: 'all',
    banned: 'all',
    daily_limited: 'all',
    has_proxy: 'all',
    in_pool: 'all',
}

export function useAccountsData({ apiFetch }) {
    const [queueStatus, setQueueStatus] = useState(null)
    const [keysExpanded, setKeysExpanded] = useState(false)

    const [accounts, setAccounts] = useState([])
    const [page, setPage] = useState(1)
    const [pageSize, setPageSize] = useState(10)
    const [totalPages, setTotalPages] = useState(1)
    const [totalAccounts, setTotalAccounts] = useState(0)
    const [loadingAccounts, setLoadingAccounts] = useState(false)
    const [quotaWindowHours, setQuotaWindowHours] = useState(24)

    const resolveAccountIdentifier = (acc) => {
        if (!acc || typeof acc !== 'object') return ''
        return String(acc.identifier || acc.email || acc.mobile || '').trim()
    }

    const [searchQuery, setSearchQuery] = useState('')
    const [sortKey, setSortKey] = useState('default')
    const [sortOrder, setSortOrder] = useState('desc')
    const [filters, setFilters] = useState({ ...DEFAULT_ACCOUNT_FILTERS })

    const buildAccountsQuery = (targetPage, targetPageSize, targetQuery, targetSortKey, targetSortOrder, targetFilters) => {
        let url = `/admin/accounts?page=${targetPage}&page_size=${targetPageSize}`
        if (targetQuery.trim()) url += `&q=${encodeURIComponent(targetQuery.trim())}`
        if (targetSortKey && targetSortKey !== 'default') url += `&sort=${encodeURIComponent(targetSortKey)}`
        if (targetSortKey && targetSortKey !== 'default' && targetSortOrder) url += `&order=${encodeURIComponent(targetSortOrder)}`
        for (const key of ACCOUNT_FILTER_KEYS) {
            const value = targetFilters?.[key]
            if (value && value !== 'all') url += `&${key}=${encodeURIComponent(value)}`
        }
        return url
    }

    const fetchAccounts = async (
        targetPage = page,
        targetPageSize = pageSize,
        targetQuery = searchQuery,
        targetSortKey = sortKey,
        targetSortOrder = sortOrder,
        targetFilters = filters,
    ) => {
        setLoadingAccounts(true)
        try {
            const url = buildAccountsQuery(targetPage, targetPageSize, targetQuery, targetSortKey, targetSortOrder, targetFilters)
            const res = await apiFetch(url)
            if (res.ok) {
                const data = await res.json()
                setAccounts(data.items || [])
                setTotalPages(data.total_pages || 1)
                setTotalAccounts(data.total || 0)
                setPage(data.page || 1)
                setQuotaWindowHours(Number(data.quota_window_hours || 24))
            }
        } catch (e) {
            console.error('Failed to fetch accounts:', e)
        } finally {
            setLoadingAccounts(false)
        }
    }

    const changePageSize = (newSize) => {
        setPageSize(newSize)
        fetchAccounts(1, newSize)
    }

    const handleSearchChange = (query) => {
        setSearchQuery(query)
        fetchAccounts(1, pageSize, query)
    }

    const changeSort = (newSortKey, newSortOrder) => {
        setSortKey(newSortKey)
        setSortOrder(newSortOrder)
        fetchAccounts(1, pageSize, searchQuery, newSortKey, newSortOrder)
    }

    const changeFilter = (key, value) => {
        const nextFilters = { ...filters, [key]: value }
        setFilters(nextFilters)
        fetchAccounts(1, pageSize, searchQuery, sortKey, sortOrder, nextFilters)
    }

    const resetFilters = () => {
        setFilters({ ...DEFAULT_ACCOUNT_FILTERS })
        setSearchQuery('')
        fetchAccounts(1, pageSize, '', sortKey, sortOrder, { ...DEFAULT_ACCOUNT_FILTERS })
    }

    const fetchQueueStatus = async () => {
        try {
            const res = await apiFetch('/admin/queue/status')
            if (res.ok) {
                const data = await res.json()
                setQueueStatus(data)
            }
        } catch (e) {
            console.error('Failed to fetch queue status:', e)
        }
    }

    useEffect(() => {
        fetchAccounts()
        fetchQueueStatus()
        const interval = setInterval(fetchQueueStatus, 5000)
        return () => clearInterval(interval)
    }, [])

    return {
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
    }
}
