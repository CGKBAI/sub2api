<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import reportsAPI, { type Report, type ReportLLMConfig, type ReportType } from '@/api/admin/reports'
import { useAppStore } from '@/stores/app'
import { BaseDialog, EmptyState, LoadingSpinner } from '@/components/common'

marked.setOptions({ breaks: true, gfm: true })

const { t } = useI18n()
const appStore = useAppStore()

const activeTab = ref<ReportType>('daily')
const date = ref(todayStr())
const userIdFilter = ref('')
const loading = ref(false)
const reports = ref<Report[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 50

const generatingAll = ref(false)
const generatingUserId = ref<number | null>(null)

function todayStr(): string {
  const d = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

async function load() {
  loading.value = true
  try {
    const filters: Record<string, string | number> = { type: activeTab.value }
    if (date.value) filters.date = date.value
    const uid = Number(userIdFilter.value)
    if (userIdFilter.value && uid > 0) filters.user_id = uid
    const res = await reportsAPI.list(page.value, pageSize, filters as never)
    reports.value = res.items ?? []
    total.value = res.total ?? 0
  } catch (error: any) {
    if (error?.code === 'ERR_CANCELED' || error?.name === 'AbortError') return
    appStore.showError(error?.message || t('admin.reports.empty'))
  } finally {
    loading.value = false
  }
}

watch([activeTab, date, userIdFilter], () => {
  page.value = 1
  load()
})

onMounted(load)

async function generateAll() {
  generatingAll.value = true
  try {
    const res = await reportsAPI.generateAll({
      type: activeTab.value,
      date: date.value || undefined
    })
    appStore.showSuccess(`${res.generated}`)
    await load()
  } catch (error: any) {
    appStore.showError(error?.message || String(error))
  } finally {
    generatingAll.value = false
  }
}

async function generateFor(report: Report) {
  generatingUserId.value = report.user_id
  try {
    await reportsAPI.generate({
      type: activeTab.value,
      user_id: report.user_id,
      date: date.value || undefined
    })
    await load()
  } catch (error: any) {
    appStore.showError(error?.message || String(error))
  } finally {
    generatingUserId.value = null
  }
}

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

const modelEntries = (report: Report) =>
  Object.entries(report.stats?.models ?? {})
    .sort((a, b) => b[1] - a[1])
    .slice(0, 6)

// ===================== 设置弹窗 =====================
const settingsOpen = ref(false)
const savingConfig = ref(false)
const configForm = ref<ReportLLMConfig & { api_key: string }>({
  enabled: false,
  base_url: '',
  api_key: '',
  model: '',
  max_prompts: 30,
  prompt_truncate_chars: 500,
  daily_schedule: '0 20 * * *',
  weekly_schedule: '10 20 * * 5',
  monthly_schedule: '20 20 1 * *'
})

async function openSettings() {
  try {
    const cfg = await reportsAPI.getConfig()
    configForm.value = { ...cfg, api_key: '' }
    settingsOpen.value = true
  } catch (error: any) {
    appStore.showError(error?.message || String(error))
  }
}

async function saveConfig() {
  savingConfig.value = true
  try {
    const payload: Record<string, unknown> = { ...configForm.value }
    if (!configForm.value.api_key) delete payload.api_key
    await reportsAPI.updateConfig(payload as never)
    appStore.showSuccess(t('admin.reports.config.saved'))
    settingsOpen.value = false
  } catch (error: any) {
    appStore.showError(error?.message || String(error))
  } finally {
    savingConfig.value = false
  }
}

const inputClass =
  'block w-full rounded-md border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 text-gray-900 dark:text-white shadow-sm focus:border-indigo-500 focus:ring-indigo-500 text-sm'

const statusBadge = computed(() => (status: string) => {
  switch (status) {
    case 'done':
      return 'badge-success'
    case 'failed':
      return 'badge-danger'
    default:
      return 'badge-warning'
  }
})
</script>

<template>
  <div class="space-y-6">
    <!-- 页头 -->
    <div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
      <div>
        <h1 class="text-2xl font-bold text-gray-900 dark:text-white">
          {{ t('admin.reports.title') }}
        </h1>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.reports.description') }}
        </p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <button
          class="btn btn-secondary text-sm"
          :disabled="loading"
          @click="load"
        >
          {{ t('admin.reports.filters.refresh') }}
        </button>
        <button class="btn btn-secondary text-sm" @click="openSettings">
          {{ t('admin.reports.actions.settings') }}
        </button>
        <button
          class="btn btn-primary text-sm"
          :disabled="generatingAll"
          @click="generateAll"
        >
          {{ generatingAll ? t('admin.reports.actions.generating') : t('admin.reports.actions.generateAll') }}
        </button>
      </div>
    </div>

    <!-- Tab + 筛选 -->
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
      <div class="flex flex-wrap items-center gap-2">
        <label class="text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.reports.filters.date') }}
        </label>
        <input v-model="date" type="date" class="input text-sm" />
        <input
          v-model="userIdFilter"
          type="text"
          :placeholder="t('admin.reports.filters.userPlaceholder')"
          class="input w-40 text-sm"
        />
      </div>
    </div>

    <!-- 列表 -->
    <LoadingSpinner v-if="loading" />
    <EmptyState v-else-if="reports.length === 0" :description="t('admin.reports.empty')" />
    <div v-else class="grid grid-cols-1 gap-4 xl:grid-cols-2">
      <div
        v-for="report in reports"
        :key="report.id"
        class="rounded-xl border border-gray-200 bg-white p-5 shadow-sm dark:border-gray-700 dark:bg-gray-800"
      >
        <div class="flex items-start justify-between gap-3">
          <div>
            <div class="flex items-center gap-2">
              <span class="font-semibold text-gray-900 dark:text-white">
                {{ report.username || `#${report.user_id}` }}
              </span>
              <span class="badge" :class="statusBadge(report.status)">
                {{ t(`admin.reports.status.${report.status}`) }}
              </span>
            </div>
            <div class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ periodLabel(report) }}
            </div>
          </div>
          <button
            class="btn btn-secondary px-3 py-1 text-xs"
            :disabled="generatingUserId === report.user_id"
            @click="generateFor(report)"
          >
            {{
              generatingUserId === report.user_id
                ? t('admin.reports.actions.generating')
                : report.status === 'failed'
                  ? t('admin.reports.actions.retry')
                  : t('admin.reports.actions.generateFor')
            }}
          </button>
        </div>

        <!-- 统计行 -->
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

        <!-- 模型分布 -->
        <div v-if="modelEntries(report).length" class="mt-3 flex flex-wrap gap-1.5">
          <span
            v-for="[model, count] in modelEntries(report)"
            :key="model"
            class="rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-600 dark:bg-gray-700 dark:text-gray-300"
          >
            {{ model }} × {{ count }}
          </span>
        </div>

        <!-- 失败原因 -->
        <div
          v-if="report.status === 'failed' && report.error"
          class="mt-3 rounded-lg bg-red-50 p-2 text-xs text-red-600 dark:bg-red-900/20 dark:text-red-400"
        >
          {{ report.error }}
        </div>

        <!-- AI 摘要 -->
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

    <!-- 设置弹窗 -->
    <BaseDialog :show="settingsOpen" :title="t('admin.reports.config.title')" @close="settingsOpen = false">
      <div class="space-y-4">
        <p class="text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.reports.config.description') }}
        </p>
        <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
          <input v-model="configForm.enabled" type="checkbox" class="checkbox" />
          {{ t('admin.reports.config.enabled') }}
        </label>
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div>
            <label class="mb-1 block text-xs text-gray-500">{{ t('admin.reports.config.baseUrl') }}</label>
            <input v-model="configForm.base_url" type="text" :class="inputClass" :placeholder="t('admin.reports.config.baseUrlPlaceholder')" />
          </div>
          <div>
            <label class="mb-1 block text-xs text-gray-500">{{ t('admin.reports.config.model') }}</label>
            <input v-model="configForm.model" type="text" :class="inputClass" :placeholder="t('admin.reports.config.modelPlaceholder')" />
          </div>
          <div>
            <label class="mb-1 block text-xs text-gray-500">{{ t('admin.reports.config.apiKey') }}</label>
            <input v-model="configForm.api_key" type="password" :class="inputClass" :placeholder="t('admin.reports.config.apiKeyPlaceholder')" autocomplete="new-password" />
          </div>
          <div>
            <label class="mb-1 block text-xs text-gray-500">{{ t('admin.reports.config.maxPrompts') }}</label>
            <input v-model.number="configForm.max_prompts" type="number" min="1" max="200" :class="inputClass" />
          </div>
          <div>
            <label class="mb-1 block text-xs text-gray-500">{{ t('admin.reports.config.truncateChars') }}</label>
            <input v-model.number="configForm.prompt_truncate_chars" type="number" min="100" max="8000" :class="inputClass" />
          </div>
          <div>
            <label class="mb-1 block text-xs text-gray-500">{{ t('admin.reports.config.dailySchedule') }}</label>
            <input v-model="configForm.daily_schedule" type="text" :class="inputClass" />
          </div>
          <div>
            <label class="mb-1 block text-xs text-gray-500">{{ t('admin.reports.config.weeklySchedule') }}</label>
            <input v-model="configForm.weekly_schedule" type="text" :class="inputClass" />
          </div>
          <div>
            <label class="mb-1 block text-xs text-gray-500">{{ t('admin.reports.config.monthlySchedule') }}</label>
            <input v-model="configForm.monthly_schedule" type="text" :class="inputClass" />
          </div>
        </div>
        <div class="flex justify-end gap-2 pt-2">
          <button class="btn btn-secondary text-sm" @click="settingsOpen = false">
            {{ t('admin.reports.config.cancel') }}
          </button>
          <button class="btn btn-primary text-sm" :disabled="savingConfig" @click="saveConfig">
            {{ t('admin.reports.config.save') }}
          </button>
        </div>
      </div>
    </BaseDialog>
  </div>
</template>
