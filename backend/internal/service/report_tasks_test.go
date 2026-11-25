package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// =========================
// 测试替身：生成任务队列（内存实现）
// =========================

type fakeTaskQueueRepo struct {
	ReportRetryRepository

	mu     sync.Mutex
	nextID int64
	tasks  map[int64]*ReportRetryTask
}

func newFakeTaskQueueRepo() *fakeTaskQueueRepo {
	return &fakeTaskQueueRepo{tasks: map[int64]*ReportRetryTask{}}
}

func (f *fakeTaskQueueRepo) clone(t *ReportRetryTask) ReportRetryTask {
	c := *t
	return c
}

func (f *fakeTaskQueueRepo) InsertPending(_ context.Context, t *ReportRetryTask) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.tasks {
		if existing.Kind != domain.ReportRetryKindGeneration || existing.UserID != t.UserID ||
			existing.ReportType != t.ReportType || !existing.PeriodStart.Equal(t.PeriodStart) {
			continue
		}
		if existing.Status == domain.ReportRetryStatusPending || existing.Status == domain.ReportRetryStatusManualRepair {
			return false, nil
		}
	}
	f.nextID++
	c := *t
	c.ID = f.nextID
	c.Kind = domain.ReportRetryKindGeneration
	c.Status = domain.ReportRetryStatusPending
	c.CreatedAt = timezone.Now()
	c.UpdatedAt = c.CreatedAt
	f.tasks[c.ID] = &c
	return true, nil
}

func (f *fakeTaskQueueRepo) InsertCompleted(_ context.Context, t *ReportRetryTask) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.tasks {
		if existing.Kind == domain.ReportRetryKindGeneration && existing.UserID == t.UserID &&
			existing.ReportType == t.ReportType && existing.PeriodStart.Equal(t.PeriodStart) {
			return false, nil
		}
	}
	f.nextID++
	c := *t
	c.ID = f.nextID
	c.Kind = domain.ReportRetryKindGeneration
	c.Status = domain.ReportRetryStatusDone
	c.Outcome = domain.ReportTaskOutcomeAlreadyDone
	c.CreatedAt = timezone.Now()
	c.UpdatedAt = c.CreatedAt
	f.tasks[c.ID] = &c
	return true, nil
}

func (f *fakeTaskQueueRepo) ClaimGeneration(_ context.Context, workerID string, now time.Time, limit int, leaseTTL time.Duration) ([]ReportRetryTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]int64, 0, len(f.tasks))
	for id, t := range f.tasks {
		if t.Kind != domain.ReportRetryKindGeneration || t.Status != domain.ReportRetryStatusPending {
			continue
		}
		if t.NextRetryAt != nil && t.NextRetryAt.After(now) {
			continue
		}
		if t.LeaseUntil != nil && t.LeaseUntil.After(now) {
			continue
		}
		ids = append(ids, id)
	}
	sortInt64(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]ReportRetryTask, 0, len(ids))
	for _, id := range ids {
		t := f.tasks[id]
		lease := now.Add(leaseTTL)
		t.LeaseOwner = workerID
		t.LeaseUntil = &lease
		t.LastAttemptAt = &now
		out = append(out, *t)
	}
	return out, nil
}

func (f *fakeTaskQueueRepo) FinishGeneration(_ context.Context, id int64, status, outcome, lastError string, nextRetryAt *time.Time, reportID int64, consumeAttempt bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[id]
	if !ok {
		return fmt.Errorf("task %d not found", id)
	}
	t.Status = status
	t.Outcome = outcome
	t.LastError = lastError
	t.NextRetryAt = nextRetryAt
	if reportID > 0 {
		t.ReportID = reportID
	}
	if consumeAttempt {
		t.Attempts++
	}
	t.LeaseOwner = ""
	t.LeaseUntil = nil
	t.UpdatedAt = timezone.Now()
	return nil
}

func (f *fakeTaskQueueRepo) latestByUser(reportType string, periodStart time.Time) map[int64]*ReportRetryTask {
	out := map[int64]*ReportRetryTask{}
	for _, t := range f.tasks {
		if t.Kind != domain.ReportRetryKindGeneration || t.ReportType != reportType || !t.PeriodStart.Equal(periodStart) {
			continue
		}
		if cur, ok := out[t.UserID]; !ok || t.ID > cur.ID {
			out[t.UserID] = t
		}
	}
	return out
}

