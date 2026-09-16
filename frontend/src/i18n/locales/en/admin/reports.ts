export default {
  reports: {
    title: 'Reports',
    description:
      'Per-user request statistics with AI work summaries; content comes from usage logs and prompt audit (prompts are included only after async audit is enabled).',
    tabs: { daily: 'Daily', weekly: 'Weekly', monthly: 'Monthly' },
    filters: {
      date: 'Date',
      user: 'User ID',
      userPlaceholder: 'Filter by user ID',
      refresh: 'Refresh'
    },
    actions: {
      generateAll: 'Generate for all active users',
      generating: 'Generating…',
      generateFor: 'Generate for this user',
      retry: 'Retry',
      settings: 'AI Summary Settings'
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
      dailySchedule: 'Daily cron (min hour dom mon dow)',
      weeklySchedule: 'Weekly cron (min hour dom mon dow)',
      monthlySchedule: 'Monthly cron (1st of month, previous month)',
      save: 'Save',
      cancel: 'Cancel',
      saved: 'Saved'
    }
  }
}
