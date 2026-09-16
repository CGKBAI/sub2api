/**
 * Admin Reports API endpoints (user daily/weekly reports)
 */

import { apiClient } from '../client'
import type { BasePaginationResponse } from '@/types'

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

export interface Report {
  id: number
  user_id: number
  username: string
  type: 'daily' | 'weekly'
  period_start: string
  period_end: string
  stats: ReportStats
  ai_summary: string
  status: 'pending' | 'done' | 'failed'
  error?: string
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
}

export interface ReportListFilters {
  type?: 'daily' | 'weekly'
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
  type: 'daily' | 'weekly'
  user_id: number
  date?: string
}): Promise<Report> {
  const { data } = await apiClient.post<Report>('/admin/reports/generate', request)
  return data
}

export async function generateAll(request: {
  type: 'daily' | 'weekly'
  date?: string
}): Promise<{ generated: number; error: string }> {
  const { data } = await apiClient.post<{ generated: number; error: string }>(
    '/admin/reports/generate-all',
    request
  )
  return data
}

export async function getConfig(): Promise<ReportLLMConfig> {
  const { data } = await apiClient.get<ReportLLMConfig>('/admin/reports/config')
  return data
}

export async function updateConfig(
  request: Partial<Omit<ReportLLMConfig, 'api_key'>> & { api_key?: string }
): Promise<ReportLLMConfig> {
  const { data } = await apiClient.put<ReportLLMConfig>('/admin/reports/config', request)
  return data
}

const reportsAPI = { list, getById, generate, generateAll, getConfig, updateConfig }
export default reportsAPI