func (f *fakeTaskQueueRepo) SummarizeByPeriod(_ context.Context, reportType string, periodStart time.Time, now time.Time) (ReportTaskSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := ReportTaskSummary{Type: reportType, PeriodStart: periodStart}
	for _, t := range f.latestByUser(reportType, periodStart) {
		out.Total++
		switch t.Status {
		case domain.ReportRetryStatusDone:
			out.Done++
		case domain.ReportRetryStatusSkipped:
			out.Skipped++
		case domain.ReportRetryStatusFailed:
			out.Failed++
		case domain.ReportRetryStatusManualRepair:
			out.ManualRepair++
		case domain.ReportRetryStatusPending:
			if t.Running(now) {
				out.Running++
			} else {
				out.Pending++
			}
		}
	}
	return out, nil
}

func (f *fakeTaskQueueRepo) ListByPeriod(_ context.Context, reportType string, periodStart time.Time, statuses []string) ([]ReportTaskItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	allowed := map[string]struct{}{}
	for _, s := range statuses {
		allowed[s] = struct{}{}
	}
	out := make([]ReportTaskItem, 0)
	for userID, t := range f.latestByUser(reportType, periodStart) {
		if _, ok := allowed[t.Status]; !ok {
			continue
		}
		out = append(out, ReportTaskItem{
			UserID: userID, Username: fmt.Sprintf("user-%d", userID), Status: t.Status,
			Outcome: t.Outcome, Attempts: t.Attempts, LastError: t.LastError, NextRetryAt: t.NextRetryAt,
		})
	}
	sortItems(out)
	return out, nil
}

func (f *fakeTaskQueueRepo) ResetByPeriod(_ context.Context, reportType string, periodStart time.Time, statuses []string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	allowed := map[string]struct{}{}
	for _, s := range statuses {
		allowed[s] = struct{}{}
	}
	var reset int64
	for _, t := range f.latestByUser(reportType, periodStart) {
		if _, ok := allowed[t.Status]; !ok {
			continue
		}
		t.Status = domain.ReportRetryStatusPending
		t.Outcome = ""
		t.Attempts = 0
		t.LastError = ""
		t.NextRetryAt = nil
		reset++
	}
	return reset, nil
}

func sortInt64(v []int64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func sortItems(items []ReportTaskItem) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].UserID < items[j-1].UserID; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// =========================
// 测试替身：报告仓储（内存实现）
// =========================

type fakeTaskReportRepo struct {
	ReportRepository

	mu        sync.Mutex
	active    []int64
	generated map[int64]struct{}
	requests  map[int64]int64
	reports   map[int64]*Report
	createID  int64
}

func newFakeTaskReportRepo() *fakeTaskReportRepo {
	return &fakeTaskReportRepo{
		generated: map[int64]struct{}{},
		requests:  map[int64]int64{},
		reports:   map[int64]*Report{},
	}
}

func (f *fakeTaskReportRepo) ListActiveUserIDs(context.Context, time.Time, time.Time) ([]int64, error) {
	return append([]int64(nil), f.active...), nil
}

func (f *fakeTaskReportRepo) ListGeneratedUserIDs(context.Context, string, time.Time) (map[int64]struct{}, error) {
	out := make(map[int64]struct{}, len(f.generated))
	for k := range f.generated {
		out[k] = struct{}{}
	}
	return out, nil
}

func (f *fakeTaskReportRepo) AggregateUserUsage(ctx context.Context, userID int64, _, _ time.Time) (ReportStats, error) {
	if err := ctx.Err(); err != nil {
		return ReportStats{}, err
	}
	return ReportStats{Requests: f.requests[userID]}, nil
}

func (f *fakeTaskReportRepo) GetUsername(_ context.Context, userID int64) (string, error) {
	return fmt.Sprintf("user-%d", userID), nil
}

func (f *fakeTaskReportRepo) GetReportGoal(context.Context, int64) (string, error) { return "", nil }

func (f *fakeTaskReportRepo) FetchUserTurns(context.Context, int64, time.Time, time.Time, int) ([]UserPromptSnippet, error) {
	return nil, nil
}

func (f *fakeTaskReportRepo) FetchPromptSnapshots(context.Context, int64, time.Time, time.Time, int) ([]UserPromptSnippet, error) {
	return nil, nil
}

