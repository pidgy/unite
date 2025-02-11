package input

import (
	"fmt"
	"io"

	"github.com/gen2brain/malgo"
	"github.com/pkg/errors"

	"github.com/pidgy/unitehud/avi/audio/device"
	"github.com/pidgy/unitehud/core/notify"
)

type Device struct {
	ID      string
	Formats []malgo.DataFormat

	name      string
	isDefault bool

	reconnects int

	active            bool
	closingq, closedq chan bool

	config malgo.DeviceConfig
}

var (
	disabled = &Device{name: device.Disabled}
)

func (d *Device) Is(name string) bool { return device.Is(d, name) }
func (d *Device) IsDefault() bool     { return d.isDefault }
func (d *Device) IsDisabled() bool    { return d == nil || d.name == device.Disabled }
func (d *Device) Name() string        { return d.name }

func New(ctx *malgo.AllocatedContext, name string) (*Device, error) {
	if name == device.Disabled || name == "" {
		return disabled, nil
	}

	for _, d := range Devices(ctx) {
		if !device.Is(d, name) {
			continue
		}

		d.config = malgo.DefaultDeviceConfig(malgo.Capture)
		d.config.Capture.Format = malgo.FormatS16
		d.config.Capture.Channels = 1
		d.config.Playback.Format = malgo.FormatS16
		d.config.Playback.Channels = 1
		d.config.SampleRate = 44100
		d.config.Alsa.NoMMap = 1

		return d, nil
	}

	return disabled, fmt.Errorf("failed to find capture device: %s", name)
}

func (d *Device) Active() bool {
	return d.active
}

func (d *Device) Close() {
	if !d.Active() {
		return
	}
	notify.System("[Audio Input] Closing %s", d.name)

	close(d.closingq)
	<-d.closedq
}

func (d *Device) Start(mctx malgo.Context, w io.ReadWriter) error {
	if d.IsDisabled() {
		return nil
	}

	if d.Active() {
		return errors.Wrap(fmt.Errorf("already active"), d.String())
	}

	defer notify.Debug("[Audio Input] Started %s", d)

	errq := make(chan error)
	go func() {
		defer notify.Debug("[Audio Input] Closed %s", d)

		d.closingq = make(chan bool)
		d.closedq = make(chan bool)

		d.active = true
		defer func() {
			d.active = false
		}()

		defer close(d.closedq)

		callbacks := malgo.DeviceCallbacks{
			Data: func(outputSamples, inputSamples []byte, frameCount uint32) {
				if !d.Active() {
					return
				}

				_, err := w.Write(inputSamples)
				if err != nil {
					if err == io.EOF || err == io.ErrUnexpectedEOF {
						d.reconnects++
						return
					}
					notify.Error("[Audio Input] Capture error (%v)", errors.Wrap(err, d.String()))
				}
			},
		}

		device, err := malgo.InitDevice(mctx, d.config, callbacks)
		if err != nil {
			errq <- errors.Wrap(err, d.name)
			return
		}
		defer device.Uninit()

		err = device.Start()
		if err != nil {
			errq <- errors.Wrap(err, d.name)
			return
		}
		defer func() {
			err := device.Stop()
			if err != nil {
				notify.Error("[Audio Input] Failed to stop device (%v)", err)
				return
			}
		}()

		close(errq)
		<-d.closingq
	}()

	return <-errq
}

func (d *Device) String() string {
	return device.String(d)
}

func (d *Device) Type() device.Type {
	return device.Input
}

func Devices(ctx *malgo.AllocatedContext) (captures []*Device) {
	d, err := ctx.Devices(malgo.Capture)
	if err != nil {
		notify.Error("[Audio Input] Failed to find devices (%v)", err)
		return nil
	}

	for _, info := range d {
		full, err := ctx.DeviceInfo(malgo.Capture, info.ID, malgo.Shared)
		if err != nil {
			notify.Warn("[Audio Input] Failed to poll device \"%s\" (%v)", info.ID, err)
		}

		captures = append(captures, &Device{
			ID:      info.ID.String(),
			Formats: full.Formats,

			name:      info.Name(),
			isDefault: info.IsDefault != 0,
		})
	}

	return captures
}
