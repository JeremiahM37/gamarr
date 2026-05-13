package api

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.json
var openapiSpec []byte

// handleOpenAPI serves the OpenAPI 3.1 spec describing Gamarr's HTTP API.
// AI agents and tooling can introspect this to discover endpoints, request
// shapes, and response shapes without prior knowledge of the codebase.
func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Cache-Control", "public, max-age=600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openapiSpec)
}
