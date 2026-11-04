package rpc

import (
	"bytes"
	"context"

	"github.com/go-faster/errors"

	"github.com/ernado/flipper-notify/internal/pb/application"
	"github.com/ernado/flipper-notify/internal/pb/flipper"
	"github.com/ernado/flipper-notify/internal/pb/gui"
	"github.com/ernado/flipper-notify/internal/pb/property"
	"github.com/ernado/flipper-notify/internal/pb/storage"
	"github.com/ernado/flipper-notify/internal/pb/system"
)

// Send writes a message without waiting for a response (command_id 0).
// Used for requests the device never answers, e.g. virtual display frames.
func (c *Client) Send(m *flipper.Main) error {
	m.CommandId = 0
	return c.write(m)
}

// Ping checks the session is alive.
func (c *Client) Ping(ctx context.Context) error {
	data := []byte("fpager")
	resp, err := c.Call(ctx, &flipper.Main{Content: &flipper.Main_SystemPingRequest{
		SystemPingRequest: &system.PingRequest{Data: data},
	}})
	if err != nil {
		return errors.Wrap(err, "ping")
	}
	if got := resp.GetSystemPingResponse().GetData(); !bytes.Equal(got, data) {
		return errors.Errorf("ping: unexpected payload %q", got)
	}
	return nil
}

// Alert plays the built-in audiovisual alert (vibro, sound, LED).
func (c *Client) Alert(ctx context.Context) error {
	_, err := c.Call(ctx, &flipper.Main{Content: &flipper.Main_SystemPlayAudiovisualAlertRequest{
		SystemPlayAudiovisualAlertRequest: &system.PlayAudiovisualAlertRequest{},
	}})
	if err != nil {
		return errors.Wrap(err, "alert")
	}
	return nil
}

// StartVirtualDisplay shows frame (1024-byte XBM) fullscreen; with input=true
// button presses are delivered as gui_send_input_event_request events.
func (c *Client) StartVirtualDisplay(ctx context.Context, frame []byte, input bool) error {
	_, err := c.Call(ctx, &flipper.Main{Content: &flipper.Main_GuiStartVirtualDisplayRequest{
		GuiStartVirtualDisplayRequest: &gui.StartVirtualDisplayRequest{
			FirstFrame: &gui.ScreenFrame{Data: frame},
			SendInput:  input,
		},
	}})
	if err != nil {
		return errors.Wrap(err, "start virtual display")
	}
	return nil
}

// Frame updates the virtual display. The device does not acknowledge frames.
func (c *Client) Frame(frame []byte) error {
	if err := c.Send(&flipper.Main{Content: &flipper.Main_GuiScreenFrame{
		GuiScreenFrame: &gui.ScreenFrame{Data: frame},
	}}); err != nil {
		return errors.Wrap(err, "frame")
	}
	return nil
}

// StopVirtualDisplay hides the virtual display.
func (c *Client) StopVirtualDisplay(ctx context.Context) error {
	_, err := c.Call(ctx, &flipper.Main{Content: &flipper.Main_GuiStopVirtualDisplayRequest{
		GuiStopVirtualDisplayRequest: &gui.StopVirtualDisplayRequest{},
	}})
	if err != nil {
		return errors.Wrap(err, "stop virtual display")
	}
	return nil
}

// StopSession asks the device to end the RPC session (USB returns to CLI).
func (c *Client) StopSession(ctx context.Context) error {
	_, err := c.Call(ctx, &flipper.Main{Content: &flipper.Main_StopSession{StopSession: &flipper.StopSession{}}})
	if err != nil {
		return errors.Wrap(err, "stop session")
	}
	return nil
}

// StartScreenStream requests framebuffer frames, delivered as gui_screen_frame events.
func (c *Client) StartScreenStream(ctx context.Context) error {
	_, err := c.Call(ctx, &flipper.Main{Content: &flipper.Main_GuiStartScreenStreamRequest{
		GuiStartScreenStreamRequest: &gui.StartScreenStreamRequest{},
	}})
	if err != nil {
		return errors.Wrap(err, "start screen stream")
	}
	return nil
}

// StopScreenStream stops framebuffer frames.
func (c *Client) StopScreenStream(ctx context.Context) error {
	_, err := c.Call(ctx, &flipper.Main{Content: &flipper.Main_GuiStopScreenStreamRequest{
		GuiStopScreenStreamRequest: &gui.StopScreenStreamRequest{},
	}})
	if err != nil {
		return errors.Wrap(err, "stop screen stream")
	}
	return nil
}

// StartApp launches an application by name or SD card path (.fap) with args.
func (c *Client) StartApp(ctx context.Context, name, args string) error {
	_, err := c.Call(ctx, &flipper.Main{Content: &flipper.Main_AppStartRequest{
		AppStartRequest: &application.StartRequest{Name: name, Args: args},
	}})
	if err != nil {
		return errors.Wrapf(err, "start app %s", name)
	}
	return nil
}

// MD5 returns the md5 of a file on the device, or a *StatusError (e.g. ERROR_STORAGE_NOT_EXIST).
func (c *Client) MD5(ctx context.Context, path string) (string, error) {
	resp, err := c.Call(ctx, &flipper.Main{Content: &flipper.Main_StorageMd5SumRequest{
		StorageMd5SumRequest: &storage.Md5SumRequest{Path: path},
	}})
	if err != nil {
		return "", errors.Wrapf(err, "md5 %s", path)
	}
	return resp.GetStorageMd5SumResponse().GetMd5Sum(), nil
}

// Mkdir creates a directory; an existing one is not an error.
func (c *Client) Mkdir(ctx context.Context, path string) error {
	_, err := c.Call(ctx, &flipper.Main{Content: &flipper.Main_StorageMkdirRequest{
		StorageMkdirRequest: &storage.MkdirRequest{Path: path},
	}})
	var se *StatusError
	if errors.As(err, &se) && se.Status == flipper.CommandStatus_ERROR_STORAGE_EXIST {
		return nil
	}
	if err != nil {
		return errors.Wrapf(err, "mkdir %s", path)
	}
	return nil
}

// WriteFile writes data to path, split into multi-part requests.
func (c *Client) WriteFile(ctx context.Context, path string, data []byte) error {
	const chunk = 512
	var parts []*flipper.Main
	for off := 0; off < len(data) || off == 0; off += chunk {
		end := min(off+chunk, len(data))
		parts = append(parts, &flipper.Main{Content: &flipper.Main_StorageWriteRequest{
			StorageWriteRequest: &storage.WriteRequest{Path: path, File: &storage.File{Data: data[off:end]}},
		}})
		if end == len(data) {
			break
		}
	}
	if _, err := c.CallParts(ctx, parts...); err != nil {
		return errors.Wrapf(err, "write %s", path)
	}
	return nil
}

// Properties returns key/value pairs for a property prefix, e.g. "pwrinfo".
func (c *Client) Properties(ctx context.Context, key string) (map[string]string, error) {
	parts, err := c.CallParts(ctx, &flipper.Main{Content: &flipper.Main_PropertyGetRequest{
		PropertyGetRequest: &property.GetRequest{Key: key},
	}})
	if err != nil {
		return nil, errors.Wrapf(err, "property %s", key)
	}
	out := make(map[string]string, len(parts))
	for _, p := range parts {
		if r := p.GetPropertyGetResponse(); r != nil && r.GetKey() != "" {
			out[r.GetKey()] = r.GetValue()
		}
	}
	return out, nil
}
