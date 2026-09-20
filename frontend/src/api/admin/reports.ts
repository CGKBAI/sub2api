/**
 * Admin Reports API endpoints (user daily/weekly reports)
 */

import { apiClient } from '../client'
import type { BasePaginationResponse } from '@/types'

/**
 * Generate-all runs one LLM call per active user sequentially on the backend,
 * so a full pass can easily take several minutes. The global 30s axios timeout
 * would abort the request mid-batch (surfacing as "Network error"), while the
 * backend keeps processing on a detached context. Align the client timeout with
 * the backend's 30-minute batch budget; single-user generation caps one LLM
 * call at 120s server-side, so allow 180s.
 */
const GENERATE_ALL_TIMEOUT_MS = 30 * 60 * 1000
const GENERATE_TIMEOUT_MS = 3 * 60 * 1000

export interface ReportStats {
  requests: number
  input_tokens: number
  output_tokens: number
  total_cost: number
  models: Record<string, number>
  model_tokens: Record<string, number>
  hourly: Record<string, number>
  prompt_count: number
}

export type ReportType = 'daily' | 'weekly' | 'monthly'

export interface Report {
  id: number
  user_id: number
  username: string
  type: ReportType
  period_start: string
  period_end: string
  stats: ReportStats
  ai_summary: string
  status: 'pending' | 'done' | 'failed'
  error?: string
  pushed_at?: string
  last_push_error?: string
  created_at: string
  updated_at: string
}

export interface ReportLLMConfig {
  enabled: boolean
  base_url: string
  api_key: string
  model: string
  max_prompts: number
  prompt_truncate_chars: number
  daily_schedule: string
  weekly_schedule: string
  monthly_schedule: string
  feishu_enabled: boolean
  feishu_webhook_url: string
  feishu_secret: string
  feishu_push_daily: boolean
  feishu_push_weekly: boolean
  feishu_push_monthly: boolean
}

export interface ReportListFilters {
  type?: ReportType
  user_id?: number
  date?: string
}

export async function list(
  page: number = 1,
  pageSize: number = 50,
  filters?: ReportListFilters,
  options?: { signal?: AbortSignal }
): Promise<BasePaginationResponse<Report>> {
  const { data } = await apiClient.get<BasePaginationResponse<Report>>('/admin/reports', {
    params: { page, page_size: pageSize, ...filters },
    signal: options?.signal
  })
  return data
}

export async function getById(id: number): Promise<Report> {
  const { data } = await apiClient.get<Report>(`/admin/reports/${id}`)
  return data
}

export async function generate(request: {
  type: ReportType
  user_id: number
  date?: string
}): Promise<Report> {
  const { data } = await apiClient.post<Report>('/admin/reports/generate', request, {
    timeout: GENERATE_TIMEOUT_MS
  })
  return data
}

export async function generateAll(request: {
  type: ReportType
  date?: string
}): Promise<{ generated: number; error: string }> {
  const { data } = await apiClient.post<{ generated: number; error: string }>(
    '/admin/reports/generate-all',
    request,
    { timeout: GENERATE_ALL_TIMEOUT_MS }
  )
  return data
}

export async function getConfig(): Promise<ReportLLMConfig> {
  const { data } = await apiClient.get<ReportLLMConfig>('/admin/reports/config')
  return data
}

export async function updateConfig(
  request: Partial<Omit<ReportLLMConfig, 'api_key' | 'feishu_webhook_url' | 'feishu_secret'>> & {
    api_key?: string
    feishu_webhook_url?: string
    feishu_secret?: string
  }
): Promise<ReportLLMConfig> {
  const { data } = await apiClient.put<ReportLLMConfig>('/admin/reports/config', request)
  return data
}

export async function pushToFeishu(id: number): Promise<Report> {
  const { data } = await apiClient.post<Report>(`/admin/reports/${id}/push`)
  return data
}

const reportsAPI = { list, getById, generate, generateAll, getConfig, updateConfig, pushToFeishu }
export default reportsAPI
