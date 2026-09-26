package application_test

import (
	"testing"
	"time"

	"github.com/vishen/go-chromecast/application"
	"github.com/vishen/go-chromecast/simulator"
)

// Exercise the public application API over real TLS, without a mocked Conn.
func TestApplicationWithSimulator(t *testing.T) {
	receiver, err := simulator.New(simulator.Config{Duration: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	app := application.NewApplication(application.WithCacheDisabled(true), application.WithConnectionRetries(1))
	if err := app.Start("127.0.0.1", receiver.Port()); err != nil {
		t.Fatal(err)
	}
	defer app.Close(false)
	idle, _, _ := app.Status()
	if idle == nil || !idle.IsIdleScreen {
		t.Fatalf("expected affirmative idle screen, got %+v", idle)
	}
	if err := app.LoadRepeating("http://example.invalid/video.mp4", "video/mp4", false); err != nil {
		t.Fatal(err)
	}
	if err := app.Update(); err != nil {
		t.Fatal(err)
	}
	running, media, _ := app.Status()
	if running == nil || running.DisplayName != "Default Media Receiver" {
		t.Fatalf("media receiver identity: %+v", running)
	}
	if media == nil || media.Media.ContentId != "http://example.invalid/video.mp4" || media.PlayerState != "PLAYING" {
		t.Fatalf("media=%+v", media)
	}
	if err := app.Pause(); err != nil {
		t.Fatal(err)
	}
	if err := app.Update(); err != nil {
		t.Fatal(err)
	}
	if state := receiver.Snapshot(); state.PlayerState != "PAUSED" {
		t.Fatalf("pause: %+v", state)
	}
	if err := app.Unpause(); err != nil {
		t.Fatal(err)
	}
	if err := app.Update(); err != nil {
		t.Fatal(err)
	}
	if err := app.StopMedia(); err != nil {
		t.Fatal(err)
	}
	if err := app.Update(); err != nil {
		t.Fatal(err)
	}
	if state := receiver.Snapshot(); state.PlayerState != "IDLE" {
		t.Fatalf("stop: %+v", state)
	}
}
