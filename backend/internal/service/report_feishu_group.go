package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

const (
	// reportFeishuMultiPushTimeout 一次多群投递的总预算（单群由 client 10s 超时兜底）。
	reportFeishuMultiPushTimeout = 30 * time.Second
	// reportFeishuPushConcurrency 多群并行投递的最大并发数。
	reportFeishuPushConcurrency = 4
	// reportFeishuDeliveryTimeout 投递结果落库超时（best-effort，独立于推送 ctx）。
	reportFeishuDeliveryTimeout = 5 * time.Second
)

// ListFeishuGroups 列出全部报告群组（管理端）。
func (s *ReportService) ListFeishuGroups(ctx context.Context) ([]ReportFeishuGroup, error) {
	if s == nil || s.groupRepo == nil {
		return nil, errors.New("report group repository not initialized")
	}
	return s.groupRepo.ListGroups(ctx)
}

// GetFeishuGroup 获取群组详情与成员 ID 列表。
func (s *ReportService) GetFeishuGroup(ctx context.Context, id int64) (*ReportFeishuGroup, []int64, error) {
	if s == nil || s.groupRepo == nil {
		return nil, nil, errors.New("report group repository not initialized")
	}
	if id <= 0 {
		return nil, nil, domain.ErrReportFeishuGroupNotFound
	}
	g, err := s.groupRepo.GetGroup(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	members, err := s.groupRepo.ListGroupMemberIDs(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return g, members, nil
}

// CreateFeishuGroup 新建群组；userIDs 非 nil 时同时写入成员。
func (s *ReportService) CreateFeishuGroup(ctx context.Context, g *ReportFeishuGroup, userIDs []int64) (*ReportFeishuGroup, error) {
	if s == nil || s.groupRepo == nil {
		return nil, errors.New("report group repository not initialized")
	}
	if g == nil {
		return nil, domain.ErrReportFeishuGroupInvalid
	}
	normalizeFeishuGroup(g)
	if g.Name == "" || g.WebhookURL == "" {
		return nil, domain.ErrReportFeishuGroupInvalid
	}
	if err := s.groupRepo.CreateGroup(ctx, g); err != nil {
		return nil, err
	}
	if userIDs != nil {
		if err := s.groupRepo.ReplaceGroupMembers(ctx, g.ID, userIDs); err != nil {
			return nil, err
		}
		g.MemberCount = len(uniqueIDs(userIDs))
	}
	return g, nil
}

// UpdateFeishuGroup 更新群组；空字段保留旧值（webhook/secret 留空=不修改），
// replaceMembers=true 时用 userIDs 全量替换成员。
func (s *ReportService) UpdateFeishuGroup(ctx context.Context, g *ReportFeishuGroup, userIDs []int64, replaceMembers bool) (*ReportFeishuGroup, error) {
	if s == nil || s.groupRepo == nil {
		return nil, errors.New("report group repository not initialized")
	}
	if g == nil || g.ID <= 0 {
		return nil, domain.ErrReportFeishuGroupNotFound
	}
	existing, err := s.groupRepo.GetGroup(ctx, g.ID)
	if err != nil {
		return nil, err
	}
	normalizeFeishuGroup(g)
	if g.Name == "" {
		g.Name = existing.Name
	}
	if g.WebhookURL == "" {
		g.WebhookURL = existing.WebhookURL
	}
	if g.Secret == "" {
		g.Secret = existing.Secret
	}
	if g.Name == "" || g.WebhookURL == "" {
		return nil, domain.ErrReportFeishuGroupInvalid
	}
	if err := s.groupRepo.UpdateGroup(ctx, g); err != nil {
		return nil, err
	}
	if replaceMembers {
		if err := s.groupRepo.ReplaceGroupMembers(ctx, g.ID, userIDs); err != nil {
			return nil, err
		}
	}
	members, err := s.groupRepo.ListGroupMemberIDs(ctx, g.ID)
	if err != nil {
		return nil, err
	}
	g.MemberCount = len(members)
	return g, nil
}

// DeleteFeishuGroup 删除群组（级联删除成员与投递记录）。
func (s *ReportService) DeleteFeishuGroup(ctx context.Context, id int64) error {
	if s == nil || s.groupRepo == nil {
		return errors.New("report group repository not initialized")
	}
	if id <= 0 {
		return domain.ErrReportFeishuGroupNotFound
	}
	return s.groupRepo.DeleteGroup(ctx, id)
}

// SetFeishuGroupMembers 全量替换群组成员。
func (s *ReportService) SetFeishuGroupMembers(ctx context.Context, groupID int64, userIDs []int64) error {
	if s == nil || s.groupRepo == nil {
		return errors.New("report group repository not initialized")
	}
	if groupID <= 0 {
		return domain.ErrReportFeishuGroupNotFound
	}
	return s.groupRepo.ReplaceGroupMembers(ctx, groupID, userIDs)
}

// TestFeishuGroup 向指定群发送一张测试卡片（不写业务报告/投递记录）。
func (s *ReportService) TestFeishuGroup(ctx context.Context, id int64) error {
	if s == nil || s.groupRepo == nil || s.feishu == nil {
		return errors.New("report service not initialized")
	}
	g, err := s.groupRepo.GetGroup(ctx, id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(g.WebhookURL) == "" {
		return domain.ErrReportFeishuNotConfigured
	}
	cfg := &ReportLLMConfig{
		FeishuEnabled:    true,
		FeishuWebhookURL: g.WebhookURL,
		FeishuSecret:     g.Secret,
	}
	pushCtx, cancel := context.WithTimeout(ctx, reportFeishuPushTimeout)
	defer cancel()
	return s.feishu.PushTestCard(pushCtx, cfg, g.Name)
}

func normalizeFeishuGroup(g *ReportFeishuGroup) {
	if g == nil {
		return
	}
	g.Name = strings.TrimSpace(g.Name)
	g.WebhookURL = strings.TrimSpace(g.WebhookURL)
	g.Secret = strings.TrimSpace(g.Secret)
}

func uniqueIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// pushReportToUserGroups 把同一份报告投递到报告作者所属的所有启用群组。
// 按 webhook URL 去重（多个群共用一个机器人时只发一次，但各群都记录结果）；
// 群之间相互隔离，单个群失败不影响其他群。返回 (成功群数, 失败群数, 汇总错误)。
// 推送结果会写入 reports（全成功才更新 pushed_at）与 report_feishu_deliveries。
func (s *ReportService) pushReportToUserGroups(ctx context.Context, cfg *ReportLLMConfig, report *Report) (int, int, error) {
	if s == nil || s.groupRepo == nil || s.feishu == nil || cfg == nil || report == nil {
		return 0, 0, nil
	}
	groups, err := s.groupRepo.ListEnabledGroupsForUser(ctx, report.UserID)
	if err != nil {
		logger.LegacyPrintf("service.report", "[ReportFeishu] list groups for user %d: %v", report.UserID, err)
		return 0, 0, err
	}
	if len(groups) == 0 {
		logger.LegacyPrintf("service.report", "[ReportFeishu] user %d has no enabled report groups, skip push", report.UserID)
		return 0, 0, nil
	}
	// 本次推送结果覆盖旧值，避免手动重推时 DTO 里出现重复的群记录
	report.PushDeliveries = nil

	type target struct {
		url    string
		secret string
		groups []ReportFeishuGroup
	}
	order := make([]string, 0, len(groups))
	buckets := make(map[string]*target, len(groups))
	success := 0
	failed := 0
	failures := make([]string, 0, len(groups))
	for _, g := range groups {
		url := strings.TrimSpace(g.WebhookURL)
		if url == "" {
			// 没有 webhook 的群直接失败落库，避免整次推送被一个坏配置拖垮
			d := s.recordDelivery(report.ID, g, domain.ErrReportFeishuNotConfigured)
			report.PushDeliveries = append(report.PushDeliveries, d)
			failed++
			failures = append(failures, fmt.Sprintf("%s（%s）", g.Name, domain.ErrReportFeishuNotConfigured.Error()))
			continue
		}
		if _, ok := buckets[url]; !ok {
			buckets[url] = &target{url: url, secret: g.Secret}
			order = append(order, url)
		}
		buckets[url].groups = append(buckets[url].groups, g)
	}

	pushCtx, cancel := context.WithTimeout(ctx, reportFeishuMultiPushTimeout)
	defer cancel()

	sem := make(chan struct{}, reportFeishuPushConcurrency)
	type pushResult struct {
		target *target
		err    error
	}
	results := make(chan pushResult, len(order))
	var wg sync.WaitGroup
	for _, url := range order {
		t := buckets[url]
		wg.Add(1)
		go func(t *target) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-pushCtx.Done():
				results <- pushResult{target: t, err: pushCtx.Err()}
				return
			}
			cfgClone := *cfg
			cfgClone.FeishuEnabled = true
			cfgClone.FeishuWebhookURL = t.url
			cfgClone.FeishuSecret = t.secret
			results <- pushResult{target: t, err: s.feishu.PushReportCard(pushCtx, &cfgClone, report)}
		}(t)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	for res := range results {
		for _, g := range res.target.groups {
			d := s.recordDelivery(report.ID, g, res.err)
			report.PushDeliveries = append(report.PushDeliveries, d)
			if res.err != nil {
				failed++
				failures = append(failures, fmt.Sprintf("%s（%s）", g.Name, res.err.Error()))
			} else {
				success++
			}
		}
	}

	if failed == 0 {
		s.markPushResult(report, nil)
		return success, 0, nil
	}
	aggErr := fmt.Errorf("%w: %d/%d groups failed: %s",
		domain.ErrReportFeishuPushFailed, failed, success+failed, strings.Join(failures, "；"))
	s.markPushResult(report, aggErr)
	return success, failed, aggErr
}

// recordDelivery 写入单个群组的投递结果（独立 Background ctx，避免推送 ctx 取消丢状态）。
func (s *ReportService) recordDelivery(reportID int64, g ReportFeishuGroup, pushErr error) ReportPushDelivery {
	d := ReportPushDelivery{
		ReportID:    reportID,
		GroupID:     g.ID,
		GroupName:   g.Name,
		Status:      domain.ReportPushStatusSuccess,
		AttemptedAt: timezone.Now(),
	}
	if pushErr != nil {
		d.Status = domain.ReportPushStatusFailed
		d.Error = truncateReportError(pushErr.Error())
	}
	recordCtx, cancel := context.WithTimeout(context.Background(), reportFeishuDeliveryTimeout)
	defer cancel()
	if err := s.groupRepo.UpsertDelivery(recordCtx, &d); err != nil {
		logger.LegacyPrintf("service.report", "[ReportFeishu] record delivery report=%d group=%d: %v", reportID, g.ID, err)
	}
	return d
}

// attachDeliveries 批量补齐报告的群组投递结果（列表/详情共用，避免 N+1）。
func (s *ReportService) attachDeliveries(ctx context.Context, items []Report) {
	if s == nil || s.groupRepo == nil || len(items) == 0 {
		return
	}
	ids := make([]int64, 0, len(items))
	for i := range items {
		if items[i].ID > 0 {
			ids = append(ids, items[i].ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	byReport, err := s.groupRepo.ListDeliveriesByReportIDs(ctx, ids)
	if err != nil {
		logger.LegacyPrintf("service.report", "[ReportFeishu] list deliveries: %v", err)
		return
	}
	for i := range items {
		if ds := byReport[items[i].ID]; len(ds) > 0 {
			items[i].PushDeliveries = ds
		}
	}
}