func (f *fakeTaskReportRepo) GetByUserPeriod(_ context.Context, userID int64, _ string, _ time.Time) (*Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reports[userID], nil
}

func (f *fakeTaskReportRepo) Create(_ context.Context, r *Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createID++
	r.ID = f.createID
	cp := *r
	f.reports[r.UserID] = &cp
	return nil
}

func (f *fakeTaskReportRepo) Update(_ context.Context, r *Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *r
	f.reports[r.UserID] = &cp
	return nil
}

func (f *fakeTaskReportRepo) IsReportPushEnabled(context.Context, int64) (bool, error) {
	return false, nil
}

func (f *fakeTaskReportRepo) MarkPushResult(context.Context, int64, time.Time, string) error {
	return nil
}

// =========================
// 用例
// =========================

func newTaskTestService(t *testing.T, reportRepo *fakeTaskReportRepo, queue *fakeTaskQueueRepo, settingRepo SettingRepository) *ReportService {
	t.Helper()
	if err := timezone.Init("Asia/Shanghai"); err != nil {
		t.Fatalf("timezone init: %v", err)
	}
	return NewReportServiceWithRetry(reportRepo, nil, queue, settingRepo)
}

func TestMaterializeReportTasksSkipsGeneratedUsers(t *testing.T) {
	reportRepo := newFakeTaskReportRepo()
	reportRepo.active = []int64{1, 2, 3}
	reportRepo.generated[2] = struct{}{}
	queue := newFakeTaskQueueRepo()
	svc := newTaskTestService(t, reportRepo, queue, nil)

	ref := time.Date(2026, 11, 24, 20, 0, 0, 0, timezone.Location())
	result, err := svc.MaterializeReportTasks(context.Background(), ReportTypeDaily, ref, ReportTriggerScheduled, false, true)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if result.Candidates != 3 || result.Created != 2 || result.Existing != 1 {
		t.Fatalf("result = %+v", result)
	}
	latest := queue.latestByUser(ReportTypeDaily, result.PeriodStart)
	if len(latest) != 3 {
		t.Fatalf("tasks = %d, want 3 (含已完成用户的 done 台账)", len(latest))
	}
	if latest[2].Status != domain.ReportRetryStatusDone || latest[2].Outcome != domain.ReportTaskOutcomeAlreadyDone {
		t.Fatalf("generated user task = %+v", latest[2])
	}
	// 重复上账必须幂等：不产生第二条 pending。
	again, err := svc.MaterializeReportTasks(context.Background(), ReportTypeDaily, ref, ReportTriggerScheduled, false, true)
	if err != nil {
		t.Fatalf("materialize again: %v", err)
	}
	if again.Created != 0 {
		t.Fatalf("second materialize created %d, want 0", again.Created)
	}
}

func TestMaterializeReportTasksManualKeepsRegenerateSemantics(t *testing.T) {
	reportRepo := newFakeTaskReportRepo()
	reportRepo.active = []int64{1}
	reportRepo.generated[1] = struct{}{}
	queue := newFakeTaskQueueRepo()
	svc := newTaskTestService(t, reportRepo, queue, nil)

	ref := time.Date(2026, 11, 24, 20, 0, 0, 0, timezone.Location())
	result, err := svc.MaterializeReportTasks(context.Background(), ReportTypeDaily, ref, ReportTriggerManual, false, false)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if result.Created != 1 {
		t.Fatalf("manual materialize created = %d, want 1 (已有报告也要重生成)", result.Created)
	}
	if task := queue.latestByUser(ReportTypeDaily, result.PeriodStart)[1]; task.Status != domain.ReportRetryStatusPending {
		t.Fatalf("task = %+v", task)
	}
}

