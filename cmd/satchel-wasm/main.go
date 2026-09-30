//go:build js && wasm

// Command satchel-wasm is the browser core. It owns the parley session and
// the transfer service, and exposes exactly two functions to JavaScript:
//
//	window.satchel_send(json, files?)   UI → core
//	window.satchelOnEvent(json, bytes?) core → UI
//
// Commands and events are JSON. The optional second argument carries what
// JSON should not: the File objects a person picked, and the bytes of an
// item that arrived (a Uint8Array). The JS layer renders and triggers
// downloads; it never implements protocol.
//
// This is the only package in the module allowed to import syscall/js.
package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"syscall/js"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/richardwooding/parley/wire"

	"github.com/richardwooding/satchel/internal/manifest"
	"github.com/richardwooding/satchel/internal/share"
	"github.com/richardwooding/satchel/internal/sink"
	"github.com/richardwooding/satchel/internal/xfer"
)

// browserMax bounds what the page will receive: everything is held in memory
// until it is handed to JS as a download.
const browserMax = 512 << 20

var browserLimits = manifest.Limits{
	MaxItems: 5000, MaxBytes: browserMax, MaxPath: 1024, MaxDepth: 32, MaxMimeLen: 127,
}

// command is the single UI→core message shape; unused fields stay empty.
type command struct {
	Type   string `json:"type"`
	Phrase string `json:"phrase,omitempty"`
	Text   string `json:"text,omitempty"`
	From   uint32 `json:"from,omitempty"`
	ID     string `json:"id,omitempty"`
}

// app holds the one live session, guarded by a generation counter so a
// superseded session's goroutines retire quietly.
type app struct {
	mu    sync.Mutex
	gen   int
	s     *share.Session
	sinks map[string]*sink.Memory // accepted offer ID → its sink
}

var current = app{sinks: map[string]*sink.Memory{}}

func main() {
	js.Global().Set("satchel_send", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		files := js.Undefined()
		if len(args) > 1 {
			files = args[1]
		}
		go dispatch(args[0].String(), files)
		return nil
	}))
	emit("core.ready", map[string]any{})
	select {}
}

func emit(typ string, fields map[string]any) { emitBytes(typ, fields, nil) }

func emitBytes(typ string, fields map[string]any, data []byte) {
	fields["type"] = typ
	b, err := json.Marshal(fields)
	if err != nil {
		return
	}
	if data == nil {
		js.Global().Call("satchelOnEvent", string(b))
		return
	}
	arr := js.Global().Get("Uint8Array").New(len(data))
	js.CopyBytesToJS(arr, data)
	js.Global().Call("satchelOnEvent", string(b), arr)
}

func emitError(msg string) { emit("error", map[string]any{"message": msg}) }

var commands = map[string]func(command, js.Value){
	"create":      func(command, js.Value) { create() },
	"join":        func(c command, _ js.Value) { join(c.Phrase) },
	"leave":       func(command, js.Value) { closeCurrent() },
	"offer.text":  func(c command, _ js.Value) { offerText(c.Text) },
	"offer.files": func(_ command, files js.Value) { offerFiles(files) },
	"accept":      func(c command, _ js.Value) { accept(c.From, c.ID) },
	"decline":     func(c command, _ js.Value) { decline(c.From, c.ID) },
	"withdraw":    func(c command, _ js.Value) { withdraw(c.ID) },
}

func dispatch(raw string, files js.Value) {
	var cmd command
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		emitError("bad command: " + err.Error())
		return
	}
	h, ok := commands[cmd.Type]
	if !ok {
		emitError("unknown command " + cmd.Type)
		return
	}
	h(cmd, files)
}

// relayURL derives the WebSocket endpoint from the page's own origin.
func relayURL() string {
	loc := js.Global().Get("location")
	scheme := "ws"
	if loc.Get("protocol").String() == "https:" {
		scheme = "wss"
	}
	return scheme + "://" + loc.Get("host").String() + "/ws"
}

func shareURL(phrase string) string {
	loc := js.Global().Get("location")
	return loc.Get("protocol").String() + "//" + loc.Get("host").String() + "/#" + phrase
}

func create() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, err := share.Host(ctx, relayURL())
	if err != nil {
		emitError("couldn't start: " + err.Error())
		return
	}
	adopt(s)
	url := shareURL(s.Phrase())
	qr := ""
	if png, err := qrcode.Encode(url, qrcode.Medium, 220); err == nil {
		qr = base64.StdEncoding.EncodeToString(png)
	}
	emit("session.created", map[string]any{"phrase": s.Phrase(), "url": url, "qr": qr})
}

