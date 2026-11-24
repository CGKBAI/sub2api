<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import reportsAPI, {
  type Report,
  type ReportLLMConfig,
  type ReportType,
  type ReportFeishuGroup
} from '@/api/admin/reports'
import { list as listAdminUsers } from '@/api/admin/users'
import type { AdminUser } from '@/types'
import { useAppStore } from '@/stores/app'
import AppLayout from '@/components/layout/AppLayout.vue'
import { BaseDialog, EmptyState, LoadingSpinner } from '@/components/common'

marked.setOptions({ breaks: true, gfm: true })

const { t } = useI18n()
const appStore = useAppStore()

const activeTab = ref<ReportType | 'groups'>('daily')
// 群组管理 tab 不是报告类型；生成/筛选统一回退到日报类型（此时按钮已隐藏）
const activeReportType = computed<ReportType>(() =>
  activeTab.value === 'groups' ? 'daily' : activeTab.value
)
const dailyDate = ref(todayStr())
const userIdFilter = ref('')
const loading = ref(false)
const reports = ref<Report[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 50

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
  if (activeTab.value === 'groups') return ''
  if (activeTab.value === 'weekly') return weeklyDate.value
  if (activeTab.value === 'monthly') return monthlyDate.value ? `${monthlyDate.value}-01` : ''
  return dailyDate.value
})

const generatingAll = ref(false)
const generatingUserId = ref<number | null>(null)
const pushingReportId = ref<number | null>(null)

// 节假日感知开关（页头按钮）：
// 开=日报工作日/周报本周最后工作日/月报当月首个工作日出上月；
// 关=日报每天（当日请求超阈值的用户）/周报周五/月报月底出当月
const holidayAware = ref(true)
const togglingHoliday = ref(false)

async function loadHolidayAware() {
  try {
    const cfg = await reportsAPI.getConfig()
    holidayAware.value = cfg.skip_holidays
  } catch {
    // 读取失败保持默认展示，切换时以后端返回为准
  }
}

async function toggleHolidayAware() {
  togglingHoliday.value = true
  try {
    const cfg = await reportsAPI.updateConfig({ skip_holidays: !holidayAware.value })
    holidayAware.value = cfg.skip_holidays
    appStore.showSuccess(t('admin.reports.actions.holidayToggled'))
  } catch (error: any) {
    appStore.showError(error?.message || String(error))
  } finally {
    togglingHoliday.value = false
  }
}

async function pushToFeishu(report: Report) {
  pushingReportId.value = report.id
  try {
    const updated = await reportsAPI.pushToFeishu(report.id)
    const deliveries = updated.push_deliveries ?? []
    const ok = deliveries.filter((d) => d.status === 'success').length
    if (deliveries.length > 0) {
      appStore.showSuccess(t('admin.reports.push.pushSuccessCount', { ok, total: deliveries.length }))
    } else {
      appStore.showSuccess(t('admin.reports.actions.pushSuccess'))
    }
    await load()
  } catch (error: any) {
    appStore.showError(error?.message || String(error))
  } finally {
    pushingReportId.value = null
  }
}

// ===================== 报告群组管理 =====================
const groups = ref<ReportFeishuGroup[]>([])
const groupsLoading = ref(false)
const groupDialogOpen = ref(false)
const editingGroupId = ref<number | null>(null)
const savingGroup = ref(false)
const testingGroupId = ref<number | null>(null)
const groupForm = ref({
  name: '',
  webhook_url: '',
  secret: '',
  enabled: true,
  user_ids: [] as number[]
})
const allUsers = ref<AdminUser[]>([])
const usersLoading = ref(false)
const userSearch = ref('')
const filteredUsers = computed(() => {
  const q = userSearch.value.trim().toLowerCase()
  if (!q) return allUsers.value
  return allUsers.value.filter(
    (u) => (u.email ?? '').toLowerCase().includes(q) || (u.username ?? '').toLowerCase().includes(q)
  )
})

async function loadGroups() {
  groupsLoading.value = true
  try {
    groups.value = await reportsAPI.listGroups()
  } catch (error: any) {
    appStore.showError(error?.message || String(error))
  } finally {
    groupsLoading.value = false
  }
}

