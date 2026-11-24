package service

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

type fakeRetryRepo struct {
	ReportRetryRepository
	mu         sync.Mutex
	generation []ReportRetryTask
	push       []ReportRetryTask
}

func (f *fakeRetryRepo) UpsertGeneration(ctx context.Context, t *ReportRetryTask) (*ReportRetryTask, error) {
	c := *t
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generation = append(f.generation, c)
	return &c, nil
}

func (f *fakeRetryRepo) UpsertPush(ctx context.Context, t *ReportRetryTask) (*ReportRetryTask, error) {
	c := *t
	f.mu.Lock()
	defer f.mu.Unlock()
	f.push = append(f.push, c)
	return &c, nil
}

func (f *fakeRetryRepo) MarkDone(context.Context, int64) error { return nil }
func (f *fakeRetryRepo) MarkDoneByGeneration(context.Context, int64, string, time.Time) error {
	return nil
}
func (f *fakeRetryRepo) MarkDoneByPush(context.Context, int64, int64) error { return nil }
func (f *fakeRetryRepo) ListDue(context.Context, string, time.Time, int) ([]ReportRetryTask, error) {
	return nil, nil
}
func (f *fakeRetryRepo) ListActiveByReportIDs(context.Context, []int64) (map[int64][]ReportRetryTask, error) {
	return nil, nil
}
func (f *fakeRetryRepo) GetByID(context.Context, int64) (*ReportRetryTask, error) {
	return nil, domain.ErrReportNotFound
}

func TestPushReportToMissingGroupsSkipsSuccessfulGroupsAndWebhook(t *testing.T) {
	var callsA, callsB int32
	srvA := newFeishuTestServer(t, 0, &callsA, nil)
	srvB := newFeishuTestServer(t, 0, &callsB, nil)

	groupRepo := &fakeReportGroupRepo{
		groups: []ReportFeishuGroup{
			{ID: 1, Name: "计算群", WebhookURL: srvA.URL, Enabled: true},
			{ID: 2, Name: "AI学科事业部", WebhookURL: srvB.URL, Enabled: true},
		},
		deliveries: map[int64][]ReportPushDelivery{
			200: {{ReportID: 200, GroupID: 1, GroupName: "计算群", Status: domain.ReportPushStatusSuccess}},
		},
	}
	reportRepo := &fakePushReportRepo{}
	svc := NewReportServiceWithRetry(reportRepo, groupRepo, &fakeRetryRepo{}, nil)
	report := &Report{ID: 200, UserID: 7, Type: domain.ReportTypeDaily, Status: domain.ReportStatusDone}

	success, failed, err := svc.pushReportToMissingGroups(context.Background(), &ReportLLMConfig{FeishuEnabled: true}, report)
	if err != nil || failed != 0 {
		t.Fatalf("got success=%d failed=%d err=%v", success, failed, err)
	}
	if got := atomic.LoadInt32(&callsA); got != 0 {
		t.Fatalf("already-successful group webhook called %d times, want 0", got)
	}
	if got := atomic.LoadInt32(&callsB); got != 1 {
		t.Fatalf("failed group webhook called %d times, want 1", got)
	}
	if d, _ := groupRepo.deliveryFor(200, 2); d.Status != domain.ReportPushStatusSuccess {
		t.Fatalf("group 2 status = %q", d.Status)
	}
	if reportRepo.lastPushAt == nil {
		t.Fatal("pushed_at should be set when all groups succeed")
	}
}

func TestPushReportToMissingGroupsDedupesSharedWebhook(t *testing.T) {
	var calls int32
	srv := newFeishuTestServer(t, 0, &calls, nil)
	groupRepo := &fakeReportGroupRepo{
		groups: []ReportFeishuGroup{
			{ID: 1, Name: "计算群", WebhookURL: srv.URL, Enabled: true},
			{ID: 2, Name: "项目群", WebhookURL: srv.URL, Enabled: true},
		},
		deliveries: map[int64][]ReportPushDelivery{
			201: {{ReportID: 201, GroupID: 1, GroupName: "计算群", Status: domain.ReportPushStatusSuccess}},
		},
	}
	svc := NewReportServiceWithRetry(&fakePushReportRepo{}, groupRepo, &fakeRetryRepo{}, nil)
	report := &Report{ID: 201, UserID: 7, Type: domain.ReportTypeDaily, Status: domain.ReportStatusDone}

	if _, _, err := svc.pushReportToMissingGroups(context.Background(), &ReportLLMConfig{FeishuEnabled: true}, report); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("shared webhook called %d times, want 0", got)
	}
	if d, _ := groupRepo.deliveryFor(201, 2); d.Status != domain.ReportPushStatusSuccess {
		t.Fatalf("group 2 should be marked success without resending, got %q", d.Status)
	}
}

