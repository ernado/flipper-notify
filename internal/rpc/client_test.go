package rpc

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/go-faster/errors"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/ernado/flipper-notify/internal/pb/flipper"
	"github.com/ernado/flipper-notify/internal/pb/gui"
	"github.com/ernado/flipper-notify/internal/pb/system"
)

func writeFrame(t *testing.T, w io.Writer, m *flipper.Main) {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(append(protowire.AppendVarint(nil, uint64(len(b))), b...)); err != nil {
		t.Error(err)
	}
}

// fakeDevice answers pings and emits an input event before each answer.
func fakeDevice(t *testing.T, conn net.Conn) {
	r := bufio.NewReader(conn)
	for {
		n, err := binary.ReadUvarint(r)
		if err != nil {
			return
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		var req flipper.Main
		if err := proto.Unmarshal(buf, &req); err != nil {
			t.Error(err)
			return
		}
		writeFrame(t, conn, &flipper.Main{Content: &flipper.Main_GuiSendInputEventRequest{
			GuiSendInputEventRequest: &gui.SendInputEventRequest{Key: gui.InputKey_OK, Type: gui.InputType_SHORT},
		}})
		if p := req.GetSystemPingRequest(); p != nil {
			writeFrame(t, conn, &flipper.Main{CommandId: req.GetCommandId(), Content: &flipper.Main_SystemPingResponse{
				SystemPingResponse: &system.PingResponse{Data: p.GetData()},
			}})
		} else if req.GetCommandId() != 0 {
			writeFrame(t, conn, &flipper.Main{CommandId: req.GetCommandId(), CommandStatus: flipper.CommandStatus_ERROR_NOT_IMPLEMENTED})
		}
	}
}

func TestClient(t *testing.T) {
	host, dev := net.Pipe()
	go fakeDevice(t, dev)
	events := make(chan *flipper.Main, 8)
	c := New(host, func(_ *Client, m *flipper.Main) { events <- m })
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range 3 {
		if err := c.Ping(ctx); err != nil {
			t.Fatal(err)
		}
	}
	err := c.Alert(ctx)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != flipper.CommandStatus_ERROR_NOT_IMPLEMENTED {
		t.Fatalf("want status error, got %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("events = %d", len(events))
	}
	if e := <-events; e.GetGuiSendInputEventRequest().GetKey() != gui.InputKey_OK {
		t.Fatal("bad event")
	}

	_ = dev.Close()
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("client did not notice closed stream")
	}
	if err := c.Ping(ctx); err == nil {
		t.Fatal("ping on dead session succeeded")
	}
}