async function loadUsers() {
  if (allUsers.value.length > 0 || usersLoading.value) return
  usersLoading.value = true
  try {
    const res = await listAdminUsers(1, 200, {})
    allUsers.value = res.items ?? []
  } catch (error: any) {
    appStore.showError(error?.message || String(error))
  } finally {
    usersLoading.value = false
  }
}

function openCreateGroup() {
  editingGroupId.value = null
  groupForm.value = { name: '', webhook_url: '', secret: '', enabled: true, user_ids: [] }
  userSearch.value = ''
  groupDialogOpen.value = true
  loadUsers()
}

async function openEditGroup(group: ReportFeishuGroup) {
  editingGroupId.value = group.id
  userSearch.value = ''
  try {
    const detail = await reportsAPI.getGroup(group.id)
    groupForm.value = {
      name: detail.name,
      // 回显脱敏值，留空/占位符均表示保留旧值
      webhook_url: '',
      secret: '',
      enabled: detail.enabled,
      user_ids: detail.user_ids ?? []
    }
    groupDialogOpen.value = true
    loadUsers()
  } catch (error: any) {
    appStore.showError(error?.message || String(error))
  }
}

function toggleGroupMember(userId: number) {
  const idx = groupForm.value.user_ids.indexOf(userId)
  if (idx >= 0) {
    groupForm.value.user_ids.splice(idx, 1)
  } else {
    groupForm.value.user_ids.push(userId)
  }
}

function isGroupMember(userId: number): boolean {
  return groupForm.value.user_ids.includes(userId)
}

async function saveGroup() {
  if (!groupForm.value.name.trim()) return
  savingGroup.value = true
  try {
    if (editingGroupId.value) {
      await reportsAPI.updateGroup(editingGroupId.value, {
        name: groupForm.value.name.trim(),
        webhook_url: groupForm.value.webhook_url.trim() || undefined,
        secret: groupForm.value.secret.trim() || undefined,
        enabled: groupForm.value.enabled,
        user_ids: groupForm.value.user_ids
      })
    } else {
      await reportsAPI.createGroup({
        name: groupForm.value.name.trim(),
        webhook_url: groupForm.value.webhook_url.trim(),
        secret: groupForm.value.secret.trim(),
        enabled: groupForm.value.enabled,
        user_ids: groupForm.value.user_ids
      })
    }
    appStore.showSuccess(t('admin.reports.groups.saved'))
    groupDialogOpen.value = false
    await loadGroups()
  } catch (error: any) {
    appStore.showError(error?.message || String(error))
  } finally {
    savingGroup.value = false
  }
}

async function removeGroup(group: ReportFeishuGroup) {
  if (!window.confirm(t('admin.reports.groups.deleteConfirm', { name: group.name }))) return
  try {
    await reportsAPI.deleteGroup(group.id)
    appStore.showSuccess(t('admin.reports.groups.deleted'))
    await loadGroups()
  } catch (error: any) {
    appStore.showError(error?.message || String(error))
  }
}

async function testGroup(group: ReportFeishuGroup) {
  testingGroupId.value = group.id
  try {
    await reportsAPI.testGroup(group.id)
    appStore.showSuccess(t('admin.reports.groups.testSent'))
  } catch (error: any) {
    appStore.showError(error?.message || String(error))
  } finally {
    testingGroupId.value = null
  }
}

