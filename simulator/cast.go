package simulator

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/grandcat/zeroconf"
	pb "github.com/vishen/go-chromecast/cast/proto"
)

const (
	receiverNS   = "urn:x-cast:com.google.cast.receiver"
	mediaNS      = "urn:x-cast:com.google.cast.media"
	heartbeatNS  = "urn:x-cast:com.google.cast.tp.heartbeat"
	connectionNS = "urn:x-cast:com.google.cast.tp.connection"
	transportID  = "simulator-transport"
)

type peer struct {
	conn   net.Conn
	mu     sync.Mutex
	sender string // guarded by Receiver.mu
}

func (r *Receiver) listen() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return err
	}
	r.listener, err = tls.Listen("tcp", r.config.Listen, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	r.peers = make(map[*peer]struct{})
	r.volume = map[string]any{"level": 1.0, "muted": false}
	return err
}

func (r *Receiver) advertise(ip net.IP) error {
	iface, err := interfaceForIP(ip)
	if err != nil {
		return err
	}
	server, err := zeroconf.RegisterProxy(r.config.Name, "_googlecast._tcp", "local.", r.Port(), "cast-sim-"+r.id[:12]+".local.", []string{ip.String()}, r.TXTRecords(), []net.Interface{*iface})
	if err == nil {
		r.shutdownDiscovery = server.Shutdown
	}
	return err
}

// TXTRecords describes exactly the subset exposed by this simulator.
func (r *Receiver) TXTRecords() []string {
	return []string{"id=" + r.id, "fn=" + r.config.Name, "md=Cast Simulator", "ca=5", "st=0", "ve=05"}
}

func (r *Receiver) serve(conn net.Conn) {
	p := &peer{conn: conn}
	r.mu.Lock()
	r.peers[p] = struct{}{}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.peers, p); r.mu.Unlock() }()
	for {
		var size uint32
		if err := binary.Read(conn, binary.BigEndian, &size); err != nil {
			return
		}
		if size == 0 || size > 1<<20 {
			return
		}
		raw := make([]byte, size)
		if _, err := io.ReadFull(conn, raw); err != nil {
			return
		}
		var msg pb.CastMessage
		if proto.Unmarshal(raw, &msg) != nil || msg.GetPayloadType() != pb.CastMessage_STRING {
			return
		}
		var req struct {
			Type        string         `json:"type"`
			RequestID   int            `json:"requestId"`
			AppID       string         `json:"appId"`
			Media       map[string]any `json:"media"`
			CurrentTime float64        `json:"currentTime"`
			Autoplay    bool           `json:"autoplay"`
			RepeatMode  string         `json:"repeatMode"`
			StartIndex  int            `json:"startIndex"`
			Items       []struct {
				Media    map[string]any `json:"media"`
				Autoplay bool           `json:"autoplay"`
			} `json:"items"`
			Volume map[string]any `json:"volume"`
		}
		if json.Unmarshal([]byte(msg.GetPayloadUtf8()), &req) != nil {
			return
		}
		r.mu.Lock()
		p.sender = msg.GetSourceId()
		var response map[string]any
		switch msg.GetNamespace() {
		case connectionNS: // CONNECT/CLOSE only attach/detach a sender, not media.
		case heartbeatNS:
			if req.Type == "PING" {
				response = map[string]any{"type": "PONG"}
			}
		case receiverNS:
			switch req.Type {
			case "LAUNCH":
				r.appID = req.AppID
			case "STOP":
				r.appID = ""
				r.state.PlayerState = "IDLE"
			case "SET_VOLUME":
				for k, v := range req.Volume {
					r.volume[k] = v
				}
			case "GET_STATUS":
			default:
				response = map[string]any{"type": "INVALID_REQUEST", "reason": "unsupported receiver command"}
			}
			if response == nil {
				response = r.receiverStatus()
			}
		case mediaNS:
			switch req.Type {
			case "LOAD", "QUEUE_LOAD":
				media, autoplay := req.Media, req.Autoplay
				repeat := ""
				if req.Type == "QUEUE_LOAD" {
					if len(req.Items) != 1 || req.StartIndex != 0 || (req.RepeatMode != "REPEAT_SINGLE" && req.RepeatMode != "REPEAT_ALL" && req.RepeatMode != "REPEAT_OFF" && req.RepeatMode != "") {
						response = map[string]any{"type": "INVALID_REQUEST", "reason": "only single-item queues are supported"}
						break
					}
					media, autoplay, repeat = req.Items[0].Media, req.Items[0].Autoplay, req.RepeatMode
				}
				url, _ := media["contentId"].(string)
				if r.failNext || url == "" {
					r.failNext = false
					response = map[string]any{"type": "LOAD_FAILED"}
					break
				}
				r.media = media
				r.sessionID++
				r.state.URL = url
				r.state.Position = req.CurrentTime
				r.state.RepeatMode = repeat
				r.state.PlayCount++
				r.state.Loops = 0
				r.state.PlayerState = "PAUSED"
				if autoplay {
					r.state.PlayerState = "PLAYING"
				}
			case "PLAY":
				if r.state.URL != "" {
					r.state.PlayerState = "PLAYING"
				}
			case "PAUSE":
				if r.state.URL != "" {
					r.state.PlayerState = "PAUSED"
				}
			case "SEEK":
				r.state.Position = req.CurrentTime
			case "STOP":
				r.state.PlayerState = "IDLE"
			case "GET_STATUS":
			default:
				response = map[string]any{"type": "INVALID_REQUEST", "reason": "unsupported media command"}
			}
			if response == nil {
				response = r.mediaStatus()
			}
		default:
			response = map[string]any{"type": "INVALID_REQUEST", "reason": "unsupported namespace"}
		}
		if response != nil {
			response["requestId"] = req.RequestID
		}
		r.mu.Unlock()
		if response != nil && p.send(msg.GetDestinationId(), msg.GetSourceId(), msg.GetNamespace(), response) != nil {
			return
		}
	}
}

