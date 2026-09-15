package runtimeconfig

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Marstheway/oh-my-api/internal/config"
)

const (
	CascadeModeDisabled = "disabled"
	CascadeModeHub      = "hub"
	CascadeModeSpoke    = "spoke"
)

// CascadeConfigInput is the PUT body for the draft cascade endpoint.
type CascadeConfigInput struct {
	Mode  string             `json:"mode"`
	Hub   *CascadeHubInput   `json:"hub,omitempty"`
	Spoke *CascadeSpokeInput `json:"spoke,omitempty"`
}

// CascadeHubInput describes the hub role configuration.
// Token is required on every PUT (including edits); there is no omit-to-keep.
type CascadeHubInput struct {
	ProviderName string `json:"provider_name"`
	Token        string `json:"token"`
}

// CascadeSpokeInput describes the spoke role configuration.
// Token is required on every PUT (including edits); there is no omit-to-keep.
type CascadeSpokeInput struct {
	Hub   string `json:"hub"`
	Token string `json:"token"`
	Peer  string `json:"peer"`
}

// CascadeConfigView is the GET response for the draft cascade endpoint.
// Shared tokens are returned in plaintext so the admin UI can display and edit them.
type CascadeConfigView struct {
	Mode      string            `json:"mode"`
	Hub       *CascadeHubView   `json:"hub,omitempty"`
	Spoke     *CascadeSpokeView `json:"spoke,omitempty"`
	Providers []string          `json:"providers"`
}

// CascadeHubView is the hub portion of CascadeConfigView.
type CascadeHubView struct {
	ProviderName string `json:"provider_name"`
	Token        string `json:"token"`
}

// CascadeSpokeView is the spoke portion of CascadeConfigView.
type CascadeSpokeView struct {
	Hub   string `json:"hub"`
	Token string `json:"token"`
	Peer  string `json:"peer"`
}

// CascadeCRUD mutates the draft top-level cascade block and hub provider.
type CascadeCRUD struct {
	draft *config.Config
}

// NewCascadeCRUD creates a cascade draft CRUD service.
func NewCascadeCRUD(draft *config.Config) *CascadeCRUD {
	return &CascadeCRUD{draft: draft}
}

// View returns a normalized draft cascade config, including plaintext tokens.
func (c *CascadeCRUD) View() CascadeConfigView {
	hubName, hubCfg := c.enabledProvider()

	var hub *CascadeHubView
	if hubName != "" && hubCfg.Cascade != nil {
		hub = &CascadeHubView{
			ProviderName: hubName,
			Token:        strings.TrimSpace(hubCfg.Cascade.Token),
		}
	}

	var spoke *CascadeSpokeView
	if c.draft.Cascade != nil {
		spoke = &CascadeSpokeView{
			Hub:   c.draft.Cascade.Hub,
			Token: strings.TrimSpace(c.draft.Cascade.Token),
			Peer:  c.draft.Cascade.Peer,
		}
	}

	mode := CascadeModeDisabled
	if hub != nil {
		mode = CascadeModeHub
	} else if spoke != nil {
		mode = CascadeModeSpoke
	}

	return CascadeConfigView{
		Mode:      mode,
		Hub:       hub,
		Spoke:     spoke,
		Providers: c.peerProviderNames(hubName),
	}
}

// Update applies the requested role to the draft config.
func (c *CascadeCRUD) Update(input *CascadeConfigInput) error {
	if input == nil {
		return &Error{Code: ErrCodeBadRequest, Field: "mode", Message: "mode is required"}
	}

	switch input.Mode {
	case CascadeModeDisabled:
		return c.applyDisabled()
	case CascadeModeHub:
		return c.applyHub(input.Hub)
	case CascadeModeSpoke:
		return c.applySpoke(input.Spoke)
	default:
		return &Error{Code: ErrCodeBadRequest, Field: "mode", Message: "must be one of disabled, hub, spoke"}
	}
}

func (c *CascadeCRUD) enabledProvider() (string, config.ProviderConfig) {
	for name, p := range c.draft.Providers.Items {
		if p.Cascade != nil && p.Cascade.Enabled {
			return name, p
		}
	}
	return "", config.ProviderConfig{}
}

