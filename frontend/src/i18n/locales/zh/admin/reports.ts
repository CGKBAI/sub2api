export default {
  reports: {
    title: '日报周报',
    description: '按用户聚合的请求统计与 AI 工作摘要；数据来自用量记录与提示词审计（需开启异步审计后才有 prompt 内容）。',
    tabs: { daily: '日报', weekly: '周报' },
    filters: {
      date: '日期',
      user: '用户 ID',
      userPlaceholder: '按用户 ID 筛选',
      refresh: '刷新'
    },
    actions: {
      generateAll: '为所有活跃用户生成',
      generating: '生成中…',
      generateFor: '为此人生成',
      retry: '重试',
      settings: 'AI 总结设置'
    },
    stats: {
      requests: '请求',
      inputTokens: '输入 tokens',
      outputTokens: '输出 tokens',
      cost: '费用',
      prompts: 'Prompt 片段',
      models: '模型分布',
      hourly: '活跃时段'
    },
    summary: { title: 'AI 工作摘要', empty: '暂无 AI 总结（未配置 LLM 或该周期无 prompt 数据）' },
    status: { pending: '生成中', done: '完成', failed: '失败' },
    empty: '该条件下暂无报告',
    config: {
      title: 'AI 总结设置',
      description: '配置 OpenAI 兼容接口用于生成工作摘要；不配置则只产出统计报告。',
      enabled: '启用定时生成',
      baseUrl: 'Base URL',
      baseUrlPlaceholder: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
      apiKey: 'API Key',
      apiKeyPlaceholder: '留空表示不修改',
      model: '模型',
      modelPlaceholder: 'qwen-plus / gpt-4o-mini 等',
      maxPrompts: '每次总结最多 prompt 条数',
      truncateChars: '单条 prompt 截断字符数',
      dailySchedule: '日报 cron（分 时 日 月 周）',
      weeklySchedule: '周报 cron（分 时 日 月 周）',
      save: '保存',
      cancel: '取消',
      saved: '已保存'
    }
  }
}