function pushDeliverySummary(report: Report) {
  const deliveries = report.push_deliveries ?? []
  if (deliveries.length === 0) return null
  const ok = deliveries.filter((d) => d.status === 'success').length
  const failed = deliveries.filter((d) => d.status === 'failed')
  return {
    total: deliveries.length,
    ok,
    failed: failed.length,
    detail: failed.map((d) => `${d.group_name || `#${d.group_id}`}: ${d.error || 'failed'}`).join('\n')
  }
}

function groupWebhookLabel(group: ReportFeishuGroup): string {
  return group.webhook_url ? t('admin.reports.groups.configured') : t('admin.reports.groups.notConfigured')
}

function todayStr(): string {
  const d = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

async function load() {
  if (activeTab.value === 'groups') return
  loading.value = true
  try {
    const filters: Record<string, string | number> = { type: activeTab.value }
    if (activeDate.value) filters.date = activeDate.value
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

watch([activeTab, dailyDate, weeklyDate, monthlyDate, userIdFilter], () => {
  page.value = 1
  if (activeTab.value === 'groups') {
    loadGroups()
    return
  }
  load()
})

onMounted(() => {
  load()
  loadHolidayAware()
})

async function generateAll() {
  generatingAll.value = true
  try {
    const res = await reportsAPI.generateAll({
      type: activeReportType.value,
      date: activeDate.value || undefined
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
      type: activeReportType.value,
      user_id: report.user_id,
      date: activeDate.value || undefined
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
  if (report.type === 'daily') {
    // 新报告 period_end = 当天切点；旧报告 period_end = 次日 00:00，只显示日期。
    if (fmt(start) !== fmt(end)) return fmt(start)
    const cutoff = `${String(end.getHours()).padStart(2, '0')}:${String(end.getMinutes()).padStart(2, '0')}`
    return `${fmt(start)}（截至 ${cutoff}）`
  }
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
  daily_schedule: '0 19 * * *',
  weekly_schedule: '0 19 * * *',
  monthly_schedule: '0 19 * * *',
  skip_holidays: true,
  feishu_enabled: false,
  feishu_webhook_url: '',
  feishu_secret: '',
  feishu_push_daily: true,
  feishu_push_weekly: true,
  feishu_push_monthly: true
})

async function openSettings() {
  try {
    const cfg = await reportsAPI.getConfig()
    configForm.value = { ...cfg, api_key: '', feishu_webhook_url: '', feishu_secret: '' }
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
    if (!configForm.value.feishu_webhook_url) delete payload.feishu_webhook_url
    if (!configForm.value.feishu_secret) delete payload.feishu_secret
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
  <AppLayout>
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
          <button
            class="btn text-sm"
            :class="holidayAware ? 'btn-primary' : 'btn-secondary'"
            :disabled="togglingHoliday"
            :title="t('admin.reports.actions.holidayAwareHint')"
            @click="toggleHolidayAware"
          >
            {{ holidayAware ? t('admin.reports.actions.holidayAwareOn') : t('admin.reports.actions.holidayAwareOff') }}
          </button>
          <button class="btn btn-secondary text-sm" @click="openSettings">
            {{ t('admin.reports.actions.settings') }}
          </button>
          <button
            v-if="activeTab !== 'groups'"
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
            v-for="tp in ['daily', 'weekly', 'monthly', 'groups'] as const"
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
        <div v-if="activeTab !== 'groups'" class="flex flex-wrap items-center gap-2">
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
          <input
            v-model="userIdFilter"
            type="text"
            :placeholder="t('admin.reports.filters.userPlaceholder')"
            class="input w-40 text-sm"
          />
        </div>
      </div>

      <!-- 报告群组管理 -->
      <div v-if="activeTab === 'groups'" class="space-y-4">
        <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
              {{ t('admin.reports.groups.title') }}
            </h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.reports.groups.description') }}
            </p>
          </div>
          <button class="btn btn-primary text-sm" @click="openCreateGroup">
            {{ t('admin.reports.groups.create') }}
          </button>
        </div>

        <LoadingSpinner v-if="groupsLoading" />
        <EmptyState v-else-if="groups.length === 0" :description="t('admin.reports.groups.empty')" />
        <div v-else class="card overflow-x-auto p-0">
          <table class="min-w-full text-sm">
            <thead class="bg-gray-50 text-left text-xs uppercase text-gray-500 dark:bg-gray-800 dark:text-gray-400">
              <tr>
                <th class="px-4 py-3">{{ t('admin.reports.groups.name') }}</th>
                <th class="px-4 py-3">{{ t('admin.reports.groups.members') }}</th>
                <th class="px-4 py-3">{{ t('admin.reports.groups.webhook') }}</th>
                <th class="px-4 py-3">{{ t('admin.reports.groups.status') }}</th>
                <th class="px-4 py-3 text-right">{{ t('admin.reports.groups.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="g in groups"
                :key="g.id"
                class="border-t border-gray-100 dark:border-gray-700"
              >
                <td class="px-4 py-3 font-medium text-gray-900 dark:text-white">{{ g.name }}</td>
                <td class="px-4 py-3 text-gray-700 dark:text-gray-200">{{ g.member_count }}</td>
                <td class="px-4 py-3 text-gray-500 dark:text-gray-400">{{ groupWebhookLabel(g) }}</td>
                <td class="px-4 py-3">
                  <span class="badge" :class="g.enabled ? 'badge-success' : 'badge-warning'">
                    {{ g.enabled ? t('admin.reports.groups.enabled') : t('admin.reports.groups.disabled') }}
                  </span>
                </td>
                <td class="whitespace-nowrap px-4 py-3 text-right">
                  <button class="btn btn-secondary px-3 py-1 text-xs" @click="openEditGroup(g)">
                    {{ t('admin.reports.groups.edit') }}
                  </button>
                  <button
                    class="btn btn-secondary ml-2 px-3 py-1 text-xs"
                    :disabled="testingGroupId === g.id"
                    @click="testGroup(g)"
                  >
                    {{
                      testingGroupId === g.id
                        ? t('admin.reports.groups.testing')
                        : t('admin.reports.groups.test')
                    }}
                  </button>
                  <button class="btn btn-danger ml-2 px-3 py-1 text-xs" @click="removeGroup(g)">
                    {{ t('admin.reports.groups.delete') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- 列表 -->
      <template v-else>
      <LoadingSpinner v-if="loading" />
      <EmptyState v-else-if="reports.length === 0" :description="t('admin.reports.empty')" />
      <div v-else class="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <div
          v-for="report in reports"
          :key="report.id"
          class="card p-5"
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
                <span
                  v-if="pushDeliverySummary(report)"
                  class="badge"
                  :class="pushDeliverySummary(report)?.failed === 0 ? 'badge-success' : 'badge-danger'"
                  :title="pushDeliverySummary(report)?.detail || ''"
                >
                  {{
                    pushDeliverySummary(report)?.failed === 0
                      ? t('admin.reports.push.pushSuccessCount', {
                          ok: pushDeliverySummary(report)?.ok,
                          total: pushDeliverySummary(report)?.total
                        })
                      : t('admin.reports.push.pushPartial', {
                          ok: pushDeliverySummary(report)?.ok,
                          total: pushDeliverySummary(report)?.total
                        })
                  }}
                </span>
                <span
                  v-else-if="report.pushed_at"
                  class="badge badge-success"
                  :title="new Date(report.pushed_at).toLocaleString()"
                >
                  {{ t('admin.reports.push.pushed') }}
                </span>
                <span
                  v-else-if="report.last_push_error"
                  class="badge badge-danger"
                  :title="report.last_push_error"
                >
                  {{ t('admin.reports.push.pushFailed') }}
                </span>
                <span
                  v-if="report.quality_warnings"
                  class="badge badge-warning"
                  :title="report.quality_warnings"
                >
                  {{ t('admin.reports.push.qualityWarn') }}
                </span>
              </div>
              <div class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
                {{ periodLabel(report) }}
              </div>
            </div>
            <div class="flex shrink-0 gap-2">
              <button
                class="btn btn-secondary px-3 py-1 text-xs"
                :disabled="pushingReportId === report.id"
                @click="pushToFeishu(report)"
              >
                {{ pushingReportId === report.id ? t('admin.reports.actions.pushing') : t('admin.reports.actions.push') }}
              </button>
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

      </template>

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

          <p class="text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.reports.config.scheduleHint') }}
          </p>

          <!-- 飞书推送 -->
          <div class="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-gray-700">
            <div class="text-sm font-medium text-gray-900 dark:text-white">
              {{ t('admin.reports.config.feishuSection') }}
            </div>
            <p class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.reports.config.feishuDescription') }}
            </p>
            <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
              <input v-model="configForm.feishu_enabled" type="checkbox" class="checkbox" />
              {{ t('admin.reports.config.feishuEnabled') }}
            </label>
            <p class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.reports.config.feishuGroupHint') }}
            </p>
            <div class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.reports.config.feishuPushTypes') }}
            </div>
            <div class="flex flex-wrap gap-4 text-sm text-gray-700 dark:text-gray-200">
              <label class="flex items-center gap-2">
                <input v-model="configForm.feishu_push_daily" type="checkbox" class="checkbox" />
                {{ t('admin.reports.config.feishuPushDaily') }}
              </label>
              <label class="flex items-center gap-2">
                <input v-model="configForm.feishu_push_weekly" type="checkbox" class="checkbox" />
                {{ t('admin.reports.config.feishuPushWeekly') }}
              </label>
              <label class="flex items-center gap-2">
                <input v-model="configForm.feishu_push_monthly" type="checkbox" class="checkbox" />
                {{ t('admin.reports.config.feishuPushMonthly') }}
              </label>
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

      <!-- 报告群组编辑弹窗 -->
      <BaseDialog
        :show="groupDialogOpen"
        :title="
          editingGroupId
            ? t('admin.reports.groups.editTitle')
            : t('admin.reports.groups.createTitle')
        "
        @close="groupDialogOpen = false"
      >
        <div class="space-y-4">
          <p class="text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.reports.groups.dialogHint') }}
          </p>
          <div>
            <label class="mb-1 block text-xs text-gray-500">{{ t('admin.reports.groups.name') }}</label>
            <input v-model="groupForm.name" type="text" :class="inputClass" :placeholder="t('admin.reports.groups.namePlaceholder')" />
          </div>
          <div>
            <label class="mb-1 block text-xs text-gray-500">{{ t('admin.reports.groups.webhook') }}</label>
            <input
              v-model="groupForm.webhook_url"
              type="password"
              :class="inputClass"
              :placeholder="
                editingGroupId
                  ? t('admin.reports.groups.webhookKeep')
                  : t('admin.reports.groups.webhookPlaceholder')
              "
              autocomplete="new-password"
            />
          </div>
          <div>
            <label class="mb-1 block text-xs text-gray-500">{{ t('admin.reports.groups.secret') }}</label>
            <input
              v-model="groupForm.secret"
              type="password"
              :class="inputClass"
              :placeholder="
                editingGroupId
                  ? t('admin.reports.groups.secretKeep')
                  : t('admin.reports.groups.secretPlaceholder')
              "
              autocomplete="new-password"
            />
          </div>
          <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
            <input v-model="groupForm.enabled" type="checkbox" class="checkbox" />
            {{ t('admin.reports.groups.enabled') }}
          </label>

          <div>
            <div class="mb-1 flex items-center justify-between">
              <label class="text-xs text-gray-500">
                {{ t('admin.reports.groups.members') }}
                <span class="ml-1 text-gray-400">({{ groupForm.user_ids.length }})</span>
              </label>
              <input
                v-model="userSearch"
                type="text"
                :placeholder="t('admin.reports.groups.searchUser')"
                class="input w-48 text-sm"
              />
            </div>
            <LoadingSpinner v-if="usersLoading" />
            <div
              v-else
              class="max-h-64 space-y-0.5 overflow-y-auto rounded-lg border border-gray-200 p-2 dark:border-gray-700"
            >
              <div v-if="filteredUsers.length === 0" class="p-2 text-xs text-gray-400">
                {{ t('admin.reports.groups.noUsers') }}
              </div>
              <label
                v-for="u in filteredUsers"
                :key="u.id"
                class="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm hover:bg-gray-50 dark:hover:bg-gray-700"
              >
                <input
                  type="checkbox"
                  class="checkbox"
                  :checked="isGroupMember(u.id)"
                  @change="toggleGroupMember(u.id)"
                />
                <span class="text-gray-900 dark:text-white">{{ u.username || u.email }}</span>
                <span class="truncate text-xs text-gray-400">{{ u.email }}</span>
              </label>
            </div>
          </div>

          <div class="flex justify-end gap-2 pt-2">
            <button class="btn btn-secondary text-sm" @click="groupDialogOpen = false">
              {{ t('admin.reports.config.cancel') }}
            </button>
            <button
              class="btn btn-primary text-sm"
              :disabled="savingGroup || !groupForm.name.trim()"
              @click="saveGroup"
            >
              {{ savingGroup ? t('admin.reports.actions.saving') : t('admin.reports.config.save') }}
            </button>
          </div>
        </div>
      </BaseDialog>
    </div>
  </AppLayout>
</template>