func TestProcessReportTaskOutcomes(t *testing.T) {
	periodStart := time.Date(2026, 11, 24, 0, 0, 0, 0, timezone.Location())

	t.Run("success marks done", func(t *testing.T) {
		reportRepo := newFakeTaskReportRepo()
		reportRepo.requests[1] = 42
		queue := newFakeTaskQueueRepo()
		svc := newTaskTestService(t, reportRepo, queue, nil)
		if _, err := queue.InsertPending(context.Background(), &ReportRetryTask{
			UserID: 1, ReportType: ReportTypeDaily, PeriodStart: periodStart,
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
		task := queue.tasks[1]
		outcome := svc.ProcessReportTask(context.Background(), *task)
		if outcome.kind != domain.ReportRetryStatusDone {
			t.Fatalf("outcome = %+v", outcome)
		}
		if queue.tasks[1].Outcome != domain.ReportTaskOutcomeGenerated || queue.tasks[1].Status != domain.ReportRetryStatusDone {
			t.Fatalf("task = %+v", queue.tasks[1])
		}
	})

	t.Run("no usage marks skipped", func(t *testing.T) {
		reportRepo := newFakeTaskReportRepo()
		queue := newFakeTaskQueueRepo()
		svc := newTaskTestService(t, reportRepo, queue, nil)
		_, _ = queue.InsertPending(context.Background(), &ReportRetryTask{UserID: 2, ReportType: ReportTypeDaily, PeriodStart: periodStart})
		svc.ProcessReportTask(context.Background(), *queue.tasks[1])
		if queue.tasks[1].Status != domain.ReportRetryStatusSkipped || queue.tasks[1].Outcome != domain.ReportTaskOutcomeNoUsage {
			t.Fatalf("task = %+v", queue.tasks[1])
		}
	})
}

func TestProcessReportTaskReschedulesTransientAndStopsOnPermanent(t *testing.T) {
	periodStart := time.Date(2026, 11, 24, 0, 0, 0, 0, timezone.Location())
	cases := []struct {
		name        string
		statusCode  int
		wantStatus  string
		wantOutcome string
	}{
		{"500 reschedules", http.StatusInternalServerError, domain.ReportRetryStatusPending, ""},
		{"404 needs manual repair", http.StatusNotFound, domain.ReportRetryStatusManualRepair, domain.ReportTaskOutcomePermanent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(`{"error":{"message":"mock"}}`))
			}))
			defer srv.Close()

			cfg, _ := json.Marshal(map[string]any{
				"enabled": true, "base_url": srv.URL, "api_key": "test-key",
				"model": "mock-model", "skip_holidays": true,
			})
			settings := &fakeSettingRepo{vals: map[string]string{SettingKeyReportConfig: string(cfg)}}
			reportRepo := newFakeTaskReportRepo()
			reportRepo.requests[3] = 20
			queue := newFakeTaskQueueRepo()
			svc := newTaskTestService(t, reportRepo, queue, settings)
			_, _ = queue.InsertPending(context.Background(), &ReportRetryTask{UserID: 3, ReportType: ReportTypeDaily, PeriodStart: periodStart})

			svc.ProcessReportTask(context.Background(), *queue.tasks[1])
			task := queue.tasks[1]
			if task.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (%+v)", task.Status, tc.wantStatus, task)
			}
			if task.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %q, want %q", task.Outcome, tc.wantOutcome)
			}
			if tc.wantStatus == domain.ReportRetryStatusPending {
				if task.NextRetryAt == nil || task.Attempts != 1 {
					t.Fatalf("retry task = %+v", task)
				}
			}
		})
	}
}

func TestProcessReportTaskExhaustsAttempts(t *testing.T) {
	periodStart := time.Date(2026, 11, 24, 0, 0, 0, 0, timezone.Location())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	cfg, _ := json.Marshal(map[string]any{"enabled": true, "base_url": srv.URL, "api_key": "k", "model": "m"})
	settings := &fakeSettingRepo{vals: map[string]string{SettingKeyReportConfig: string(cfg)}}
	reportRepo := newFakeTaskReportRepo()
	reportRepo.requests[4] = 30
	queue := newFakeTaskQueueRepo()
	svc := newTaskTestService(t, reportRepo, queue, settings)
	_, _ = queue.InsertPending(context.Background(), &ReportRetryTask{UserID: 4, ReportType: ReportTypeDaily, PeriodStart: periodStart})
	queue.tasks[1].Attempts = reportTaskMaxAttempts - 1

	svc.ProcessReportTask(context.Background(), *queue.tasks[1])
	if queue.tasks[1].Status != domain.ReportRetryStatusFailed || queue.tasks[1].Outcome != domain.ReportTaskOutcomeAttempts {
		t.Fatalf("task = %+v", queue.tasks[1])
	}
}

