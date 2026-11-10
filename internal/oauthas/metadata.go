package oauthas

import (
	"net/http"
	"net/url"
)

// Handler serves the metadata documents and the OAuth endpoints. Mount it
// for AuthorizationServerMetadataPath, ProtectedResourceMetadataPrefix
// (with and without a trailing slash) and OAuthPrefix.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+AuthorizationServerMetadataPath, s.handleServerMetadata)
	mux.HandleFunc("GET "+s.protectedResourceMetadataPath(), s.handleResourceMetadata)
	mux.HandleFunc("GET "+AuthorizePath, s.handleAuthorize)
	mux.HandleFunc("POST "+AuthorizePath, s.handleConsent)
	mux.HandleFunc("POST "+TokenPath, s.handleToken)
	if s.cfg.DynamicRegistration {
		mux.HandleFunc("POST "+RegisterPath, s.handleRegister)
	}
	return mux
}

// Mount routes every path Handler serves on mux.
func (s *Server) Mount(mux *http.ServeMux) {
	h := s.Handler()
	mux.Handle(AuthorizationServerMetadataPath, h)
	mux.Handle(ProtectedResourceMetadataPrefix, h)
	mux.Handle(ProtectedResourceMetadataPrefix+"/", h)
	mux.Handle(OAuthPrefix, h)
}

// ProtectedResourceMetadataURL is where the metadata of the protected
// resource is published (RFC 9728 section 3.1): the well-known prefix
// inserted between the resource's host and path. The MCP endpoint names it
// in WWW-Authenticate.
func (s *Server) ProtectedResourceMetadataURL() string {
	u, _ := url.Parse(s.cfg.Resource) // validated by New
	return u.Scheme + "://" + u.Host + s.protectedResourceMetadataPath()
}

func (s *Server) protectedResourceMetadataPath() string {
	u, _ := url.Parse(s.cfg.Resource) // validated by New
	return ProtectedResourceMetadataPrefix + u.EscapedPath()
}

// ServerMetadata is the Authorization Server Metadata document (RFC 8414).
type ServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint,omitempty"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	ResponseModesSupported            []string `json:"response_modes_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	AuthorizationResponseIssParameter bool     `json:"authorization_response_iss_parameter_supported"`
}

// ResourceMetadata is the Protected Resource Metadata document (RFC 9728).
type ResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceName           string   `json:"resource_name,omitempty"`
}

// Metadata returns this server's metadata document.
func (s *Server) Metadata() ServerMetadata {
	m := ServerMetadata{
		Issuer:                            s.cfg.Issuer,
		AuthorizationEndpoint:             s.cfg.Issuer + AuthorizePath,
		TokenEndpoint:                     s.cfg.Issuer + TokenPath,
		ScopesSupported:                   normalizeScopes(s.cfg.ScopesSupported),
		ResponseTypesSupported:            []string{"code"},
		ResponseModesSupported:            []string{"query"},
		GrantTypesSupported:               []string{"authorization_code"},
		TokenEndpointAuthMethodsSupported: []string{"none"},
		CodeChallengeMethodsSupported:     []string{"S256"},
		AuthorizationResponseIssParameter: true,
	}
	if s.cfg.DynamicRegistration {
		m.RegistrationEndpoint = s.cfg.Issuer + RegisterPath
	}
	return m
}

// ResourceMetadata returns the metadata document of the protected resource.
func (s *Server) ResourceMetadata() ResourceMetadata {
	return ResourceMetadata{Resource: s.cfg.Resource, AuthorizationServers: []string{s.cfg.Issuer},
		ScopesSupported: normalizeScopes(s.cfg.ScopesSupported), BearerMethodsSupported: []string{"header"},
		ResourceName: s.cfg.ResourceName}
}

func (s *Server) handleServerMetadata(w http.ResponseWriter, _ *http.Request) {
	publicJSON(w, s.Metadata())
}

func (s *Server) handleResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	publicJSON(w, s.ResourceMetadata())
}

// publicJSON serves a public discovery document that any origin may read.
func publicJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, v)
}
