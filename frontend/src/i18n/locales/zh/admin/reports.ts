export default {
  reports: {
    title: '日报周报月报',
    description: '按用户聚合的请求统计与 AI 工作摘要；数据来自用量记录与提示词审计（需开启异步审计后才有 prompt 内容）。',
    tabs: { daily: '日报', weekly: '周报', monthly: '月报' },
    filters: {
      date: '日期',
      week: '周',
      month: '月份',
      user: '用户 ID',
      userPlaceholder: '按用户 ID 筛选',
      refresh: '刷新'
    },
    actions: {
      generateAll: '为所有活跃用户生成',
      generating: '生成中…',
      generateFor: '为此人生成',
      retry: '重试',
      settings: 'AI 总结设置',
      push: '发送到飞书',
      pushing: '发送中…',
      pushSuccess: '已推送到飞书群',
      autoPush: '参与飞书自动推送',
      autoPushHint: '仅作用于定时生成的报告（工作日 19:00 日报、每周最后工作日 19:00 周报、每月首个工作日 19:00 月报，自动避开法定节假日）；手动生成的报告不自动推送，可点卡片按钮手动发送'
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
    push: { pushed: '已推送', pushFailed: '推送失败' },
    goal: {
      title: '近期目标',
      placeholder: '填写近期要完成的项目目标，如：完成 XX 功能开发并上线；日报将生成「近期目标计划」小节围绕这些目标制定',
      hint: '仅日报生成时读取；周报/月报聚合日报摘要自然继承。修改后从下次生成起生效',
      save: '保存目标',
      saving: '保存中…',
      saved: '目标已保存'
    },
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
      dailySchedule: '日报 cron（仅时分生效）',
      weeklySchedule: '周报 cron（仅时分生效）',
      monthlySchedule: '月报 cron（仅时分生效，生成上月）',
      skipHolidays: '规避法定节假日（含调休补班）',
      skipHolidaysHint:
        '开启后生成日由工作日规则决定：日报=每个工作日、周报=本周最后一个工作日、月报=当月第一个工作日（生成上月），cron 仅决定当天生成时刻；关闭则逢 cron 触发即生成。次年安排公布并内置前，按「仅避开周末」回退',
      feishuSection: '飞书推送',
      feishuDescription: '通过群自定义机器人 Webhook 把报告卡片推送到飞书群；在飞书群「设置 → 群机器人 → 添加自定义机器人」获取。',
      feishuEnabled: '启用飞书推送',
      feishuWebhook: 'Webhook 地址',
      feishuWebhookPlaceholder: 'https://open.feishu.cn/open-apis/bot/v2/hook/…，留空表示不修改',
      feishuSecret: '加签密钥（可选）',
      feishuSecretPlaceholder: '留空表示不修改',
      feishuPushTypes: '自动推送类型（仅定时生成）',
      feishuPushDaily: '日报',
      feishuPushWeekly: '周报',
      feishuPushMonthly: '月报',
      save: '保存',
      cancel: '取消',
      saved: '已保存'
    }
  }
}
