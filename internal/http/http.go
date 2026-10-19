package http

import (
	"embed"
	"errors"
	"fmt"
	"github.com/avanha/pmaas-spi/format"
	"github.com/avanha/pmaas-spi/logging"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"time"

	"github.com/avanha/pmaas-plugin-nestthermostat/data"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/common"
	"github.com/avanha/pmaas-spi"
	"github.com/avanha/pmaas-spi/environment"
)

//go:embed content/static content/templates
var contentFS embed.FS

var statusTemplate = spi.TemplateInfo{
	Name: "nestthermostat_status",
	FuncMap: template.FuncMap{
		"FormatRemaining": format.Remaining,
	},
	Paths:   []string{"templates/nestthermostat_status.htmlt"},
	Styles:  []string{"css/nestthermostat_status.css"},
	Scripts: []string{"js/nestthermostat_status.js"},
}

var thermostatTemplate = spi.TemplateInfo{
	Name: "nestthermostat_thermostat",
	FuncMap: template.FuncMap{
		"Temperature": format.Temperature, "Fahrenheit": format.Fahrenheit, "Humidity": format.Humidity,
		"RelativeTime":           format.Terse,
		"IsOffline":              isOffline,
		"IsOnline":               isOnline,
		"FormatConnectivityTime": formatConnectivityTime,
	},
	Paths:   []string{"templates/nestthermostat_thermostat.htmlt"},
	Styles:  []string{"css/nestthermostat_thermostat.css"},
	Scripts: []string{"js/nestthermostat_thermostat.js"},
}

type Handler struct {
	log         *slog.Logger
	container   spi.IPMAASContainer
	entityStore common.EntityStore
}

func NewHandler() *Handler {
	return &Handler{}
}

func (h *Handler) Init(container spi.IPMAASContainer, entityStore common.EntityStore) {
	h.container = container
	h.log = logging.For(container, "http")
	h.entityStore = entityStore
	container.ProvideContentFS(&contentFS, "content")
	container.EnableStaticContent("static")
	container.AddRoute("", h.handleHttpListRequest)
	container.AddRoute(common.OAuthCallbackPath, h.handleHttpOAuthCallbackRequest)
	container.AddJsonRoute(
		"oauthAttempt",
		func() any { return nil },
		h.handleHttpOAuthAttemptRequest)
	container.RegisterEntityRenderer(
		reflect.TypeOf((*data.PluginStatus)(nil)).Elem(),
		h.statusDataRendererFactory)
	container.RegisterEntityRenderer(
		reflect.TypeOf((*data.ThermostatData)(nil)).Elem(),
		h.thermostatDataRendererFactory)
}

func (h *Handler) handleHttpListRequest(writer http.ResponseWriter, request *http.Request) {
	result, err := h.entityStore.GetStatusAndEntities()

	if err != nil {
		h.log.Error("error retrieving entities", "error", err)
		result = common.StatusAndEntities{}
	}

	// getStatusAndEntities already returns result.Thermostats sorted by name; just take a pointer to
	// each so the render layer gets *data.ThermostatData, matching how the status header is passed.
	entityPointers := make([]any, len(result.Thermostats))

	for i := range result.Thermostats {
		entityPointers[i] = &result.Thermostats[i]
	}

	h.container.RenderList(
		writer,
		request,
		spi.RenderListOptions{
			Title:  "Nest Thermostats",
			Header: &result.Status,
		},
		entityPointers)
}

func (h *Handler) handleHttpOAuthAttemptRequest(_ http.ResponseWriter, r *http.Request, _ any) (any, error) {
	// TODO: Analyze this for vulnerabilities.
	// All we're doing is generating a secure OAuth flow URI, but maybe it might worthwhile adding some
	// CSRF protection.
	if r.Method != http.MethodPost {
		return nil, fmt.Errorf("method not allowed")
	}

	// Resolved here, on the request's own goroutine, rather than passed through to the plugin's
	// actor goroutine as a *http.Request: GetBaseUrl only needs the Host header and server config, and
	// this keeps the request object itself from ever crossing the actor boundary.
	baseUrl, err := h.container.GetBaseUrl(r)

	if err != nil {
		return nil, err
	}

	attempt, err := h.entityStore.GetOAuthAttempt(baseUrl)

	if err != nil {
		return nil, err
	}

	return attempt, nil
}

func (h *Handler) handleHttpOAuthCallbackRequest(w http.ResponseWriter, r *http.Request) {
	errCh, err := h.entityStore.ProcessOAuthCallback(r.URL)

	if err != nil {
		http.Error(w, "unable to enqueue OAuth callback", http.StatusInternalServerError)
		return
	}

	if err = <-errCh; err != nil {
		http.Error(w, "unable to process OAuth callback", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/plugins/nestthermostat/", http.StatusFound)
}

func (h *Handler) statusDataRendererFactory() (spi.EntityRenderer, error) {
	// Load the template
	template, err := h.container.GetTemplate(&statusTemplate)

	if err != nil {
		return spi.EntityRenderer{}, fmt.Errorf("unable to load nestthermostat_status template: %v", err)
	}

	// Declare a function that casts the entity to the expected type and evaluates it via the template loaded above
	renderer := func(w io.Writer, entity any) error {
		status, ok := entity.(*data.PluginStatus)

		if !ok {
			return errors.New("item is not an instance of *PluginStatus")
		}

		err := template.Instance.Execute(w, status)

		if err != nil {
			return fmt.Errorf("unable to execute nestthermostat_status template: %w", err)
		}

		return nil
	}

	return spi.EntityRenderer{
		StreamingRenderFunc: renderer,
		Styles:              template.Styles,
		Scripts:             template.Scripts,
	}, nil
}

func (h *Handler) thermostatDataRendererFactory() (spi.EntityRenderer, error) {
	// Load the template
	compiledTemplate, err := h.container.GetTemplate(&thermostatTemplate)

	if err != nil {
		return spi.EntityRenderer{}, fmt.Errorf("unable to load nestthermostat_thermostat template: %v", err)
	}

	// Declare a function that casts the entity to the expected type and evaluates it via the template loaded above
	renderer := func(w io.Writer, entity any) error {
		thermostat, ok := entity.(*data.ThermostatData)

		if !ok {
			return errors.New("item is not an instance of *ThermostatData")
		}

		err := compiledTemplate.Instance.Execute(w, thermostat)

		if err != nil {
			return fmt.Errorf("unable to execute nestthermostat_thermostat template: %w", err)
		}

		return nil
	}

	return spi.EntityRenderer{
		StreamingRenderFunc: renderer,
		Styles:              compiledTemplate.Styles,
		Scripts:             compiledTemplate.Scripts,
	}, nil
}

func isOffline(connectivity environment.Connectivity) bool {
	return connectivity == environment.ConnectivityOffline
}

func isOnline(connectivity environment.Connectivity) bool {
	return connectivity == environment.ConnectivityOnline
}

// formatConnectivityTime formats an OfflineSince/OnlineSince timestamp for display, distinguishing a
// state that's genuinely never been observed (zero time.Time) from a real timestamp — text/template has
// no way to test IsZero on its own.
func formatConnectivityTime(timeValue time.Time) string {
	if timeValue.IsZero() {
		return "Unknown"
	}

	return timeValue.Format("2006-01-02 3:04:05 PM")
}
