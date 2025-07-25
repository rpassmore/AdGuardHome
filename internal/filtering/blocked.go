package filtering

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/AdguardTeam/AdGuardHome/internal/aghhttp"
	"github.com/AdguardTeam/AdGuardHome/internal/filtering/rulelist"
	"github.com/AdguardTeam/AdGuardHome/internal/schedule"
	"github.com/AdguardTeam/golibs/logutil/slogutil"
	"github.com/AdguardTeam/urlfilter/rules"
)

// serviceRules maps a service ID to its filtering rules.
var serviceRules map[string][]*rules.NetworkRule

// serviceIDs contains service IDs sorted alphabetically.
var serviceIDs []string

// initBlockedServices initializes package-level blocked service data.  l must
// not be nil.
func initBlockedServices(ctx context.Context, l *slog.Logger) {
	svcLen := len(blockedServices)
	serviceIDs = make([]string, svcLen)
	serviceRules = make(map[string][]*rules.NetworkRule, svcLen)

	for i, s := range blockedServices {
		netRules := make([]*rules.NetworkRule, 0, len(s.Rules))
		for _, text := range s.Rules {
			rule, err := rules.NewNetworkRule(text, rulelist.URLFilterIDBlockedService)
			if err == nil {
				netRules = append(netRules, rule)

				continue
			}

			l.ErrorContext(
				ctx,
				"parsing blocked service rule",
				"svc", s.ID,
				"rule", text,
				slogutil.KeyError, err,
			)
		}

		serviceIDs[i] = s.ID
		serviceRules[s.ID] = netRules
	}

	slices.Sort(serviceIDs)

	l.DebugContext(ctx, "initialized services", "svc_len", svcLen)
}

// BlockedServices is the configuration of blocked services.
//
// TODO(s.chzhen):  Move to a higher-level package to allow importing the client
// package into the filtering package.
type BlockedServices struct {
	// Schedule is blocked services schedule for every day of the week.
	Schedule *schedule.Weekly `json:"schedule" yaml:"schedule"`

	// IDs is the names of blocked services.
	IDs []string `json:"ids" yaml:"ids"`
}

// Clone returns a deep copy of blocked services.
func (s *BlockedServices) Clone() (c *BlockedServices) {
	if s == nil {
		return nil
	}

	return &BlockedServices{
		Schedule: s.Schedule.Clone(),
		IDs:      slices.Clone(s.IDs),
	}
}

// Validate returns an error if blocked services contain unknown service ID.  s
// must not be nil.
func (s *BlockedServices) Validate() (err error) {
	for _, id := range s.IDs {
		_, ok := serviceRules[id]
		if !ok {
			return fmt.Errorf("unknown blocked-service %q", id)
		}
	}

	return nil
}

// ApplyBlockedServices - set blocked services settings for this DNS request
func (d *DNSFilter) ApplyBlockedServices(setts *Settings) {
	d.confMu.RLock()
	defer d.confMu.RUnlock()

	setts.ServicesRules = []ServiceEntry{}

	bsvc := d.conf.BlockedServices

	// TODO(s.chzhen):  Use startTime from [dnsforward.dnsContext].
	if !bsvc.Schedule.Contains(time.Now()) {
		d.ApplyBlockedServicesList(setts, bsvc.IDs)
	}
}

// ApplyBlockedServicesList appends filtering rules to the settings.
func (d *DNSFilter) ApplyBlockedServicesList(setts *Settings, list []string) {
	for _, name := range list {
		rules, ok := serviceRules[name]
		if !ok {
			d.logger.ErrorContext(context.TODO(), "unknown service name", "name", name)

			continue
		}

		setts.ServicesRules = append(setts.ServicesRules, ServiceEntry{
			Name:  name,
			Rules: rules,
		})
	}
}

func (d *DNSFilter) handleBlockedServicesIDs(w http.ResponseWriter, r *http.Request) {
	aghhttp.WriteJSONResponseOK(w, r, serviceIDs)
}

func (d *DNSFilter) handleBlockedServicesAll(w http.ResponseWriter, r *http.Request) {
	aghhttp.WriteJSONResponseOK(w, r, struct {
		BlockedServices []blockedService `json:"blocked_services"`
	}{
		BlockedServices: blockedServices,
	})
}

// handleBlockedServicesList is the handler for the GET
// /control/blocked_services/list HTTP API.
//
// Deprecated:  Use handleBlockedServicesGet.
func (d *DNSFilter) handleBlockedServicesList(w http.ResponseWriter, r *http.Request) {
	var list []string
	func() {
		d.confMu.Lock()
		defer d.confMu.Unlock()

		list = d.conf.BlockedServices.IDs
	}()

	aghhttp.WriteJSONResponseOK(w, r, list)
}

// handleBlockedServicesSet is the handler for the POST
// /control/blocked_services/set HTTP API.
//
// Deprecated:  Use handleBlockedServicesUpdate.
func (d *DNSFilter) handleBlockedServicesSet(w http.ResponseWriter, r *http.Request) {
	list := []string{}
	err := json.NewDecoder(r.Body).Decode(&list)
	if err != nil {
		aghhttp.Error(r, w, http.StatusBadRequest, "json.Decode: %s", err)

		return
	}

	func() {
		d.confMu.Lock()
		defer d.confMu.Unlock()

		d.conf.BlockedServices.IDs = list
		d.logger.DebugContext(r.Context(), "updated blocked services list", "len", len(list))
	}()

	d.conf.ConfigModified()
}

// handleBlockedServicesGet is the handler for the GET
// /control/blocked_services/get HTTP API.
func (d *DNSFilter) handleBlockedServicesGet(w http.ResponseWriter, r *http.Request) {
	var bsvc *BlockedServices
	func() {
		d.confMu.RLock()
		defer d.confMu.RUnlock()

		bsvc = d.conf.BlockedServices.Clone()
	}()

	aghhttp.WriteJSONResponseOK(w, r, bsvc)
}

// handleBlockedServicesUpdate is the handler for the PUT
// /control/blocked_services/update HTTP API.
func (d *DNSFilter) handleBlockedServicesUpdate(w http.ResponseWriter, r *http.Request) {
	bsvc := &BlockedServices{}
	err := json.NewDecoder(r.Body).Decode(bsvc)
	if err != nil {
		aghhttp.Error(r, w, http.StatusBadRequest, "json.Decode: %s", err)

		return
	}

	err = bsvc.Validate()
	if err != nil {
		aghhttp.Error(r, w, http.StatusUnprocessableEntity, "validating: %s", err)

		return
	}

	if bsvc.Schedule == nil {
		bsvc.Schedule = schedule.EmptyWeekly()
	}

	func() {
		d.confMu.Lock()
		defer d.confMu.Unlock()

		d.conf.BlockedServices = bsvc
	}()

	d.logger.DebugContext(r.Context(), "updated blocked services schedule", "len", len(bsvc.IDs))

	d.conf.ConfigModified()
}
