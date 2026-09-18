<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import userReportsAPI, { type Report, type ReportType } from '@/api/reports'
import { getProfile, updateProfile } from '@/api/user'
import { useAppStore } from '@/stores/app'
import { EmptyState, LoadingSpinner } from '@/components/common'

marked.setOptions({ breaks: true, gfm: true })

const { t } = useI18n()
const appStore = useAppStore()

const activeTab = ref<ReportType>('daily')
const dailyDate = ref(todayStr())
const loading = ref(false)
const reports = ref<Report[]>([])

// 周报按周选（近 12 周，value=周一日期）；月报按月选（value=该月 1 日）
const weekOptions = (() => {
  const pad = (n: number) => String(n).padStart(2, '0')
  const fmt = (d: Date) => `${pad(d.getMonth() + 1)}.${pad(d.getDate())}`
  const day = new Date().getDay()
  const monday = new Date()
  monday.setDate(monday.getDate() - (day === 0 ? 6 : day - 1))
  const out: { value: string; label: string }[] = []
  for (let i = 0; i < 12; i++) {
    const start = new Date(monday)
    start.setDate(monday.getDate() - 7 * i)
    const end = new Date(start)
    end.setDate(start.getDate() + 6)
    out.push({
      value: `${start.getFullYear()}-${pad(start.getMonth() + 1)}-${pad(start.getDate())}`,
      label: `${fmt(start)} ~ ${fmt(end)}`
    })
  }
  return out
})()
const weeklyDate = ref(weekOptions[0]?.value ?? todayStr())
const monthlyDate = ref(todayStr().slice(0, 7))
const activeDate = computed(() => {
  if (activeTab.value === 'weekly') return weeklyDate.value
  if (activeTab.value === 'monthly') return monthlyDate.value ? `${monthlyDate.value}-01` : ''
  return dailyDate.value
})

// 飞书推送
const reportPushEnabled = ref(true)
const savingPushToggle = ref(false)
const pushingReportId = ref<number | null>(null)

async function loadProfile() {
  try {
    const profile = await getProfile()
    reportPushEnabled.value = profile.report_push_enabled ?? true
  } catch {
    // profile 读取失败不阻塞报告页，开关保持默认参与
  }
}

async function togglePush(enabled: boolean) {
  const previous = reportPushEnabled.value
  reportPushEnabled.value = enabled
  savingPushToggle.value = true
  try {
    await updateProfile({ report_push_enabled: enabled })
  } catch (error: any) {
    reportPushEnabled.value = previous
    appStore.showError(error?.message || String(error))
  } finally {
    savingPushToggle.value = false
  }
}

async function pushToFeishu(report: Report) {
  pushingReportId.value = report.id
  try {
    await userReportsAPI.pushMyReportToFeishu(report.id)
    appStore.showSuccess(t('admin.reports.actions.pushSuccess'))
  } catch (error: any) {
    appStore.showError(error?.message || String(error))
  } finally {
    pushingReportId.value = null
  }
}

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
      date: activeDate.value || undefined
    })
    reports.value = res.items ?? []
  } catch (error: any) {
    if (error?.code === 'ERR_CANCELED' || error?.name === 'AbortError') return
    appStore.showError(error?.message || t('admin.reports.empty'))
  } finally {
    loading.value = false
  }
}

watch([activeTab, dailyDate, weeklyDate, monthlyDate], load)
onMounted(() => {
  load()
  loadProfile()
})

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
      <label class="mt-2 flex w-fit items-center gap-2 text-sm text-gray-700 dark:text-gray-200" :title="t('admin.reports.actions.autoPushHint')">
        <input
          v-model="reportPushEnabled"
          type="checkbox"
          class="checkbox"
          :disabled="savingPushToggle"
          @change="togglePush(reportPushEnabled)"
        />
        {{ t('admin.reports.actions.autoPush') }}
      </label>
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
          {{
            activeTab === 'weekly'
              ? t('admin.reports.filters.week')
              : activeTab === 'monthly'
                ? t('admin.reports.filters.month')
                : t('admin.reports.filters.date')
          }}
        </label>
        <input v-if="activeTab === 'daily'" v-model="dailyDate" type="date" class="input text-sm" />
        <select v-else-if="activeTab === 'weekly'" v-model="weeklyDate" class="input text-sm">
          <option v-for="w in weekOptions" :key="w.value" :value="w.value">{{ w.label }}</option>
        </select>
        <input v-else v-model="monthlyDate" type="month" class="input text-sm" />
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
          <button
            class="btn btn-secondary shrink-0 px-3 py-1 text-xs"
            :disabled="pushingReportId === report.id"
            @click="pushToFeishu(report)"
          >
            {{ pushingReportId === report.id ? t('admin.reports.actions.pushing') : t('admin.reports.actions.push') }}
          </button>
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
