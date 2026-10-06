package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"vocat/internal/buildinfo"
	"vocat/internal/store"
	"vocat/internal/update"
)

const upstreamUpdateStateKey = "system.upstream_update_monitor"
const upstreamUpdateInterval = 24 * time.Hour

type upstreamUpdateState struct {
	LastChecked time.Time         `json:"last_checked"`
	Notified    map[string]string `json:"notified"`
}

type upstreamUpdateNotification struct {
	Title, Text, Notes, CurrentVersion, LatestVersion, ReleaseURL string
	Time                                                          time.Time
}

// StartUpstreamUpdateMonitor checks on startup when due, then once per day.
// Persisted timestamps and per-channel receipts survive service restarts.
func (s *Server) StartUpstreamUpdateMonitor(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.upstreamUpdateOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				if err := s.checkUpstreamUpdate(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
					s.logger.Warn("daily upstream update check", "error", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	})
}

func (s *Server) saveUpstreamUpdateState(ctx context.Context, state upstreamUpdateState) error {
	value, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.store.UpsertAppSetting(ctx, store.AppSetting{Key: upstreamUpdateStateKey, Value: value})
}

func (s *Server) checkUpstreamUpdate(ctx context.Context, now time.Time) error {
	s.upstreamUpdateMu.Lock()
	defer s.upstreamUpdateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	state := upstreamUpdateState{Notified: make(map[string]string)}
	setting, err := s.store.AppSetting(ctx, upstreamUpdateStateKey)
	if err == nil {
		if err := json.Unmarshal(setting.Value, &state); err != nil {
			return fmt.Errorf("decode upstream monitor state: %w", err)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if !state.LastChecked.IsZero() && now.Sub(state.LastChecked) < upstreamUpdateInterval {
		return nil
	}
	if state.Notified == nil {
		state.Notified = make(map[string]string)
	}
	if s.updateCheck == nil {
		return errors.New("upstream release checker unavailable")
	}
	// Record the attempt first, so restarts and API failures do not produce a
	// burst of checks. Failed deliveries are retried on the next daily check.
	state.LastChecked = now.UTC()
	if err := s.saveUpstreamUpdateState(ctx, state); err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	result, err := s.updateCheck(checkCtx, update.DefaultRepository, s.updateToken, buildinfo.Version)
	cancel()
	if err != nil {
		return err
	}
	if !result.Available {
		return nil
	}
	if strings.TrimSpace(result.Latest) == "" {
		return errors.New("upstream release has no version")
	}
	message := newUpstreamUpdateNotification(result, now)
	send := s.upstreamSend
	if send == nil {
		send = sendUpstreamUpdateNotification
	}
	for _, channel := range notificationChannels {
		if state.Notified[channel] == result.Latest {
			continue
		}
		setting, err := s.store.NotificationSetting(ctx, channel)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !setting.Enabled) {
			continue
		}
		if err != nil {
			s.logger.Warn("read upstream notification channel", "channel", channel, "error", err)
			continue
		}
		var config map[string]any
		if err := json.Unmarshal(setting.Config, &config); err != nil {
			s.logger.Warn("decode upstream notification channel", "channel", channel, "error", err)
			continue
		}
		sendCtx, cancel := context.WithTimeout(s.notificationDestinationContext(ctx), 30*time.Second)
		err = send(sendCtx, channel, config, message)
		cancel()
		if err != nil {
			s.logger.Warn("send upstream update notification", "channel", channel, "version", result.Latest, "error", err)
			continue
		}
		state.Notified[channel] = result.Latest
		if err := s.saveUpstreamUpdateState(ctx, state); err != nil {
			return err
		}
	}
	return nil
}

func newUpstreamUpdateNotification(result update.CheckResult, now time.Time) upstreamUpdateNotification {
	message := upstreamUpdateNotification{
		Title: "VoCat 上游有新版本", CurrentVersion: result.Current, LatestVersion: result.Latest,
		ReleaseURL: "https://github.com/" + update.DefaultRepository + "/releases/tag/v" + strings.TrimPrefix(result.Latest, "v"),
		Notes:      strings.TrimSpace(result.ReleaseNotes), Time: now.UTC(),
	}
	notes := message.Notes
	if notes == "" {
		notes = "上游未提供更新说明，请查看发布页面。"
	}
	// Leave room for the title, versions and links within WeCom's text limit.
	if len(notes) > 1200 {
		notes = notes[:1200]
		for !utf8.ValidString(notes) {
			notes = notes[:len(notes)-1]
		}
		notes += "\n（说明已截断，完整内容见发布页面）"
	}
	message.Text = strings.Join([]string{
		"当前版本：" + message.CurrentVersion,
		"上游新版本：" + message.LatestVersion,
		"请及时合并上游并重新构建此分支，保留自定义功能。",
		"发布页面：" + message.ReleaseURL,
		"上游差异：https://github.com/sdrpsps/VoCat/compare/master...MengMengCode:master",
		"", "更新说明：", notes,
	}, "\n")
	return message
}

func sendUpstreamUpdateNotification(ctx context.Context, channel string, config map[string]any, message upstreamUpdateNotification) error {
	switch channel {
	case "telegram":
		return sendTelegramTextNotification(ctx, config, message.Title+"\n"+message.Text)
	case "bark":
		return sendBarkTextNotification(ctx, config, message.Title, message.Text)
	case "email":
		return sendEmailTextNotification(ctx, config, message.Title, message.Text)
	case "pushplus":
		return sendPushplusTextNotification(ctx, config, message.Title, message.Text)
	case "meow":
		return meowNotificationSender(ctx, config, message.Title, message.Text)
	case "wecom", "lark":
		values := map[string]string{"event": "upstream.update_available", "title": message.Title,
			"message": message.Text, "content": message.Text, "timestamp": message.Time.Format(time.RFC3339),
			"time": message.Time.Local().Format("2006-01-02 15:04:05")}
		if channel == "wecom" {
			return sendWecomNotification(ctx, config, wecomTemplateValues(values))
		}
		return sendLarkNotification(ctx, config, larkTemplateValues(values))
	case "webhook":
		payload, err := message.webhookPayload()
		if err != nil {
			return err
		}
		return sendJSONWebhookNotification(ctx, config, payload, "vocat-upstream-update/1")
	default:
		return fmt.Errorf("unsupported upstream notification channel %q", channel)
	}
}

func (message upstreamUpdateNotification) webhookPayload() ([]byte, error) {
	return json.Marshal(map[string]any{"event": "upstream.update_available", "title": message.Title,
		"message": message.Text, "timestamp": message.Time.Format(time.RFC3339), "repository": update.DefaultRepository,
		"current_version": message.CurrentVersion, "latest_version": message.LatestVersion,
		"release_url": message.ReleaseURL, "release_notes": message.Notes, "merge_required": true})
}
