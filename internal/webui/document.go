package webui

import (
	"encoding/json"
	"net/http"

	"github.com/nscaledev/devflow/internal/consolecfg"
)

type documentSource struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Base    string `json:"base_digest"`
}

func (s *server) document(w http.ResponseWriter, r *http.Request) {
	var in documentSource
	if !read(w, r, &in) {
		return
	}
	doc, err := consolecfg.DescribeDocument(r.Context(), s.repo.Dir, in.Path, in.Content, in.Base)
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	reply(w, http.StatusOK, doc)
}

func (s *server) editDocument(w http.ResponseWriter, r *http.Request) {
	var in struct {
		documentSource
		Keys      []string        `json:"keys"`
		Operation string          `json:"operation"`
		Value     json.RawMessage `json:"value"`
	}
	if !read(w, r, &in) {
		return
	}
	if in.Keys == nil || (len(in.Keys) == 0 && in.Operation == "remove") || (in.Operation != "set" && in.Operation != "remove") ||
		(in.Operation == "set" && in.Value == nil) {
		problem(w, http.StatusBadRequest, "choose a document field and a valid edit")
		return
	}
	doc, err := consolecfg.DescribeDocument(r.Context(), s.repo.Dir, in.Path, in.Content, in.Base)
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	content, err := consolecfg.EditDocument(in.Content, doc.Format, in.Keys, in.Value, in.Operation == "remove", doc.Schema)
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.composed(w, r, in.Path, content, in.Base)
}
