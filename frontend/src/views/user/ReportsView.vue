<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import userReportsAPI, { type Report, type ReportType } from '@/api/reports'
import { useAppStore } from '@/stores/app'
import { EmptyState, LoadingSpinner } from '@/components/common'

marked.setOptions({ breaks: true, gfm: true })

const { t } = useI18n()
const appStore = useAppStore()

const activeTab = ref<ReportType>('daily')
const date = ref(todayStr())
const loading = ref(false)
const reports = ref<Report[]>([])

function todayStr(): string {
  const d = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

async function load() {
  loading.value = true
  try {
    const res = await userReportsAPI.listMyReports(1, 50, {
      type: activeTab.value,
      date: date.value || undefined
    })
    reports.value = res.items ?? []
  } catch (error: any) {
    if (error?.code === 'ERR_CANCELED' || error?.name === 'AbortError') return
    appStore.showError(error?.message || t('admin.reports.empty'))
  } finally {
    loading.value = false
  }
}

watch([activeTab, date], load)
onMounted(load)

function renderMarkdown(content: string): string {
  if (!content) return ''
  return DOMPurify.sanitize(marked.parse(content) as string)
}

function periodLabel(report: Report): string {
  const start = new Date(report.period_start)
  const end = new Date(report.period_end)
  const fmt = (d: Date) =>
    `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
  return `${fmt(start)} ~ ${fmt(end)}`
}

function statusBadgeClass(status: string): string {
  switch (status) {
    case 'done':
      return 'badge-success'
    case 'failed':
      return 'badge-danger'
    default:
      return 'badge-warning'
  }
}

const modelEntries = (report: Report) =>
  Object.entries(report.stats?.models ?? {})
    .sort((a, b) => b[1] - a[1])
    .slice(0, 6)
</script>

<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-2xl font-bold text-gray-900 dark:text-white">
        {{ t('admin.reports.title') }}
      </h1>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.reports.description') }}
      </p>
    </div>

    <!-- Tab + 日期 -->
    <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div class="flex rounded-lg border border-gray-200 dark:border-gray-700 p-0.5">
        <button
          v-for="tp in ['daily', 'weekly', 'monthly'] as const"
          :key="tp"
          class="rounded-md px-4 py-1.5 text-sm font-medium transition-colors"
          :class="
            activeTab === tp
              ? 'bg-indigo-600 text-white'
              : 'text-gray-600 dark:text-gray-300 hover:bg-gray-100 dark:hover:bg-gray-700'
          "
          @click="activeTab = tp"
        >
          {{ t(`admin.reports.tabs.${tp}`) }}
        </button>
      </div>
      <div class="flex items-center gap-2">
        <label class="text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.reports.filters.date') }}
        </label>
        <input v-model="date" type="date" class="input text-sm" />
        <button class="btn btn-secondary text-sm" :disabled="loading" @click="load">
          {{ t('admin.reports.filters.refresh') }}
        </button>
      </div>
    </div>

    <LoadingSpinner v-if="loading" />
    <EmptyState v-else-if="reports.length === 0" :description="t('admin.reports.empty')" />
    <div v-else class="grid grid-cols-1 gap-4">
      <div
        v-for="report in reports"
        :key="report.id"
        class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm dark:border-gray-700 dark:bg-gray-800"
      >
        <div class="flex items-center gap-2">
          <span class="font-semibold text-gray-900 dark:text-white">
            {{ t(`admin.reports.tabs.${report.type}`) }}
          </span>
          <span class="badge" :class="statusBadgeClass(report.status)">
            {{ t(`admin.reports.status.${report.status}`) }}
          </span>
          <span class="ml-auto text-xs text-gray-500 dark:text-gray-400">
            {{ periodLabel(report) }}
          </span>
        </div>

        <div class="mt-3 grid grid-cols-2 gap-2 text-sm sm:grid-cols-4">
          <div>
            <div class="text-xs text-gray-400">{{ t('admin.reports.stats.requests') }}</div>
            <div class="font-medium text-gray-900 dark:text-white">{{ report.stats?.requests ?? 0 }}</div>
          </div>
          <div>
            <div class="text-xs text-gray-400">{{ t('admin.reports.stats.outputTokens') }}</div>
            <div class="font-medium text-gray-900 dark:text-white">
              {{ (report.stats?.output_tokens ?? 0).toLocaleString() }}
            </div>
          </div>
          <div>
            <div class="text-xs text-gray-400">{{ t('admin.reports.stats.inputTokens') }}</div>
            <div class="font-medium text-gray-900 dark:text-white">
              {{ (report.stats?.input_tokens ?? 0).toLocaleString() }}
            </div>
          </div>
          <div>
            <div class="text-xs text-gray-400">{{ t('admin.reports.stats.cost') }}</div>
            <div class="font-medium text-gray-900 dark:text-white">
              ${{ (report.stats?.total_cost ?? 0).toFixed(4) }}
            </div>
          </div>
        </div>

        <div v-if="modelEntries(report).length" class="mt-3 flex flex-wrap gap-1.5">
          <span
            v-for="[model, count] in modelEntries(report)"
            :key="model"
            class="rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-600 dark:bg-gray-700 dark:text-gray-300"
          >
            {{ model }} × {{ count }}
          </span>
        </div>

        <div class="mt-3">
          <div class="mb-1 text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('admin.reports.summary.title') }}
          </div>
          <div
            v-if="report.ai_summary"
            class="prose prose-sm max-w-none text-sm text-gray-700 dark:text-gray-200"
            v-html="renderMarkdown(report.ai_summary)"
          />
          <div v-else class="text-xs text-gray-400">
            {{ t('admin.reports.summary.empty') }}
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