// Caller holds r.mu. Returned maps do not share mutable receiver state.
func (r *Receiver) receiverStatus() map[string]any {
	apps := []any{}
	if r.appID != "" {
		apps = append(apps, map[string]any{"appId": r.appID, "displayName": "Simulator Media Receiver", "sessionId": "simulator-session", "transportId": transportID, "isIdleScreen": false})
	}
	volume := map[string]any{}
	for k, v := range r.volume {
		volume[k] = v
	}
	return map[string]any{"type": "RECEIVER_STATUS", "status": map[string]any{"applications": apps, "volume": volume}}
}
func (r *Receiver) mediaStatus() map[string]any {
	status := []any{}
	if r.state.URL != "" {
		media := map[string]any{}
		for k, v := range r.media {
			media[k] = v
		}
		media["duration"] = r.state.Duration
		item := map[string]any{"mediaSessionId": r.sessionID, "playerState": r.state.PlayerState, "currentTime": r.state.Position, "media": media, "repeatMode": r.state.RepeatMode, "currentItemId": 1, "volume": map[string]any{"level": 1.0, "muted": false}}
		if r.state.PlayerState == "IDLE" {
			item["idleReason"] = "CANCELLED"
			if r.state.Position >= r.state.Duration {
				item["idleReason"] = "FINISHED"
			}
		}
		status = append(status, item)
	}
	return map[string]any{"type": "MEDIA_STATUS", "status": status}
}

func (r *Receiver) notify() {
	r.mu.Lock()
	status := r.mediaStatus()
	targets := make(map[*peer]string, len(r.peers))
	for p := range r.peers {
		if p.sender != "" {
			targets[p] = p.sender
		}
	}
	r.mu.Unlock()
	for p, sender := range targets {
		_ = p.send(transportID, sender, mediaNS, status)
	}
}

func (p *peer) send(source, destination, namespace string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	msg := &pb.CastMessage{ProtocolVersion: pb.CastMessage_CASTV2_1_0.Enum(), SourceId: proto.String(source), DestinationId: proto.String(destination), Namespace: proto.String(namespace), PayloadType: pb.CastMessage_STRING.Enum(), PayloadUtf8: proto.String(string(raw))}
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame, uint32(len(data)))
	copy(frame[4:], data)
	n, err := p.conn.Write(frame)
	if err == nil && n != len(frame) {
		return errors.New("short Cast frame write")
	}
	return err
}
