// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
)

// fork: CallDrift runs the /api/drift/detect or /api/drift/remediate handler
// in-process (no network hop) with the configured API secret, for the drift
// scheduler. Contracts and handlers are untouched; see docs/adr/0006.
func (a *APIController) CallDrift(path string, body []byte) (int, []byte) {
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	r.Header.Set(atlantisTokenHeader, string(a.APISecret))
	w := httptest.NewRecorder()
	switch path {
	case "/api/drift/detect":
		a.DetectDrift(w, r)
	case "/api/drift/remediate":
		a.Remediate(w, r)
	default:
		return http.StatusNotFound, nil
	}
	return w.Code, w.Body.Bytes()
}
