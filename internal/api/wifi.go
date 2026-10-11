package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Wi-Fi (M4, #57). Two kinds of edit (D-100): a radio edit is staged and goes
// through the apply bar with its confirmation window; a network's name and
// password are applied at once, without a watchdog, and only when nothing
// else waits in the draft — an unconfirmed apply of those would take the
// password back with it.

type WiFiOutput struct {
	Body core.WiFiStatus
}

// WiFiInput carries where the request came from: that is how the answer knows
// whether the person reading it is on the radio they are about to change.
type WiFiInput struct {
	from string
}

func (in *WiFiInput) Resolve(ctx huma.Context) []error {
	if host, _, err := net.SplitHostPort(ctx.RemoteAddr()); err == nil {
		in.from = host
	}
	return nil
}

type WiFiNetworkIDInput struct {
	ID string `path:"id" doc:"The network's id from GET /wifi"`
}

type WiFiPasswordOutput struct {
	Body struct {
		Password string `json:"password"`
	}
}

type StageRadioInput struct {
	ID   string `path:"id" doc:"The radio's id from GET /wifi"`
	Body core.RadioConfig
}

type StageCountryInput struct {
	Body struct {
		Country string `json:"country" doc:"Two-letter country code, e.g. RU" minLength:"2" maxLength:"2"`
	}
}

type SetAccessInput struct {
	ID   string `path:"id" doc:"The network's id from GET /wifi"`
	Body core.AccessConfig
}

type AccessOutput struct {
	Body struct {
		// Changes as they were applied; the password is •••• here as
		// everywhere in a diff.
		Changes []core.ConfigChange `json:"changes"`
	}
}

type StageGuestInput struct {
	Body core.GuestConfig
}

type StageGuestEnabledInput struct {
	Body struct {
		Enabled bool `json:"enabled"`
	}
}

func (s *Server) wifi() (core.WiFiManager, bool) {
	p, ok := s.adapter.(core.WiFiProvider)
	if !ok {
		return nil, false
	}
	return p.WiFi(), true
}

func (s *Server) registerWiFi(authed huma.Middlewares, authSec []map[string][]string) {
	huma.Register(s.api, huma.Operation{
		OperationID: "getWiFi", Method: http.MethodGet, Path: "/wifi",
		Summary: "Wi-Fi: networks, radios, the channels the country allows, and how the caller is connected",
		Description: "404 means the device has no radio — a fact about the hardware. Passwords are never in this answer; " +
			"see GET /wifi/networks/{id}/password.",
		Tags: []string{"wifi"}, Middlewares: authed, Security: authSec,
		Errors: []int{http.StatusNotFound, http.StatusNotImplemented, http.StatusBadGateway},
	}, s.getWiFi)
	huma.Register(s.api, huma.Operation{
		OperationID: "getWiFiPassword", Method: http.MethodGet, Path: "/wifi/networks/{id}/password",
		Summary: "A Wi-Fi network's password, asked for when somebody presses \"Show\" or \"QR code\"",
		Tags:    []string{"wifi"}, Middlewares: authed, Security: authSec,
		Errors: []int{http.StatusNotFound, http.StatusNotImplemented},
	}, s.getWiFiPassword)
	huma.Register(s.api, huma.Operation{
		OperationID: "stageWiFiRadio", Method: http.MethodPut, Path: "/wifi/radios/{id}",
		Summary:     "Stage a radio edit: on or off, channel, width (does not apply it)",
		Description: "Goes through the apply bar and its confirmation window. The channel is \"auto\" or one of the radio's channels.",
		Tags:        []string{"wifi"}, Middlewares: authed, Security: authSec,
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusNotImplemented},
	}, s.stageWiFiRadio)
	huma.Register(s.api, huma.Operation{
		OperationID: "stageWiFiCountry", Method: http.MethodPut, Path: "/wifi/country",
		Summary: "Stage the Wi-Fi country on every radio (does not apply it)",
		Tags:    []string{"wifi"}, Middlewares: authed, Security: authSec,
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusNotImplemented},
	}, s.stageWiFiCountry)
	huma.Register(s.api, huma.Operation{
		OperationID: "setWiFiAccess", Method: http.MethodPut, Path: "/wifi/networks/{id}",
		Summary: "Change a network's name, password or security — applied at once, without a confirmation window",
		Description: "Every device on the network is disconnected and has to join again with the new password. " +
			"There is no automatic revert: devices that saw the new password refused do not come back when the old one " +
			"is restored. Refused with 409 while other changes wait in the draft or an apply awaits confirmation.",
		Tags: []string{"wifi"}, Middlewares: authed, Security: authSec,
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusNotImplemented},
	}, s.setWiFiAccess)

	// The guest network (#58). Each edit changes the network and the
	// firewall, so it goes through the apply bar with its confirmation
	// window; its password is changed like any network's (PUT above).
	guestErrors := []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict,
		http.StatusUnprocessableEntity, http.StatusNotImplemented, http.StatusBadGateway}
	huma.Register(s.api, huma.Operation{
		OperationID: "stageWiFiGuest", Method: http.MethodPost, Path: "/wifi/guest",
		Summary: "Stage a guest network on every radio (or the given ones): internet only, no home devices, no panel",
		Description: "Does not apply it. 409 when there is a guest network already (the panel's own or one made by hand), " +
			"when other changes wait in the draft, or while an apply awaits confirmation. May renumber the home " +
			"network's IPv6 once to leave room for the guests; the draft then says so in a row of its own.",
		Tags: []string{"wifi"}, Middlewares: authed, Security: authSec, Errors: guestErrors,
	}, s.stageWiFiGuest)
	huma.Register(s.api, huma.Operation{
		OperationID: "stageWiFiGuestEnabled", Method: http.MethodPut, Path: "/wifi/guest",
		Summary: "Stage turning the guest network's access points off or on; its name and password stay",
		Tags:    []string{"wifi"}, Middlewares: authed, Security: authSec, Errors: guestErrors,
	}, s.stageWiFiGuestEnabled)
	huma.Register(s.api, huma.Operation{
		OperationID: "removeWiFiGuest", Method: http.MethodDelete, Path: "/wifi/guest",
		Summary:     "Stage removing the panel's guest network",
		Description: "The home network's IPv6 numbering is not changed back: that would change its devices' addresses a second time.",
		Tags:        []string{"wifi"}, Middlewares: authed, Security: authSec, Errors: guestErrors,
	}, s.removeWiFiGuest)
}

