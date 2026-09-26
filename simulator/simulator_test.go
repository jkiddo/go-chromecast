package simulator_test

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/vishen/go-chromecast/cast"
	"github.com/vishen/go-chromecast/simulator"
)

const receiverNS = "urn:x-cast:com.google.cast.receiver"
const mediaNS = "urn:x-cast:com.google.cast.media"

type client struct {
	conn *cast.Connection
	id   int
	t    *testing.T
}

func connect(t *testing.T, r *simulator.Receiver) *client {
	t.Helper()
	c := cast.NewConnection()
	if err := c.Start("127.0.0.1", r.Port()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &client{conn: c, t: t}
}
func (c *client) request(namespace string, payload cast.Payload) map[string]any {
	c.t.Helper()
	c.id++
	payload.SetRequestId(c.id)
	destination := "receiver-0"
	if namespace == mediaNS {
		destination = "simulator-transport"
	}
	if err := c.conn.Send(c.id, payload, "sender-test", destination, namespace); err != nil {
		c.t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case msg := <-c.conn.MsgChan():
			if msg == nil {
				c.t.Fatal("connection closed")
			}
			var reply map[string]any
			if err := json.Unmarshal([]byte(msg.GetPayloadUtf8()), &reply); err != nil {
				c.t.Fatal(err)
			}
			if reply["requestId"] == float64(c.id) {
				return reply
			}
		case <-deadline:
			c.t.Fatal("receiver did not answer")
		}
	}
}

func TestClientPlaybackLifecycle(t *testing.T) {
	r, err := simulator.New(simulator.Config{Duration: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c := connect(t, r)
	reply := c.request(receiverNS, &cast.LaunchRequest{PayloadHeader: cast.PayloadHeader{Type: "LAUNCH"}, AppId: "CC1AD845"})
	if reply["type"] != "RECEIVER_STATUS" {
		t.Fatal(reply)
	}
	load := &cast.LoadMediaCommand{PayloadHeader: cast.PayloadHeader{Type: "LOAD"}, Autoplay: true, Media: cast.MediaItem{ContentId: "http://example.invalid/video.mp4", ContentType: "video/mp4"}}
	if reply := c.request(mediaNS, load); reply["type"] != "MEDIA_STATUS" {
		t.Fatal(reply)
	}
	r.Advance(3 * time.Second)
	c.request(mediaNS, &cast.MediaHeader{PayloadHeader: cast.PayloadHeader{Type: "PAUSE"}, MediaSessionId: 1})
	r.Advance(time.Second)
	if s := r.Snapshot(); s.Position != 3 || s.PlayerState != "PAUSED" {
		t.Fatalf("pause: %+v", s)
	}
	c.request(mediaNS, &cast.MediaHeader{PayloadHeader: cast.PayloadHeader{Type: "PLAY"}, MediaSessionId: 1})
	r.Advance(7 * time.Second)
	if s := r.Snapshot(); s.PlayerState != "IDLE" || s.Position != 10 {
		t.Fatalf("completion: %+v", s)
	}
	reply = c.request(mediaNS, &cast.PayloadHeader{Type: "GET_STATUS"})
	status := reply["status"].([]any)[0].(map[string]any)
	if status["idleReason"] != "FINISHED" {
		t.Fatal(status)
	}
	r.FailNextPlay()
	if reply := c.request(mediaNS, load); reply["type"] != "LOAD_FAILED" {
		t.Fatal(reply)
	}
	if s := r.Snapshot(); s.PlayCount != 1 {
		t.Fatalf("failed load replaced media: %+v", s)
	}
}

func TestRepeatAndReconnect(t *testing.T) {
	for _, mode := range []string{cast.RepeatModeSingle, cast.RepeatModeAll} {
		t.Run(mode, func(t *testing.T) {
			r, err := simulator.New(simulator.Config{Duration: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			c := connect(t, r)
			c.request(receiverNS, &cast.LaunchRequest{PayloadHeader: cast.PayloadHeader{Type: "LAUNCH"}, AppId: "CC1AD845"})
			c.request(mediaNS, &cast.QueueLoad{PayloadHeader: cast.PayloadHeader{Type: "QUEUE_LOAD"}, RepeatMode: mode, Items: []cast.QueueLoadItem{{Autoplay: true, Media: cast.MediaItem{ContentId: "https://example.invalid/repeat.mp4"}}}})
			r.Advance(23 * time.Second)
			if s := r.Snapshot(); s.Loops != 2 || s.Position != 3 || s.PlayerState != "PLAYING" || s.RepeatMode != mode {
				t.Fatalf("repeat: %+v", s)
			}
			r.Disconnect()
			observer := connect(t, r)
			reply := observer.request(mediaNS, &cast.PayloadHeader{Type: "GET_STATUS"})
			status := reply["status"].([]any)[0].(map[string]any)
			if status["playerState"] != "PLAYING" {
				t.Fatal(status)
			}
			observer.request(mediaNS, &cast.MediaHeader{PayloadHeader: cast.PayloadHeader{Type: "STOP"}, MediaSessionId: 1})
			if s := r.Snapshot(); s.PlayerState != "IDLE" {
				t.Fatalf("stop: %+v", s)
			}
		})
	}
}

func TestCloseAndDiscoveryValidation(t *testing.T) {
	r, err := simulator.New(simulator.Config{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", r.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Closing must also terminate an incomplete TLS handshake.
	done := make(chan struct{})
	go func() { r.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close hung")
	}
	r.Close()
	if _, err := simulator.New(simulator.Config{AdvertiseIP: "192.0.2.1"}); err == nil {
		t.Fatal("advertised unreachable loopback listener")
	}
}
