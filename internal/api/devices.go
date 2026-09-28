package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Devices on the local network (M4, #51). Reading comes from the adapter;
// naming a device and "I know this one" are the panel's own notes, kept in
// the panel's settings. They change nothing on the router's network, so they
// take effect at once, without the apply bar (D-86, design 08 §6).

// observeEvery is how often the network is looked at when nobody asks. The
// bridge forgets a silent device after 300 s, so a look every minute knows
// when everyone was last heard to within the minute.
const observeEvery = time.Minute

type DevicesOutput struct {
	Body core.DeviceList
}

type DeviceMACInput struct {
	MAC string `path:"mac" doc:"Hardware address of the device, e.g. 00:00:5e:00:53:10"`
}

type NameDeviceInput struct {
	MAC  string `path:"mac" doc:"Hardware address of the device"`
	Body struct {
		Name string `json:"name" doc:"Name for the panel; empty removes the name. Not published in DNS"`
	}
}

type MarkKnownInput struct {
	Body struct {
		MACs []string `json:"macs" minItems:"1" maxItems:"1024" doc:"Devices the owner knows; their \"new\" mark goes away"`
	}
}

func (s *Server) registerDevices(authed huma.Middlewares, authSec []map[string][]string) {
	huma.Register(s.api, huma.Operation{
		OperationID: "listDevices", Method: http.MethodGet, Path: "/devices",
		Summary: "Devices on the local network: who is here now, who is new, how each is connected",
		Tags:    []string{"devices"}, Middlewares: authed, Security: authSec,
		Errors:      []int{http.StatusNotFound, http.StatusNotImplemented, http.StatusBadGateway},
		Description: "404 means this device has no local network — a fact about the hardware, not an error.",
	}, s.listDevices)
	huma.Register(s.api, huma.Operation{
		OperationID: "nameDevice", Method: http.MethodPut, Path: "/devices/{mac}/name",
		Summary: "Give a device a name in the panel (takes effect at once; changes nothing on the network)",
		Tags:    []string{"devices"}, DefaultStatus: http.StatusNoContent,
		Middlewares: authed, Security: authSec,
	}, s.nameDevice)
	huma.Register(s.api, huma.Operation{
		OperationID: "markDevicesKnown", Method: http.MethodPost, Path: "/devices/known",
		Summary: "Mark devices as known, taking their \"new\" mark off",
		Tags:    []string{"devices"}, DefaultStatus: http.StatusNoContent,
		Middlewares: authed, Security: authSec,
	}, s.markDevicesKnown)
	huma.Register(s.api, huma.Operation{
		OperationID: "forgetDevice", Method: http.MethodDelete, Path: "/devices/{mac}",
		Summary: "Forget what the panel remembers about a device (its name and \"known\")",
		Tags:    []string{"devices"}, DefaultStatus: http.StatusNoContent,
		Middlewares: authed, Security: authSec,
	}, s.forgetDevice)
}

func (s *Server) listDevices(_ context.Context, _ *struct{}) (*DevicesOutput, error) {
	list, err := s.adapter.Device().ListDevices()
	switch {
	case errors.Is(err, core.ErrNoLAN):
		return nil, huma.Error404NotFound("no lan interface", err)
	case errors.Is(err, core.ErrNotImplemented):
		return nil, huma.Error501NotImplemented("devices", err)
	case err != nil:
		return nil, huma.Error502BadGateway("devices", err)
	}
	doc, err := s.store.Load()
	if err != nil {
		return nil, huma.Error500InternalServerError("reading the panel's notes about devices", err)
	}
	list.Devices = core.MergeDevices(list.Devices, doc.Devices)
	return &DevicesOutput{Body: list}, nil
}

func pathMAC(raw string) (string, error) {
	mac, err := core.NormalizeMAC(raw)
	if err != nil {
		return "", huma.Error400BadRequest(err.Error(), &huma.ErrorDetail{
			Message: err.Error(), Location: "path.mac", Value: raw,
		})
	}
	return mac, nil
}

func (s *Server) nameDevice(_ context.Context, in *NameDeviceInput) (*struct{}, error) {
	mac, err := pathMAC(in.MAC)
	if err != nil {
		return nil, err
	}
	name, err := core.CleanDeviceName(in.Body.Name)
	if err != nil {
		return nil, refusal(core.Refuse("name", err))
	}
	return nil, s.noted(s.store.Update(func(d *config.Document) error { return d.SetDeviceName(mac, name) }))
}

func (s *Server) markDevicesKnown(_ context.Context, in *MarkKnownInput) (*struct{}, error) {
	macs := make([]string, 0, len(in.Body.MACs))
	for _, raw := range in.Body.MACs {
		mac, err := core.NormalizeMAC(raw)
		if err != nil {
			return nil, refusal(core.Refuse("macs", err))
		}
		macs = append(macs, mac)
	}
	return nil, s.noted(s.store.Update(func(d *config.Document) error { return d.MarkDevicesKnown(macs) }))
}

func (s *Server) forgetDevice(_ context.Context, in *DeviceMACInput) (*struct{}, error) {
	mac, err := pathMAC(in.MAC)
	if err != nil {
		return nil, err
	}
	// Forgetting a device the panel knows nothing about is not an error: the
	// outcome the caller asked for is already true.
	return nil, s.noted(s.store.Update(func(d *config.Document) error { d.ForgetDevice(mac); return nil }))
}

func (s *Server) noted(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, config.ErrTooManyDevices):
		return huma.Error409Conflict(err.Error())
	default:
		return huma.Error500InternalServerError("saving the panel's notes about devices", err)
	}
}

// observeDevices looks at the network on a timer, if the adapter can.
func (s *Server) observeDevices(ctx context.Context) {
	o, ok := s.adapter.Device().(core.DeviceObserver)
	if !ok {
		return
	}
	o.ObserveDevices()
	t := time.NewTicker(observeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			o.ObserveDevices()
		}
	}
}