func TestPushRetryClassification(t *testing.T) {
	transient := newReportFeishuError(500, 0, "server error")
	if reportFeishuErrorPermanent(transient) {
		t.Fatal("5xx should be transient")
	}
	permanent := newReportFeishuError(404, 0, "webhook not found")
	if !reportFeishuErrorPermanent(permanent) {
		t.Fatal("404 should be permanent")
	}
	if !reportFeishuErrorPermanent(newReportFeishuError(200, 19001, "invalid webhook")) {
		t.Fatal("business code should be permanent")
	}
	if reportFeishuErrorPermanent(newReportFeishuError(200, 9499, "rate limited")) {
		t.Fatal("9499 should be transient")
	}
}

func TestSchedulePushRetryWritesPendingOrManualRepair(t *testing.T) {
	repo := &fakeRetryRepo{}
	svc := NewReportServiceWithRetry(nil, nil, repo, nil)
	report := &Report{ID: 300, UserID: 7, Type: domain.ReportTypeDaily, PeriodStart: time.Now()}
	group := ReportFeishuGroup{ID: 9, Name: "计算群"}

	svc.schedulePushRetry(report, group, newReportFeishuError(500, 0, "server error"))
	if len(repo.push) != 1 || repo.push[0].Status != domain.ReportRetryStatusPending {
		t.Fatalf("transient push task = %+v", repo.push)
	}
	if repo.push[0].NextRetryAt == nil || repo.push[0].NextRetryAt.Sub(time.Now()) < 25*time.Minute {
		t.Fatalf("next retry = %v, want ~30m", repo.push[0].NextRetryAt)
	}

	svc.schedulePushRetry(report, group, newReportFeishuError(404, 0, "not found"))
	if len(repo.push) != 2 || repo.push[1].Status != domain.ReportRetryStatusManualRepair || repo.push[1].NextRetryAt != nil {
		t.Fatalf("permanent push task = %+v", repo.push[1])
	}
}

func TestReportLLMErrorClassification(t *testing.T) {
	if !reportLLMErrorPermanent(newReportLLMCallError(401, "bad key", true)) {
		t.Fatal("401 should be permanent")
	}
	if reportLLMErrorPermanent(newReportLLMCallError(0, "timeout", false)) {
		t.Fatal("network timeout should be transient")
	}
}

func TestScheduleGenerationRetryRespectsAutoPush(t *testing.T) {
	repo := &fakeRetryRepo{}
	svc := NewReportServiceWithRetry(nil, nil, repo, nil)
	report := &Report{ID: 400, UserID: 7, Type: domain.ReportTypeDaily, PeriodStart: time.Now()}

	svc.scheduleGenerationRetry(context.Background(), report, newReportLLMCallError(500, "server error", false), true)
	if len(repo.generation) != 1 {
		t.Fatalf("generation tasks = %d", len(repo.generation))
	}
	task := repo.generation[0]
	if task.Status != domain.ReportRetryStatusPending || !task.AutoPush || task.NextRetryAt == nil {
		t.Fatalf("task = %+v", task)
	}
	if !strings.Contains(task.LastError, "REPORT_LLM_CALL_FAILED") {
		t.Fatalf("last error = %q", task.LastError)
	}

	svc.scheduleGenerationRetry(context.Background(), report, newReportLLMCallError(404, "model not found", true), false)
	if len(repo.generation) != 2 || repo.generation[1].Status != domain.ReportRetryStatusManualRepair || repo.generation[1].AutoPush {
		t.Fatalf("permanent task = %+v", repo.generation[1])
	}
}