func join(phrase string) {
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		emitError("enter a code phrase")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, err := share.Join(ctx, relayURL(), phrase)
	if err != nil {
		emitError(err.Error())
		return
	}
	adopt(s)
	emit("session.joined", map[string]any{"phrase": s.Phrase()})
}

// adopt makes s the live session, retiring any previous one.
func adopt(s *share.Session) {
	closeCurrent()
	current.mu.Lock()
	current.gen++
	gen := current.gen
	current.s = s
	current.mu.Unlock()
	go s.Run(func(ev any) {
		if isCurrent(gen) {
			onEvent(ev)
		}
	})
}

func closeCurrent() {
	current.mu.Lock()
	current.gen++
	s := current.s
	current.s = nil
	current.sinks = map[string]*sink.Memory{}
	current.mu.Unlock()
	if s != nil {
		s.Close()
	}
}

func isCurrent(gen int) bool {
	current.mu.Lock()
	defer current.mu.Unlock()
	return gen == current.gen
}

func live() *share.Session {
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.s
}

func onEvent(ev any) {
	switch e := ev.(type) {
	case share.Peers:
		emit("peers", map[string]any{"count": e.Count})
	case share.Reconnecting:
		emit("session.reconnecting", map[string]any{})
	case share.Resumed:
		emit("session.resumed", map[string]any{})
	case share.Closed:
		emit("session.closed", map[string]any{"reason": e.Reason})
	case xfer.Offered:
		emit("offer", map[string]any{
			"from": uint32(e.From), "id": e.ID.String(), "name": e.Summary.Name,
			"items": e.Summary.Items, "bytes": e.Summary.Bytes, "kind": e.Summary.Kind.String(),
			"tooBig": e.Summary.Bytes > browserMax,
		})
	case xfer.Progress:
		emit("progress", map[string]any{"id": e.ID.String(), "done": e.Done, "total": e.Total})
	case xfer.Received:
		deliver(e)
	case xfer.Delivered:
		emit("delivered", map[string]any{"id": e.ID.String()})
	case xfer.Failed:
		emit("failed", map[string]any{"id": e.ID.String(), "message": e.Err.Error()})
	case xfer.Withdrawn:
		emit("withdrawn", map[string]any{"id": e.ID.String()})
	}
}

// deliver hands every received item to JS, then announces completion.
func deliver(e xfer.Received) {
	id := e.ID.String()
	current.mu.Lock()
	mem := current.sinks[id]
	delete(current.sinks, id)
	current.mu.Unlock()
	if mem == nil {
		return
	}
	for i, it := range e.Manifest.Items {
		data, ok := mem.Item(i)
		if !ok || it.Kind == manifest.KindDir {
			continue
		}
		emitBytes("item", map[string]any{
			"id": id, "path": it.Path, "kind": it.Kind.String(), "mime": it.Mime, "size": it.Size,
		}, data)
	}
	emit("received", map[string]any{"id": id, "items": len(e.Manifest.Items)})
}

func accept(from uint32, idHex string) {
	s := live()
	id, ok := parseID(idHex)
	if s == nil || !ok {
		emitError("that offer is gone")
		return
	}
	mem := sink.NewMemory(browserMax)
	current.mu.Lock()
	current.sinks[idHex] = mem
	current.mu.Unlock()
	if err := s.X.Accept(wire.ParticipantID(from), id, mem, browserLimits); err != nil {
		emitError(err.Error())
	}
}

func decline(from uint32, idHex string) {
	if s, id, ok := liveID(idHex); ok {
		s.X.Decline(wire.ParticipantID(from), id)
	}
}

func withdraw(idHex string) {
	if s, id, ok := liveID(idHex); ok {
		_ = s.X.Withdraw(id)
	}
}

func liveID(idHex string) (*share.Session, xfer.OfferID, bool) {
	s := live()
	id, ok := parseID(idHex)
	return s, id, s != nil && ok
}

func parseID(h string) (xfer.OfferID, bool) {
	var id xfer.OfferID
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != len(id) {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

func offerText(text string) {
	s := live()
	if s == nil {
		emitError("start or join a session first")
		return
	}
	data := []byte(text)
	m := manifest.Manifest{Items: []manifest.Item{xfer.ItemFor("text.txt", manifest.KindText, "text/plain;charset=utf-8", data)}}
	id, err := s.X.Offer(m, xfer.Bytes{data})
	if err != nil {
		emitError(err.Error())
		return
	}
	emit("offered", map[string]any{"id": id.String(), "name": "text", "items": 1, "bytes": len(data)})
}
