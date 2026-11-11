package economy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

// The operator-document staff API (PLAN Roadmap 8, P3; slice 8.5): loopback only, on the admin listener. A document
// is one operator data file (AGENTS "Operator-editable fabricated content"); a published version applies at the next
// restart, never to the running process. Actor "loopback". Replies are JSON; an error is {"error": "..."} and carries
// the validator's message. The public admin UI (slice 8.7) builds on the same store functions.
//
//	curl -X POST "http://127.0.0.1:8099/admin/docs/cherrytips.json?note=test" --data-binary "@body.json"
//	curl -X POST http://127.0.0.1:8099/admin/docs/cherrytips.json/2/validate
//	curl -X POST http://127.0.0.1:8099/admin/docs/cherrytips.json/2/publish
//	curl -X POST http://127.0.0.1:8099/admin/docs/cherrytips.json/rollback

const docActor = "loopback"

// docsLoopback serves h to loopback clients only; anything else gets the 404 the other staff routes give.
func docsLoopback(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			httpx.ServeNotFound(w)
			return
		}
		h(w, r)
	}
}

func writeDocJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	httpx.WriteJSON(w, status, string(b))
}

// writeDocError maps a store error to its status by the sentinel it wraps.
func writeDocError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrNoDatabase):
		status = http.StatusServiceUnavailable
	case errors.Is(err, store.ErrNotDocument), errors.Is(err, store.ErrNoVersion):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrInvalidDocument):
		status = http.StatusUnprocessableEntity
	case errors.Is(err, store.ErrDocumentState):
		status = http.StatusConflict
	case errors.Is(err, store.ErrDocumentTooLarge):
		status = http.StatusRequestEntityTooLarge
	case errors.Is(err, store.ErrBadInput):
		status = http.StatusBadRequest
	}
	writeDocJSON(w, status, map[string]string{"error": err.Error()})
}

// docVersionArg reads the {version} path value; ok is false (and the reply written) when it is not a positive number.
func docVersionArg(w http.ResponseWriter, r *http.Request) (int, bool) {
	v, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || v < 1 {
		writeDocError(w, fmt.Errorf("%w: the version must be a positive number", store.ErrBadInput))
		return 0, false
	}
	return v, true
}

// GET /admin/docs: every document with what it runs on, its published version and whether a restart changes it.
func handleDocsList(w http.ResponseWriter, r *http.Request) {
	docs, err := store.Live()
	if err != nil {
		writeDocError(w, err)
		return
	}
	writeDocJSON(w, http.StatusOK, map[string]any{"documents": docs})
}

// GET /admin/docs/{name}: the stored versions, newest first, without bodies.
func handleDocsHistory(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	versions, err := store.History(name)
	if err != nil {
		writeDocError(w, err)
		return
	}
	writeDocJSON(w, http.StatusOK, map[string]any{"name": name, "versions": versions})
}

// GET /admin/docs/{name}/{version}: one version with its body.
func handleDocsGet(w http.ResponseWriter, r *http.Request) {
	version, ok := docVersionArg(w, r)
	if !ok {
		return
	}
	v, body, err := store.Get(r.PathValue("name"), version)
	if err != nil {
		writeDocError(w, err)
		return
	}
	writeDocJSON(w, http.StatusOK, map[string]any{"version": v, "body": json.RawMessage(body)})
}

// POST /admin/docs/{name}: the body is the document's JSON; it is stored as the next draft (?note= is the operator's note).
func handleDocsDraft(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, store.MaxDocumentBytes+1))
	if err != nil {
		writeDocError(w, fmt.Errorf("%w: %v", store.ErrBadInput, err))
		return
	}
	v, err := store.SaveDraft(r.PathValue("name"), raw, docActor, r.URL.Query().Get("note"))
	if err != nil {
		writeDocError(w, err)
		return
	}
	writeDocJSON(w, http.StatusOK, v)
}

// POST /admin/docs/{name}/{version}/validate: the validator's verdict on that version; nothing is changed.
func handleDocsValidate(w http.ResponseWriter, r *http.Request) {
	version, ok := docVersionArg(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	artSkipped, err := store.Validate(name, version)
	if errors.Is(err, store.ErrInvalidDocument) {
		writeDocJSON(w, http.StatusUnprocessableEntity, map[string]any{"name": name, "version": version, "valid": false, "error": err.Error()})
		return
	}
	if err != nil {
		writeDocError(w, err)
		return
	}
	writeDocJSON(w, http.StatusOK, map[string]any{"name": name, "version": version, "valid": true, "artSkipped": artSkipped})
}

// POST /admin/docs/{name}/{version}/publish: makes the version the one a restart loads (refused when it fails validation).
func handleDocsPublish(w http.ResponseWriter, r *http.Request) {
	version, ok := docVersionArg(w, r)
	if !ok {
		return
	}
	res, err := store.Publish(r.PathValue("name"), version, docActor)
	if err != nil {
		writeDocError(w, err)
		return
	}
	writeDocJSON(w, http.StatusOK, res)
}

// POST /admin/docs/{name}/rollback: re-publishes the version the published one replaced, or un-publishes the document.
func handleDocsRollback(w http.ResponseWriter, r *http.Request) {
	res, err := store.Rollback(r.PathValue("name"), docActor)
	if err != nil {
		writeDocError(w, err)
		return
	}
	writeDocJSON(w, http.StatusOK, res)
}
