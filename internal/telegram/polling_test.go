package telegram

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/applog"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type flakyUpdateClient struct {
	fakeUpdateClient
	mu        sync.Mutex
	calls     int
	failTimes int
}

func (f *flakyUpdateClient) GetUpdates(config tgbotapi.UpdateConfig) ([]tgbotapi.Update, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failTimes {
		return nil, errors.New("network down")
	}
	// Return one update then block-ish via context cancellation in test.
	if f.calls == f.failTimes+1 {
		return []tgbotapi.Update{
			commandUpdate(1, 1, "/help"),
		}, nil
	}
	// Subsequent polls: sleep a bit so Run can be cancelled.
	time.Sleep(20 * time.Millisecond)
	return nil, nil
}

func TestRunLogsPollingFailuresThroughApplog(t *testing.T) {
	var buf bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		_ = applog.Configure("INFO")
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	if err := applog.Configure("WARNING"); err != nil {
		t.Fatalf("configure: %v", err)
	}
	log.SetOutput(&buf)
	log.SetFlags(0)

	api := &flakyUpdateClient{failTimes: 1}
	bot := newTestBot(t, api, &fakeRevoker{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bot.Run(ctx)
	}()

	// Wait until help reply is sent (means recovery after the 3s retry delay).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if texts := api.texts(); len(texts) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Run exit")
	}

	out := buf.String()
	if !strings.Contains(out, "WARNING Failed to get Telegram updates") {
		t.Fatalf("missing polling failure log: %q", out)
	}
	if !strings.Contains(out, "Retrying Telegram updates in 3 seconds") {
		t.Fatalf("missing retry log: %q", out)
	}
	texts := api.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "Available commands:") {
		t.Fatalf("expected /help reply after recovery, got %v", texts)
	}
}

func TestRunHonorsErrorLogLevelForPollingFailures(t *testing.T) {
	var buf bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		_ = applog.Configure("INFO")
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	if err := applog.Configure("ERROR"); err != nil {
		t.Fatalf("configure: %v", err)
	}
	log.SetOutput(&buf)
	log.SetFlags(0)

	api := &flakyUpdateClient{failTimes: 1}
	bot := newTestBot(t, api, &fakeRevoker{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = bot.Run(ctx)

	if out := buf.String(); strings.Contains(out, "Failed to get Telegram updates") {
		t.Fatalf("polling warnings should be filtered at ERROR level: %q", out)
	}
}
