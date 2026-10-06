package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"vocat/internal/buildinfo"
	"vocat/internal/store"
	"vocat/internal/update"
)

func enableUpstreamTestChannel(t *testing.T, s *Server, channel string, enabled bool) {
	t.Helper()
	if err := s.store.UpsertNotificationSetting(context.Background(), store.NotificationSetting{
		Channel: channel, Enabled: enabled, Config: json.RawMessage(`{"test":"saved-config"}`),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamDailyChecksAndReceiptsSurviveRestart(t *testing.T) {
	s := newSettingsAPITest(t).server
	for _, channel := range notificationChannels {
		enableUpstreamTestChannel(t, s, channel, true)
	}
	checks := 0
	latest := "9.9.9"
	checker := func(_ context.Context, repo, token, current string) (update.CheckResult, error) {
		checks++
		if repo != update.DefaultRepository || token != "test-token" || current != buildinfo.Version {
			t.Fatalf("unexpected check arguments: %q %q %q", repo, token, current)
		}
		return update.CheckResult{Available: true, Current: current, Latest: latest, ReleaseNotes: "修复问题并增加功能"}, nil
	}
	sent := map[string]int{}
	sender := func(_ context.Context, channel string, config map[string]any, message upstreamUpdateNotification) error {
		if config["test"] != "saved-config" || !strings.Contains(message.Text, "修复问题并增加功能") || !strings.Contains(message.Text, "合并上游") {
			t.Fatalf("missing existing config or update details: %#v %s", config, message.Text)
		}
		sent[channel]++
		return nil
	}
	s.updateCheck, s.upstreamSend, s.updateToken = checker, sender, "test-token"
	now := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	if err := s.checkUpstreamUpdate(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	// A new server instance uses the persisted state from the same database.
	restarted := &Server{store: s.store, logger: s.logger, updateCheck: checker, upstreamSend: sender, updateToken: "test-token"}
	for _, delta := range []time.Duration{time.Minute, 23 * time.Hour, 24 * time.Hour} {
		if err := restarted.checkUpstreamUpdate(context.Background(), now.Add(delta)); err != nil {
			t.Fatal(err)
		}
	}
	if checks != 2 {
		t.Fatalf("checked %d times, expected once per 24 hours", checks)
	}
	for _, channel := range notificationChannels {
		if sent[channel] != 1 {
			t.Fatalf("duplicate/missing notification on %s: %d", channel, sent[channel])
		}
	}
	latest = "9.9.10"
	if err := restarted.checkUpstreamUpdate(context.Background(), now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, channel := range notificationChannels {
		if sent[channel] != 2 {
			t.Fatalf("new release not notified on %s", channel)
		}
	}
}

func TestUpstreamFailedDeliveriesRetryWithoutRepeatingSuccessfulChannels(t *testing.T) {
	s := newSettingsAPITest(t).server
	enableUpstreamTestChannel(t, s, "webhook", true)
	enableUpstreamTestChannel(t, s, "meow", true)
	enableUpstreamTestChannel(t, s, "bark", false)
	s.updateCheck = func(context.Context, string, string, string) (update.CheckResult, error) {
		return update.CheckResult{Available: true, Current: "0.3.11", Latest: "0.3.12"}, nil
	}
	sent := map[string]int{}
	s.upstreamSend = func(_ context.Context, channel string, _ map[string]any, _ upstreamUpdateNotification) error {
		sent[channel]++
		if channel == "webhook" && sent[channel] == 1 {
			return errors.New("temporary provider failure")
		}
		return nil
	}
	now := time.Now().UTC()
	if err := s.checkUpstreamUpdate(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	enableUpstreamTestChannel(t, s, "bark", true)
	for _, days := range []int{1, 2} {
		if err := s.checkUpstreamUpdate(context.Background(), now.Add(time.Duration(days)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if sent["webhook"] != 2 || sent["meow"] != 1 || sent["bark"] != 1 {
		t.Fatalf("unexpected retries/disabled-channel handling: %#v", sent)
	}
}

func TestUpstreamAPIFailureDoesNotCauseCheckBurstOrNotification(t *testing.T) {
	s := newSettingsAPITest(t).server
	enableUpstreamTestChannel(t, s, "webhook", true)
	checks := 0
	s.updateCheck = func(context.Context, string, string, string) (update.CheckResult, error) {
		checks++
		return update.CheckResult{}, errors.New("GitHub unavailable")
	}
	s.upstreamSend = func(context.Context, string, map[string]any, upstreamUpdateNotification) error {
		t.Fatal("API failure must not trigger a notification")
		return nil
	}
	now := time.Now().UTC()
	if err := s.checkUpstreamUpdate(context.Background(), now); err == nil {
		t.Fatal("expected API error")
	}
	if err := s.checkUpstreamUpdate(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if checks != 1 {
		t.Fatal("failed check repeated before daily interval")
	}
	s.updateCheck = func(context.Context, string, string, string) (update.CheckResult, error) {
		return update.CheckResult{Available: false}, nil
	}
	if err := s.checkUpstreamUpdate(context.Background(), now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamUpdateMessageLimitsAndMissingNotes(t *testing.T) {
	for _, notes := range []string{"", strings.Repeat("更新内容🙂", 500)} {
		message := newUpstreamUpdateNotification(update.CheckResult{Current: "0.3.11", Latest: "0.3.12", ReleaseNotes: notes}, time.Now())
		if len(message.Text) > 2000 || !utf8.ValidString(message.Text) || !strings.Contains(message.Text, "https://github.com/MengMengCode/VoCat/releases/tag/v0.3.12") {
			t.Fatalf("invalid or oversized message (%d bytes)", len(message.Text))
		}
		if notes == "" && !strings.Contains(message.Text, "未提供更新说明") {
			t.Fatal("missing release notes fallback")
		}
	}
}

func TestUpstreamWebhookIncludesReleaseDetailsAndSignature(t *testing.T) {
	message := newUpstreamUpdateNotification(update.CheckResult{Current: "0.3.11", Latest: "0.3.12", ReleaseNotes: "修复蜂窝连接"}, time.Now())
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte("test-secret"))
		mac.Write(body)
		if r.Header.Get("X-vocat-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || r.Header.Get("X-Test") != "configured" {
			t.Error("existing webhook signing/header configuration was lost")
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Error(err)
		}
		if payload["event"] != "upstream.update_available" || payload["latest_version"] != "0.3.12" || payload["merge_required"] != true || payload["release_notes"] != "修复蜂窝连接" {
			t.Errorf("invalid upstream webhook: %#v", payload)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()
	config := map[string]any{"urls": []any{provider.URL}, "secret": "test-secret", "headers": map[string]any{"X-Test": "configured"}}
	payload, err := message.webhookPayload()
	if err != nil {
		t.Fatal(err)
	}
	request, err := newSignedJSONWebhookRequest(context.Background(), provider.URL, config, payload, "vocat-upstream-update/1")
	if err != nil {
		t.Fatal(err)
	}
	if err := performNotificationRequest(provider.Client(), request, false); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("webhook deliveries = %d", requests.Load())
	}
}

func TestUpstreamConcurrentChecksAndCancellation(t *testing.T) {
	s := newSettingsAPITest(t).server
	var checks atomic.Int32
	s.updateCheck = func(context.Context, string, string, string) (update.CheckResult, error) {
		checks.Add(1)
		return update.CheckResult{Available: false}, nil
	}
	now := time.Now().UTC()
	var group sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		group.Add(1)
		go func() { defer group.Done(); failures <- s.checkUpstreamUpdate(context.Background(), now) }()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if checks.Load() != 1 {
		t.Fatalf("concurrent check count=%d", checks.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.checkUpstreamUpdate(ctx, now.Add(24*time.Hour)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled check=%v", err)
	}
	if checks.Load() != 1 {
		t.Fatal("cancelled check contacted upstream")
	}
}
