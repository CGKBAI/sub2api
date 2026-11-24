package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// =========================
// 多群推送路由
// =========================

type fakeReportGroupRepo struct {
	ReportGroupRepository
	groups []ReportFeishuGroup
	calls  int32

	mu         sync.Mutex
	deliveries map[int64][]ReportPushDelivery
}

func (f *fakeReportGroupRepo) ListEnabledGroupsForUser(ctx context.Context, userID int64) ([]ReportFeishuGroup, error) {
	atomic.AddInt32(&f.calls, 1)
	return f.groups, nil
}

func (f *fakeReportGroupRepo) ListGroups(ctx context.Context) ([]ReportFeishuGroup, error) {
	return f.groups, nil
}

func (f *fakeReportGroupRepo) UpsertDelivery(ctx context.Context, d *ReportPushDelivery) error {
	if d.AttemptedAt.IsZero() {
		d.AttemptedAt = time.Now()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deliveries == nil {
		f.deliveries = map[int64][]ReportPushDelivery{}
	}
	list := f.deliveries[d.ReportID]
	for i := range list {
		if list[i].GroupID == d.GroupID {
			d.AttemptCount = list[i].AttemptCount + 1
			list[i] = *d
			f.deliveries[d.ReportID] = list
			return nil
		}
	}
	d.AttemptCount = 1
	f.deliveries[d.ReportID] = append(list, *d)
	return nil
}

func (f *fakeReportGroupRepo) ListDeliveriesByReportIDs(ctx context.Context, ids []int64) (map[int64][]ReportPushDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deliveries, nil
}

func (f *fakeReportGroupRepo) deliveryFor(reportID, groupID int64) (ReportPushDelivery, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.deliveries[reportID] {
		if d.GroupID == groupID {
			return d, true
		}
	}
	return ReportPushDelivery{}, false
}

type fakePushReportRepo struct {
	ReportRepository
	optIn      bool
	markCalls  int
	lastPushAt *time.Time
	lastErr    string
}

func (f *fakePushReportRepo) IsReportPushEnabled(ctx context.Context, userID int64) (bool, error) {
	return f.optIn, nil
}

func (f *fakePushReportRepo) MarkPushResult(ctx context.Context, id int64, pushedAt time.Time, pushErr string) error {
	f.markCalls++
	f.lastErr = pushErr
	if pushErr == "" {
		f.lastPushAt = &pushedAt
	}
	return nil
}

// newFeishuTestServer 返回一个记录调用次数、固定响应 code 的飞书 webhook 假服务。
func newFeishuTestServer(t *testing.T, code int, calls *int32, bodies *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if bodies != nil {
			raw, _ := io.ReadAll(r.Body)
			*bodies = append(*bodies, string(raw))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": "mock"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestService(reportRepo ReportRepository, groupRepo ReportGroupRepository) *ReportService {
	return NewReportService(reportRepo, groupRepo, nil)
}

func TestPushReportToUserGroupsRoutesAndDedupesWebhook(t *testing.T) {
	var callsA, callsB int32
	srvA := newFeishuTestServer(t, 0, &callsA, nil)
	srvB := newFeishuTestServer(t, 0, &callsB, nil)

	groupRepo := &fakeReportGroupRepo{groups: []ReportFeishuGroup{
		{ID: 1, Name: "计算群", WebhookURL: srvA.URL, Enabled: true},
		{ID: 2, Name: "数据群", WebhookURL: srvB.URL, Enabled: true},
		// 与计算群共用同一个机器人：只发一次，但两个群都记录结果
		{ID: 3, Name: "项目群", WebhookURL: srvA.URL, Enabled: true},
	}}
	reportRepo := &fakePushReportRepo{}
	svc := newTestService(reportRepo, groupRepo)
	report := &Report{
		ID:        99,
		UserID:    7,
		Type:      domain.ReportTypeDaily,
		Status:    domain.ReportStatusDone,
		AISummary: "# 工作日报（2026-09-17）- 张三\n\n## 一、今日核心工作\n1. 修复登录问题",
		// 模拟手动重推前 DTO 已带有历史投递记录：本次推送应覆盖而不是追加
		PushDeliveries: []ReportPushDelivery{{GroupID: 1, GroupName: "计算群", Status: domain.ReportPushStatusFailed}},
	}
	cfg := &ReportLLMConfig{FeishuEnabled: true}

	success, failed, err := svc.pushReportToUserGroups(context.Background(), cfg, report)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if success != 3 || failed != 0 {
		t.Fatalf("success=%d failed=%d, want 3/0", success, failed)
	}
	if got := atomic.LoadInt32(&callsA); got != 1 {
		t.Fatalf("webhook A calls = %d, want 1 (dedupe)", got)
	}
	if got := atomic.LoadInt32(&callsB); got != 1 {
		t.Fatalf("webhook B calls = %d, want 1", got)
	}
	if len(report.PushDeliveries) != 3 {
		t.Fatalf("deliveries = %d, want 3", len(report.PushDeliveries))
	}
	for _, gid := range []int64{1, 2, 3} {
		d, ok := groupRepo.deliveryFor(report.ID, gid)
		if !ok || d.Status != domain.ReportPushStatusSuccess {
			t.Fatalf("group %d delivery = %+v ok=%v", gid, d, ok)
		}
	}
	if reportRepo.lastPushAt == nil || reportRepo.lastErr != "" {
		t.Fatalf("mark push success not recorded: at=%v err=%q", reportRepo.lastPushAt, reportRepo.lastErr)
	}
}

func TestPushReportToUserGroupsIsolatesFailures(t *testing.T) {
	var callsOK, callsBad int32
	srvOK := newFeishuTestServer(t, 0, &callsOK, nil)
	srvBad := newFeishuTestServer(t, 1, &callsBad, nil)

	groupRepo := &fakeReportGroupRepo{groups: []ReportFeishuGroup{
		{ID: 1, Name: "计算群", WebhookURL: srvOK.URL, Enabled: true},
		{ID: 2, Name: "数据群", WebhookURL: srvBad.URL, Enabled: true},
	}}
	reportRepo := &fakePushReportRepo{}
	svc := newTestService(reportRepo, groupRepo)
	report := &Report{ID: 100, UserID: 7, Type: domain.ReportTypeDaily, Status: domain.ReportStatusDone}

	success, failed, err := svc.pushReportToUserGroups(context.Background(), &ReportLLMConfig{FeishuEnabled: true}, report)
	if err == nil {
		t.Fatal("expected aggregated error when one group fails")
	}
	if success != 1 || failed != 1 {
		t.Fatalf("success=%d failed=%d, want 1/1", success, failed)
	}
	if !strings.Contains(err.Error(), "数据群") {
		t.Fatalf("aggregated error should name failed group: %v", err)
	}
	if d, _ := groupRepo.deliveryFor(100, 1); d.Status != domain.ReportPushStatusSuccess {
		t.Fatalf("group 1 status = %q", d.Status)
	}
	if d, _ := groupRepo.deliveryFor(100, 2); d.Status != domain.ReportPushStatusFailed || d.Error == "" {
		t.Fatalf("group 2 status = %+v", d)
	}
	if reportRepo.lastPushAt != nil {
		t.Fatal("pushed_at should not be set on partial failure")
	}
	if !strings.Contains(reportRepo.lastErr, "数据群") {
		t.Fatalf("last_push_error = %q", reportRepo.lastErr)
	}
}

func TestPushReportToUserGroupsSkipsWhenNoGroup(t *testing.T) {
	groupRepo := &fakeReportGroupRepo{}
	reportRepo := &fakePushReportRepo{}
	svc := newTestService(reportRepo, groupRepo)
	report := &Report{ID: 101, UserID: 7, Status: domain.ReportStatusDone}

	success, failed, err := svc.pushReportToUserGroups(context.Background(), &ReportLLMConfig{FeishuEnabled: true}, report)
	if err != nil || success != 0 || failed != 0 {
		t.Fatalf("got success=%d failed=%d err=%v, want 0/0/nil", success, failed, err)
	}
	if reportRepo.markCalls != 0 {
		t.Fatalf("markPushResult called %d times, want 0", reportRepo.markCalls)
	}
}

func TestMaybeAutoPushFeishuHonoursOptInAndTypeSwitches(t *testing.T) {
	groupRepo := &fakeReportGroupRepo{groups: []ReportFeishuGroup{
		{ID: 1, Name: "计算群", WebhookURL: "http://127.0.0.1:1", Enabled: true},
	}}
	reportRepo := &fakePushReportRepo{optIn: false}
	svc := newTestService(reportRepo, groupRepo)
	report := &Report{ID: 102, UserID: 7, Type: domain.ReportTypeDaily, Status: domain.ReportStatusDone}

	// 用户未参与自动推送：不查群、不发
	svc.maybeAutoPushFeishu(context.Background(), &ReportLLMConfig{FeishuEnabled: true, FeishuPushDaily: true}, report)
	if atomic.LoadInt32(&groupRepo.calls) != 0 {
		t.Fatal("group lookup should be skipped when user opted out")
	}

	// 类型开关关闭：不查群
	reportRepo.optIn = true
	svc.maybeAutoPushFeishu(context.Background(), &ReportLLMConfig{FeishuEnabled: true, FeishuPushDaily: false}, report)
	if atomic.LoadInt32(&groupRepo.calls) != 0 {
		t.Fatal("group lookup should be skipped when type switch is off")
	}
}

// TestMaybeAutoPushFeishuSkipsAlreadyPushedReport 验证推送幂等：同周期报告此前
// 已推送成功（pushed_at 非空）时，定时路径不再重复刷群（Redis 标记丢失、leader
// 切换、节假日模式切换都会导致同周期再生成一次）。
func TestMaybeAutoPushFeishuSkipsAlreadyPushedReport(t *testing.T) {
	var calls int32
	srv := newFeishuTestServer(t, 0, &calls, nil)
	groupRepo := &fakeReportGroupRepo{groups: []ReportFeishuGroup{
		{ID: 1, Name: "计算群", WebhookURL: srv.URL, Enabled: true},
	}}
	reportRepo := &fakePushReportRepo{optIn: true}
	svc := newTestService(reportRepo, groupRepo)
	pushedAt := time.Date(2026, 11, 23, 19, 0, 0, 0, time.Local)
	report := &Report{
		ID:       103,
		UserID:   7,
		Type:     domain.ReportTypeDaily,
		Status:   domain.ReportStatusDone,
		PushedAt: &pushedAt,
	}

	svc.maybeAutoPushFeishu(context.Background(), &ReportLLMConfig{FeishuEnabled: true, FeishuPushDaily: true}, report)

	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("webhook calls = %d, want 0 (already pushed)", calls)
	}
	if atomic.LoadInt32(&groupRepo.calls) != 0 {
		t.Fatalf("group lookup calls = %d, want 0", groupRepo.calls)
	}
	if reportRepo.markCalls != 0 {
		t.Fatalf("markPushResult called %d times, want 0", reportRepo.markCalls)
	}
}

func TestTestFeishuGroupSendsTestCard(t *testing.T) {
	var bodies []string
	var calls int32
	srv := newFeishuTestServer(t, 0, &calls, &bodies)
	groupRepo := &fakeReportGroupRepo{}
	// 只实现 GetGroup
	groupRepo.groups = nil
	svc := newTestService(&fakePushReportRepo{}, &fakeGetGroupRepo{
		ReportGroupRepository: groupRepo,
		group:                 ReportFeishuGroup{ID: 5, Name: "计算群", WebhookURL: srv.URL, Enabled: true},
	})

	if err := svc.TestFeishuGroup(context.Background(), 5); err != nil {
		t.Fatalf("test push: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 || len(bodies) != 1 {
		t.Fatalf("calls=%d bodies=%d, want 1/1", calls, len(bodies))
	}
	if !strings.Contains(bodies[0], "报告群组测试") || !strings.Contains(bodies[0], "计算群") {
		t.Fatalf("test card body = %s", bodies[0])
	}
}

type fakeGetGroupRepo struct {
	ReportGroupRepository
	group ReportFeishuGroup
}

func (f *fakeGetGroupRepo) GetGroup(ctx context.Context, id int64) (*ReportFeishuGroup, error) {
	g := f.group
	return &g, nil
}

func TestCreateFeishuGroupValidation(t *testing.T) {
	svc := newTestService(&fakePushReportRepo{}, &fakeReportGroupRepo{})
	if _, err := svc.CreateFeishuGroup(context.Background(), &ReportFeishuGroup{Name: "计算群"}, nil); err == nil {
		t.Fatal("missing webhook should be rejected")
	}
	if _, err := svc.CreateFeishuGroup(context.Background(), &ReportFeishuGroup{WebhookURL: "https://x"}, nil); err == nil {
		t.Fatal("missing name should be rejected")
	}
}
