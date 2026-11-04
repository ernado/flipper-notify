// Package rpc implements the Flipper Zero protobuf RPC client over a byte stream.
//
// Frames are varint-length-delimited PB.Main messages in both directions. Responses
// carry the request command_id; unsolicited messages (e.g. virtual display input)
// use command_id 0.
package rpc

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"sync"

	"github.com/go-faster/errors"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/ernado/flipper-notify/internal/pb/flipper"
)

const maxFrame = 64 << 10

// StatusError is a non-OK command status returned by the device.
type StatusError struct {
	Status flipper.CommandStatus
}

func (e *StatusError) Error() string { return "flipper: " + e.Status.String() }

// Client is an RPC session. Safe for concurrent use.
type Client struct {
	rw      io.ReadWriteCloser
	onEvent func(*Client, *flipper.Main)

	wmu sync.Mutex

	mu      sync.Mutex
	nextID  uint32
	pending map[uint32]chan *flipper.Main
	err     error
	done    chan struct{}
	close   sync.Once
}

// New starts a session over rw. onEvent receives unsolicited messages; it is called
// from the read loop and must not block or call the client synchronously.
func New(rw io.ReadWriteCloser, onEvent func(*Client, *flipper.Main)) *Client {
	c := &Client{
		rw:      rw,
		onEvent: onEvent,
		pending: make(map[uint32]chan *flipper.Main),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Done is closed when the session is dead.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err returns the reason the session ended.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close terminates the session.
func (c *Client) Close() error {
	c.fail(errors.New("closed"))
	return nil
}

func (c *Client) fail(err error) {
	c.close.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		_ = c.rw.Close()
		close(c.done)
	})
}

func (c *Client) readLoop() {
	r := bufio.NewReader(c.rw)
	for {
		n, err := binary.ReadUvarint(r)
		if err != nil {
			c.fail(errors.Wrap(err, "read length"))
			return
		}
		if n > maxFrame {
			c.fail(errors.Errorf("frame too large: %d", n))
			return
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			c.fail(errors.Wrap(err, "read frame"))
			return
		}
		m := new(flipper.Main)
		if err := proto.Unmarshal(buf, m); err != nil {
			c.fail(errors.Wrap(err, "decode frame"))
			return
		}
		if id := m.GetCommandId(); id != 0 {
			c.mu.Lock()
			ch := c.pending[id]
			if ch != nil && !m.GetHasNext() {
				delete(c.pending, id)
			}
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- m:
				default:
					c.fail(errors.Errorf("response %d: too many parts", id))
					return
				}
				continue
			}
		}
		if c.onEvent != nil {
			c.onEvent(c, m)
		}
	}
}

func (c *Client) write(m *flipper.Main) error {
	body, err := proto.Marshal(m)
	if err != nil {
		return errors.Wrap(err, "marshal")
	}
	frame := protowire.AppendVarint(make([]byte, 0, len(body)+3), uint64(len(body)))
	frame = append(frame, body...)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.rw.Write(frame); err != nil {
		c.fail(errors.Wrap(err, "write"))
		return errors.Wrap(err, "write")
	}
	return nil
}

// Call sends a request and waits for the final response.
func (c *Client) Call(ctx context.Context, m *flipper.Main) (*flipper.Main, error) {
	parts, err := c.CallParts(ctx, m)
	if len(parts) == 0 {
		return nil, err
	}
	return parts[len(parts)-1], err
}

// maxParts bounds a multi-part response (e.g. property lists, directory listings).
const maxParts = 256

// CallParts sends a request made of one or more parts (has_next set on all but the
// last, one command_id) and returns every response part.
func (c *Client) CallParts(ctx context.Context, req ...*flipper.Main) ([]*flipper.Main, error) {
	ch := make(chan *flipper.Main, maxParts)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, errors.Wrap(err, "session dead")
	}
	c.nextID++
	if c.nextID == 0 {
		c.nextID = 1
	}
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()

	for i, m := range req {
		m.CommandId = id
		m.HasNext = i < len(req)-1
		if err := c.write(m); err != nil {
			c.forget(id)
			return nil, err
		}
	}
	var parts []*flipper.Main
	for {
		select {
		case resp := <-ch:
			parts = append(parts, resp)
			if s := resp.GetCommandStatus(); s != flipper.CommandStatus_OK {
				c.forget(id)
				return parts, &StatusError{Status: s}
			}
			if !resp.GetHasNext() {
				return parts, nil
			}
		case <-ctx.Done():
			c.forget(id)
			return parts, ctx.Err()
		case <-c.done:
			return parts, errors.Wrap(c.Err(), "session dead")
		}
	}
}

func (c *Client) forget(id uint32) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}