// stageGuest runs one guest edit: a dozen sections in four files, under the
// coordinator's lock (no apply can land half-way) and only on an empty draft.
func (s *Server) stageGuest(what string, fn func(core.WiFiGuestManager) ([]core.ConfigChange, error)) (*ChangesOutput, error) {
	w, ok := s.wifi()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform has no Wi-Fi")
	}
	g, ok := w.(core.WiFiGuestManager)
	if !ok {
		return nil, huma.Error501NotImplemented("this platform cannot make a guest network")
	}
	var changes []core.ConfigChange
	err := s.apply.Stage(func() error {
		if nw, ok := s.writer(); ok {
			// A draft that cannot be read is not an empty one.
			staged, err := nw.StagedChanges()
			if err != nil {
				return fmt.Errorf("%w: %w", errDraftUnreadable, err)
			}
			if len(staged) > 0 {
				return core.ErrDraftNotEmpty
			}
		}
		var err error
		changes, err = fn(g)
		return err
	})
	switch {
	case errors.Is(err, core.ErrApplyInFlight):
		return nil, huma.Error409Conflict("a change is already waiting for confirmation")
	case errors.Is(err, errDraftUnreadable):
		return nil, huma.Error502BadGateway("the draft could not be read", err)
	case errors.Is(err, core.ErrDraftNotEmpty):
		return nil, huma.Error409Conflict("other changes are waiting in the draft: apply or discard them first")
	case errors.Is(err, core.ErrGuestExists):
		return nil, huma.Error409Conflict("there is a guest network already", err)
	case errors.Is(err, core.ErrGuestBlocked):
		// GET /wifi says the same in `guest.blocked`, for the screen to word.
		return nil, huma.Error422UnprocessableEntity("guests could not be kept from the home network here", err)
	case errors.Is(err, core.ErrNoGuest):
		return nil, huma.Error404NotFound("there is no guest network", err)
	case err != nil:
		return nil, wifiError(what, err)
	}
	return changesOutput(changes), nil
}

func (s *Server) stageWiFiGuest(_ context.Context, in *StageGuestInput) (*ChangesOutput, error) {
	return s.stageGuest("making the guest network", func(g core.WiFiGuestManager) ([]core.ConfigChange, error) {
		return g.StageGuest(in.Body)
	})
}

func (s *Server) stageWiFiGuestEnabled(_ context.Context, in *StageGuestEnabledInput) (*ChangesOutput, error) {
	return s.stageGuest("turning the guest network on or off", func(g core.WiFiGuestManager) ([]core.ConfigChange, error) {
		return g.StageGuestEnabled(in.Body.Enabled)
	})
}

func (s *Server) removeWiFiGuest(_ context.Context, _ *struct{}) (*ChangesOutput, error) {
	return s.stageGuest("removing the guest network", func(g core.WiFiGuestManager) ([]core.ConfigChange, error) {
		return g.StageGuestRemove()
	})
}