func TestProcessReportTaskCanceledContextDoesNotConsumeAttempt(t *testing.T) {
	periodStart := time.Date(2026, 11, 24, 0, 0, 0, 0, timezone.Location())
	reportRepo := newFakeTaskReportRepo()
	reportRepo.requests[5] = 10
	queue := newFakeTaskQueueRepo()
	svc := newTaskTestService(t, reportRepo, queue, nil)
	_, _ = queue.InsertPending(context.Background(), &ReportRetryTask{UserID: 5, ReportType: ReportTypeDaily, PeriodStart: periodStart})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.ProcessReportTask(ctx, *queue.tasks[1])

	task := queue.tasks[1]
	if task.Status != domain.ReportRetryStatusPending || task.Attempts != 0 {
		t.Fatalf("task = %+v", task)
	}
	if task.NextRetryAt == nil || task.NextRetryAt.Sub(timezone.Now()) > 5*time.Minute {
		t.Fatalf("next retry = %v, want ~1m", task.NextRetryAt)
	}
}

func TestRunGenerationTasksProcessesAllAndRespectsBudget(t *testing.T) {
	reportRepo := newFakeTaskReportRepo()
	for i := int64(1); i <= 6; i++ {
		reportRepo.active = append(reportRepo.active, i)
		reportRepo.requests[i] = 15
	}
	queue := newFakeTaskQueueRepo()
	svc := newTaskTestService(t, reportRepo, queue, nil)
	ref := time.Date(2026, 11, 24, 20, 0, 0, 0, timezone.Location())
	if _, err := svc.MaterializeReportTasks(context.Background(), ReportTypeDaily, ref, ReportTriggerManual, false, false); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	result, err := svc.RunGenerationTasks(context.Background(), 2*time.Minute, 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Generated != 6 || result.Processed != 6 {
		t.Fatalf("result = %+v", result)
	}
	summary, err := svc.ReportTaskSummaryFor(context.Background(), ReportTypeDaily, ref)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Total != 6 || summary.Done != 6 || len(summary.Items) != 0 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestRunGenerationTasksLeavesTasksPendingWhenBudgetIsZero(t *testing.T) {
	reportRepo := newFakeTaskReportRepo()
	reportRepo.active = []int64{1, 2, 3}
	for _, uid := range reportRepo.active {
		reportRepo.requests[uid] = 11
	}
	queue := newFakeTaskQueueRepo()
	svc := newTaskTestService(t, reportRepo, queue, nil)
	ref := time.Date(2026, 11, 24, 20, 0, 0, 0, timezone.Location())
	if _, err := svc.MaterializeReportTasks(context.Background(), ReportTypeDaily, ref, ReportTriggerScheduled, false, true); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	result, err := svc.RunGenerationTasks(context.Background(), time.Nanosecond, 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Processed != 0 {
		t.Fatalf("processed = %d, want 0", result.Processed)
	}
	for _, task := range queue.tasks {
		if task.Status != domain.ReportRetryStatusPending {
			t.Fatalf("task status = %q, want pending", task.Status)
		}
		if task.LeaseUntil != nil {
			t.Fatalf("task %d should not hold a lease", task.ID)
		}
	}
}

func TestResetReportTasksRequeuesOnlyUnfinished(t *testing.T) {
	periodStart := time.Date(2026, 11, 24, 0, 0, 0, 0, timezone.Location())
	queue := newFakeTaskQueueRepo()
	svc := newTaskTestService(t, newFakeTaskReportRepo(), queue, nil)
	ref := time.Date(2026, 11, 24, 20, 0, 0, 0, timezone.Location())

	for _, tc := range []struct {
		user   int64
		status string
	}{
		{1, domain.ReportRetryStatusManualRepair},
		{2, domain.ReportRetryStatusFailed},
		{3, domain.ReportRetryStatusDone},
	} {
		_, _ = queue.InsertPending(context.Background(), &ReportRetryTask{UserID: tc.user, ReportType: ReportTypeDaily, PeriodStart: periodStart})
		queue.tasks[int64(len(queue.tasks))].Status = tc.status
	}

	reset, err := svc.ResetReportTasks(context.Background(), ReportTypeDaily, ref, nil)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if reset != 2 {
		t.Fatalf("reset = %d, want 2", reset)
	}
	if queue.tasks[1].Status != domain.ReportRetryStatusPending || queue.tasks[2].Status != domain.ReportRetryStatusPending {
		t.Fatalf("tasks = %+v / %+v", queue.tasks[1], queue.tasks[2])
	}
	if queue.tasks[3].Status != domain.ReportRetryStatusDone {
		t.Fatalf("done task must stay done, got %q", queue.tasks[3].Status)
	}
}