// peerProviderNames returns provider names usable as the spoke peer. The current
// hub provider (if any) is excluded because it is a session-backed marker, not a
// real HTTP upstream.
func (c *CascadeCRUD) peerProviderNames(exclude string) []string {
	names := make([]string, 0, len(c.draft.Providers.Items))
	for name := range c.draft.Providers.Items {
		if name == exclude {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *CascadeCRUD) applyDisabled() error {
	if err := c.validateNoReferencedCascadeProvider(""); err != nil {
		return err
	}

	c.draft.Cascade = nil
	for name, p := range c.draft.Providers.Items {
		if p.Cascade == nil || !p.Cascade.Enabled {
			continue
		}
		c.clearProviderCascade(name, p)
	}
	return nil
}

func (c *CascadeCRUD) applyHub(input *CascadeHubInput) error {
	if input == nil {
		return &Error{Code: ErrCodeBadRequest, Field: "hub", Message: "hub configuration is required for mode=hub"}
	}

	name := strings.TrimSpace(input.ProviderName)
	if name == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "hub.provider_name", Message: "must not be empty"}
	}

	// A hub is a session-backed marker, not an HTTP upstream. Refuse to convert an
	// existing non-cascade provider in place — that would wipe its endpoint and
	// break model groups / rules that still reference it.
	if existing, ok := c.draft.Providers.Items[name]; ok {
		if existing.Cascade == nil || !existing.Cascade.Enabled {
			return &Error{
				Code:    ErrCodeConflict,
				Field:   "hub.provider_name",
				Message: fmt.Sprintf("provider %q already exists; choose a different name to avoid overwriting it", name),
			}
		}
	}

	// Switching hub identity may require demoting the previous hub provider; refuse
	// before mutating if that provider is still referenced by model groups.
	if err := c.validateNoReferencedCascadeProvider(name); err != nil {
		return err
	}

	token := strings.TrimSpace(input.Token)
	if token == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "hub.token", Message: "must not be empty"}
	}

	// Clear any other cascade-enabled provider so at most one remains.
	for n, p := range c.draft.Providers.Items {
		if n == name {
			continue
		}
		if p.Cascade == nil || !p.Cascade.Enabled {
			continue
		}
		c.clearProviderCascade(n, p)
	}

	p := c.draft.Providers.Items[name]
	p.Endpoint = ""
	p.Endpoints = nil
	p.Protocols = []string{"openai.chat", "openai.responses", "anthropic.messages"}
	p.RemoteBridge = nil
	p.Cascade = &config.ProviderCascadeConfig{Enabled: true, Token: token}
	c.draft.Providers.Items[name] = p

	c.draft.Cascade = nil
	return nil
}

func (c *CascadeCRUD) applySpoke(input *CascadeSpokeInput) error {
	if input == nil {
		return &Error{Code: ErrCodeBadRequest, Field: "spoke", Message: "spoke configuration is required for mode=spoke"}
	}

	if err := c.validateNoReferencedCascadeProvider(""); err != nil {
		return err
	}

	hub := strings.TrimSpace(input.Hub)
	if err := config.ValidateSpokeHubOrigin(hub); err != nil {
		return &Error{Code: ErrCodeBadRequest, Field: "spoke.hub", Message: err.Error()}
	}

	peer := strings.TrimSpace(input.Peer)
	if peer == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "spoke.peer", Message: "must not be empty"}
	}
	if _, ok := c.draft.Providers.Items[peer]; !ok {
		return &Error{Code: ErrCodeNotFound, Field: "spoke.peer", Message: fmt.Sprintf("provider %q not found", peer)}
	}
	if curName, _ := c.enabledProvider(); curName != "" && peer == curName {
		return &Error{Code: ErrCodeBadRequest, Field: "spoke.peer", Message: "cascade hub provider cannot be used as peer"}
	}

	token := strings.TrimSpace(input.Token)
	if token == "" {
		return &Error{Code: ErrCodeBadRequest, Field: "spoke.token", Message: "must not be empty"}
	}

	c.draft.Cascade = &config.SpokeCascadeConfig{
		Hub:   hub,
		Token: token,
		Peer:  peer,
	}

	// Cascade roles are mutually exclusive; clear any hub provider.
	for name, p := range c.draft.Providers.Items {
		if p.Cascade == nil || !p.Cascade.Enabled {
			continue
		}
		c.clearProviderCascade(name, p)
	}

	return nil
}

// clearProviderCascade removes the cascade block from a provider. When the
// provider was only a hub marker (no endpoint and no model-group references),
// it is removed so the resulting draft stays applicable.
func (c *CascadeCRUD) clearProviderCascade(name string, p config.ProviderConfig) {
	p.Cascade = nil
	if c.providerIsCascadeOnly(name, p) {
		delete(c.draft.Providers.Items, name)
		return
	}
	c.draft.Providers.Items[name] = p
}

func (c *CascadeCRUD) providerIsCascadeOnly(name string, p config.ProviderConfig) bool {
	if providerHasEndpoint(p) {
		return false
	}
	return len(c.referencingGroups(name)) == 0
}

// validateNoReferencedCascadeProvider rejects a role transition when a cascade
// provider without an endpoint is still referenced by model groups. Deleting it
// would break the model-group reference, while keeping it without a cascade block
// would break the endpoint requirement, so the operator must resolve the groups first.
func (c *CascadeCRUD) validateNoReferencedCascadeProvider(exceptName string) *Error {
	for name, p := range c.draft.Providers.Items {
		if name == exceptName {
			continue
		}
		if p.Cascade == nil || !p.Cascade.Enabled {
			continue
		}
		if providerHasEndpoint(p) {
			continue
		}
		if groups := c.referencingGroups(name); len(groups) > 0 {
			return &Error{
				Code:    ErrCodeConflict,
				Field:   "providers." + name,
				Message: fmt.Sprintf("cascade provider %q is referenced by model groups %s; update or remove those groups before disabling cascade", name, strings.Join(groups, ", ")),
			}
		}
	}
	return nil
}

func (c *CascadeCRUD) referencingGroups(name string) []string {
	groups := make([]string, 0)
	for _, g := range c.draft.ModelGroups {
		for _, e := range g.Models {
			if strings.HasPrefix(e.Model, name+"/") {
				groups = append(groups, g.Name)
				break
			}
		}
	}
	sort.Strings(groups)
	return groups
}

func providerHasEndpoint(p config.ProviderConfig) bool {
	if strings.TrimSpace(p.Endpoint) != "" {
		return true
	}
	for _, ep := range p.Endpoints {
		if strings.TrimSpace(ep.URL) != "" {
			return true
		}
	}
	return false
}