// wifiError maps what the adapter says onto the answers the panel reacts to.
func wifiError(what string, err error) error {
	switch {
	case errors.Is(err, errGuestDraftPending):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, errDraftUnreadable):
		return huma.Error502BadGateway("the draft could not be read", err)
	case errors.Is(err, core.ErrNoWiFi):
		return huma.Error404NotFound("no radio on this device", err)
	case errors.Is(err, core.ErrNotImplemented):
		return huma.Error501NotImplemented(what, err)
	}
	var fe *core.FieldError
	if errors.As(err, &fe) {
		return refusal(err)
	}
	return huma.Error404NotFound(reasonFor(err))
}

func (s *Server) getWiFi(_ context.Context, in *WiFiInput) (*WiFiOutput, error) {
	w, ok := s.wifi()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform has no Wi-Fi")
	}
	st, err := w.Status()
	switch {
	case errors.Is(err, core.ErrNoWiFi):
		return nil, huma.Error404NotFound("no radio on this device", err)
	case errors.Is(err, core.ErrNotImplemented):
		return nil, huma.Error501NotImplemented("wifi", err)
	case err != nil:
		return nil, huma.Error502BadGateway("wifi", err)
	}
	// How the caller is connected comes from the device list — the same
	// "here" the devices screen marks (#53).
	if list, err := s.adapter.Device().ListDevices(); err == nil {
		core.MarkHere(list.Devices, in.from)
		st.Here = core.WiFiHereFrom(list.Devices, st.Radios)
	}
	return &WiFiOutput{Body: st}, nil
}

func (s *Server) getWiFiPassword(_ context.Context, in *WiFiNetworkIDInput) (*WiFiPasswordOutput, error) {
	w, ok := s.wifi()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform has no Wi-Fi")
	}
	p, err := w.WiFiPassword(in.ID)
	if err != nil {
		return nil, wifiError("wifi password", err)
	}
	out := &WiFiPasswordOutput{}
	out.Body.Password = p
	return out, nil
}

func (s *Server) stageWiFiRadio(_ context.Context, in *StageRadioInput) (*ChangesOutput, error) {
	w, ok := s.wifi()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform has no Wi-Fi")
	}
	if err := s.refuseWhileApplying(); err != nil {
		return nil, err
	}
	changes, err := s.lockedStage(func() ([]core.ConfigChange, error) { return w.StageRadio(in.ID, in.Body) })
	if err != nil {
		return nil, wifiError("staging a radio", err)
	}
	return changesOutput(changes), nil
}

func (s *Server) stageWiFiCountry(_ context.Context, in *StageCountryInput) (*ChangesOutput, error) {
	w, ok := s.wifi()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform has no Wi-Fi")
	}
	if err := s.refuseWhileApplying(); err != nil {
		return nil, err
	}
	changes, err := s.lockedStage(func() ([]core.ConfigChange, error) { return w.StageCountry(in.Body.Country) })
	if err != nil {
		return nil, wifiError("staging the Wi-Fi country", err)
	}
	return changesOutput(changes), nil
}

func (s *Server) setWiFiAccess(_ context.Context, in *SetAccessInput) (*AccessOutput, error) {
	w, ok := s.wifi()
	if !ok {
		return nil, huma.Error501NotImplemented("this platform has no Wi-Fi")
	}
	var changes []core.ConfigChange
	_, err := s.apply.ApplyNow(func() error {
		// Checked under the coordinator's lock: nothing can be staged into
		// a transaction between this look and the commit.
		if nw, ok := s.writer(); ok {
			if staged, err := nw.StagedChanges(); err == nil && len(staged) > 0 {
				return core.ErrDraftNotEmpty
			}
		}
		var err error
		changes, err = w.StageAccess(in.ID, in.Body)
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			return errNothingToApply
		}
		return nil
	})
	switch {
	case errors.Is(err, errNothingToApply):
		out := &AccessOutput{}
		out.Body.Changes = []core.ConfigChange{}
		return out, nil
	case errors.Is(err, core.ErrApplyInFlight):
		return nil, huma.Error409Conflict("a change is already waiting for confirmation")
	case errors.Is(err, core.ErrDraftNotEmpty):
		return nil, huma.Error409Conflict("other changes are waiting in the draft: apply or discard them first")
	case err != nil:
		var fe *core.FieldError
		if errors.As(err, &fe) || errors.Is(err, core.ErrNoWiFi) || errors.Is(err, core.ErrNotImplemented) {
			return nil, wifiError("changing the Wi-Fi network", err)
		}
		return nil, huma.Error500InternalServerError(err.Error())
	}
	out := &AccessOutput{}
	out.Body.Changes = changes
	return out, nil
}

// errDraftUnreadable: the draft could not be read, so it is not known to be empty.
var errDraftUnreadable = errors.New("the draft could not be read")

// errNothingToApply: the edit asks for what is already so.
var errNothingToApply = errors.New("nothing to apply")
