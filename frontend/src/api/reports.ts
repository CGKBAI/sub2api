/**
 * User Reports API endpoints (own daily/weekly reports only)
 */

import { apiClient } from './client'
import type { BasePaginationResponse } from '@/types'
import type { Report, ReportType } from './admin/reports'

export type { Report, ReportStats, ReportType } from './admin/reports'

export interface UserReportListFilters {
  type?: ReportType
  date?: string
}

export async function listMyReports(
  page: number = 1,
  pageSize: number = 50,
  filters?: UserReportListFilters,
  options?: { signal?: AbortSignal }
): Promise<BasePaginationResponse<Report>> {
  const { data } = await apiClient.get<BasePaginationResponse<Report>>('/user/reports', {
    params: { page, page_size: pageSize, ...filters },
    signal: options?.signal
  })
  return data
}

export async function pushMyReportToFeishu(id: number): Promise<Report> {
  const { data } = await apiClient.post<Report>(`/user/reports/${id}/push`)
  return data
}

const userReportsAPI = { listMyReports, pushMyReportToFeishu }
export default userReportsAPI
