export default {
  reports: {
    title: 'Reports',
    description:
      'Per-user request statistics with AI work summaries; content comes from usage logs and prompt audit (prompts are included only after async audit is enabled).',
    tabs: { daily: 'Daily', weekly: 'Weekly', monthly: 'Monthly' },
    filters: {
      date: 'Date',
      week: 'Week',
      month: 'Month',
      user: 'User ID',
      userPlaceholder: 'Filter by user ID',
      refresh: 'Refresh'
    },
    actions: {
      generateAll: 'Generate for all active users',
      generating: 'Generating…',
      generateFor: 'Generate for this user',
      retry: 'Retry',
      settings: 'AI Summary Settings',
      push: 'Send to Feishu',
      pushing: 'Sending…',
      pushSuccess: 'Pushed to Feishu group',
      autoPush: 'Join Feishu auto push',
      autoPushHint:
        'Applies only to scheduled reports (daily 19:00 on workdays, weekly on the last workday, monthly on the first workday, skipping statutory holidays); manually generated reports are never auto-pushed — use the card button to send'
    },
    stats: {
      requests: 'Requests',
      inputTokens: 'Input tokens',
      outputTokens: 'Output tokens',
      cost: 'Cost',
      prompts: 'Prompt snippets',
      models: 'Model distribution',
      hourly: 'Active hours'
    },
    summary: { title: 'AI Summary', empty: 'No AI summary (LLM not configured or no prompts in this period)' },
    status: { pending: 'Pending', done: 'Done', failed: 'Failed' },
    push: { pushed: 'Pushed', pushFailed: 'Push failed' },
    goal: {
      title: 'Recent Goals',
      placeholder:
        'List the projects you aim to complete soon, e.g. ship feature X. The daily report will include a "Recent Goals Plan" section built around these goals',
      hint: 'Read by daily report generation only; weekly/monthly reports inherit via daily summaries. Takes effect from the next generation after saving',
      save: 'Save goals',
      saving: 'Saving…',
      saved: 'Goals saved'
    },
    empty: 'No reports under current filters',
    config: {
      title: 'AI Summary Settings',
      description: 'Configure an OpenAI-compatible endpoint to generate work summaries; without it only statistics are produced.',
      enabled: 'Enable scheduled generation',
      baseUrl: 'Base URL',
      baseUrlPlaceholder: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
      apiKey: 'API Key',
      apiKeyPlaceholder: 'Leave empty to keep unchanged',
      model: 'Model',
      modelPlaceholder: 'qwen-plus / gpt-4o-mini etc.',
      maxPrompts: 'Max prompts per summary',
      truncateChars: 'Truncate each prompt to characters',
      dailySchedule: 'Daily cron (hour & minute only)',
      weeklySchedule: 'Weekly cron (hour & minute only)',
      monthlySchedule: 'Monthly cron (hour & minute only, previous month)',
      skipHolidays: 'Skip weekends & statutory holidays (incl. adjusted workdays)',
      skipHolidaysHint:
        'When on, the generation day follows workday rules: daily = every workday, weekly = last workday of the week, monthly = first workday of the month; the cron decides the time of day only. When off, reports generate on every cron trigger. Falls back to weekends-only until next year\'s schedule is built in',
      feishuSection: 'Feishu Push',
      feishuDescription:
        'Push report cards to a Feishu group via custom bot webhook. Get one in Feishu: group settings → bots → add custom bot.',
      feishuEnabled: 'Enable Feishu push',
      feishuWebhook: 'Webhook URL',
      feishuWebhookPlaceholder: 'https://open.feishu.cn/open-apis/bot/v2/hook/…, leave empty to keep unchanged',
      feishuSecret: 'Signing secret (optional)',
      feishuSecretPlaceholder: 'Leave empty to keep unchanged',
      feishuPushTypes: 'Auto push types (scheduled reports only)',
      feishuPushDaily: 'Daily',
      feishuPushWeekly: 'Weekly',
      feishuPushMonthly: 'Monthly',
      save: 'Save',
      cancel: 'Cancel',
      saved: 'Saved'
    }
  }
}
