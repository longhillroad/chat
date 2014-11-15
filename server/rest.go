/******************************************************************************
 *
 *  Copyright (C) 2014 Tinode, All Rights Reserved
 * 
 *  This program is free software; you can redistribute it and/or modify it 
 *  under the terms of the GNU General Public License as published by the Free 
 *  Software Foundation; either version 3 of the License, or (at your option) 
 *  any later version.
 *
 *  This program is distributed in the hope that it will be useful, but 
 *  WITHOUT ANY WARRANTY; without even the implied warranty of MERCHANTABILITY 
 *  or FITNESS FOR A PARTICULAR PURPOSE. 
 *  See the GNU General Public License for more details.
 *
 *  You should have received a copy of the GNU General Public License along with
 *  this program; if not, see <http://www.gnu.org/licenses>.
 *
 *  This code is available under licenses for commercial use.
 *
 *  File        :  rest.go
 *  Author      :  Gene Sokolov
 *  Created     :  18-May-2014
 *
 ******************************************************************************
 *
 *  Description : 
 *
 *  Basic REST handling: login, getting user details, handling of stored messages
 *
 *****************************************************************************/
package main

import (
	"encoding/json"
	"errors"
	"github.com/gorilla/mux"
	"github.com/or-else/guid"
	"log"
	"net/http"
)

type SearchQuerySort struct {
	Term string `json:"term"`
	Desc bool   `json:"desc,omitempty"`
}

type SearchQueryRange struct {
	From []interface{} `json:"from"`
	To   []interface{} `json:"to"`
}

// Parsed search query
type SearchQuery struct {
	Type    string           `json:"type,omitempty"` // "match" (default) or "range",
	Index   string           `json:"index"`
	Match   []string         `json:"match,omitempty"` // terms to match
	Range   SearchQueryRange `json:"range,omitempty"` // Between search
	Limit   uint             `json:"limit,omitempty"`
	Offset  uint             `json:"offset,omitempty"`
	OrderBy struct {
		UseIndex  bool              `json:"useIndex"`
		IndexDesc bool              `json:"indexDesc"`
		Terms     []SearchQuerySort `json:"terms"`
	} `json:"orderBy,omitempty"`
}

type context struct {
	wrt  http.ResponseWriter
	req  *http.Request
	code int

	appid  uint32
	isRoot bool
	uid    guid.GUID

	// is this a /kind/id (level=0) or /kind/id/subkind/id? (level=1)
	level int

	kind  string
	objId string

	parentKind string
	parentId   string

	query SearchQuery
	body  map[string]interface{}
}

// Write default headers
func (ctx *context) WriteHeaders() {
	ctx.wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
	ctx.wrt.Header().Set("Access-Control-Allow-Origin", "*")
	ctx.wrt.WriteHeader(ctx.code)
}

func MountCrud(router *mux.Router) {
	// Handle user authentication
	router.HandleFunc("/login", chainedHandler(
		getAppId, userLogin)).Methods("GET", "POST")

	// User management
	// Create - ignoring current user
	router.HandleFunc("/users", chainedHandler(
		getAppId, userKindParams, readRequestBody, create)).Methods("POST")
	// Read
	router.HandleFunc("/users", chainedHandler(
		getAppId, readCurrentUser, userKindParams, parseGetAllQuery, getAll)).Methods("GET")
	router.HandleFunc("/users/:{id}", chainedHandler(
		getAppId, readCurrentUser, userKindParams, get)).Methods("GET")
	// Update
	router.HandleFunc("/users/:{id}", chainedHandler(
		getAppId, readCurrentUser, userKindParams, readRequestBody, update)).Methods("PUT", "PATCH")
	// Delete
	router.HandleFunc("/users/:{id}", chainedHandler(
		getAppId, readCurrentUser, userKindParams, del)).Methods("DELETE")

	// Messages
	// List messages
	router.HandleFunc("/messages", chainedHandler(
		getAppId, readCurrentUser, messageKindParams, parseGetMessagesQuery, getAll)).Methods("GET")
	router.HandleFunc("/messages/:{id}", chainedHandler(
		getAppId, readCurrentUser, messageKindParams, get)).Methods("GET")

	// Generic objects
	// Create
	router.HandleFunc("/{kind}", chainedHandler(
		getAppId, readCurrentUser, loadURLParams, readRequestBody, create)).Methods("POST")

	// Read
	router.HandleFunc("/{kind}", chainedHandler(
		getAppId, readCurrentUser, loadURLParams, parseGetAllQuery, getAll)).Methods("GET")
	router.HandleFunc("/{kind}/:{id}", chainedHandler(
		getAppId, readCurrentUser, loadURLParams, get)).Methods("GET")

	// Update
	router.HandleFunc("/{kind}/:{id}", chainedHandler(
		getAppId, readCurrentUser, loadURLParams, readRequestBody, update)).Methods("PUT", "PATCH")

	// Delete
	router.HandleFunc("/{kind}/:{id}", chainedHandler(
		getAppId, readCurrentUser, loadURLParams, del)).Methods("DELETE")

	// No support for deleteAll by design (for now)

	// Child objects
	// Create
	router.HandleFunc("/{kind}/:{id}/{subkind}", chainedHandler(
		getAppId, readCurrentUser, loadURLParams2, readRequestBody, create)).Methods("POST")

	// Read
	router.HandleFunc("/{kind}/:{id}/{subkind}", chainedHandler(
		getAppId, readCurrentUser, loadURLParams2, parseGetAllQuery, getAll)).Methods("GET")
	router.HandleFunc("/{kind}/:{id}/{subkind}/:{id2}", chainedHandler(
		getAppId, readCurrentUser, loadURLParams2, get)).Methods("GET")

	// Update
	router.HandleFunc("/{kind}/:{id}/{subkind}/:{id2}", chainedHandler(
		getAppId, readCurrentUser, loadURLParams2, readRequestBody, update)).Methods("PUT", "PATCH")

	// Delete
	router.HandleFunc("/{kind}/:{id}/{subkind}/:{id2}", chainedHandler(
		getAppId, readCurrentUser, loadURLParams2, del, restLogger)).Methods("DELETE")

	// OPTIONS routes for API discovery
	router.HandleFunc("/{kind}", optionsKind).Methods("OPTIONS")
	router.HandleFunc("/{kind}/:{id}", optionsResource).Methods("OPTIONS")
	router.HandleFunc("/{kind}/:{id}/{subkind}", optionsKind).Methods("OPTIONS")
	router.HandleFunc("/{kind}/:{id}/{subkind}/:{id2}", optionsResource).Methods("OPTIONS")

	router.NotFoundHandler = http.HandlerFunc(func(wrt http.ResponseWriter, req *http.Request) {
		errorResponse(wrt, nil, http.StatusNotFound)
	})
}

func chainedHandler(handlers ...func(c *context) error) http.HandlerFunc {
	return func(wrt http.ResponseWriter, req *http.Request) {
		ctx := &context{wrt: wrt, req: req}
		for _, f := range handlers {
			if err := f(ctx); err != nil {
				errorResponse(wrt, err, ctx.code)
				break
			}
		}
		restLogger(ctx)
	}
}

func errorResponse(wrt http.ResponseWriter, err error, code int) {
	wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
	wrt.Header().Set("Access-Control-Allow-Origin", "*")
	wrt.WriteHeader(code)

	var text string
	if err == nil {
		text = http.StatusText(code)
	} else {
		text = err.Error()
		log.Println(err)
	}

	json.NewEncoder(wrt).Encode(&ServerComMessage{Ctrl: &MsgServerCtrl{Code: code, Text: text}})
}

func getApiKey(req *http.Request) string {
	apikey := req.FormValue("apikey")
	if apikey == "" {
		apikey = req.Header.Get("X-Tinode-APIKey")
	}
	return apikey
}

func getAppId(ctx *context) error {
	apikey := getApiKey(ctx.req)

	if apikey == "" {
		ctx.code = http.StatusUnauthorized
		return errors.New("Missing API key")
	}

	ctx.appid, ctx.isRoot = checkApiKey(apikey)
	if ctx.appid == 0 {
		ctx.code = http.StatusUnauthorized
		return errors.New("Invalid API key")
	}

	return nil
}

// If no token is present, leave UID empty (anonymous user access is generally permitted)
// Otherwise check token for validity
func readCurrentUser(ctx *context) error {
	token := ctx.req.FormValue("token")
	if token == "" {
		token = ctx.req.Header.Get("X-Tinode-Token")
	}

	if token == "" {
		return nil
	}

	uid, _ := checkSecurityToken(token)
	if uid.IsZero() {
		ctx.code = http.StatusUnauthorized
		return errors.New("Invalid or expired security token")
	}

	ctx.uid = uid
	return nil
}

func readRequestBody(ctx *context) error {
	if err := json.NewDecoder(ctx.req.Body).Decode(&ctx.body); err != nil {
		ctx.code = http.StatusBadRequest
		return err
	}

	return nil
}

func loadURLParams(ctx *context) error {
	vars := mux.Vars(ctx.req)
	ctx.kind = vars["kind"]
	ctx.objId = vars["id"]
	if len(ctx.objId) == guid.Base64Length {
		if g := guid.ParseBase64(ctx.objId); !g.IsZero() {
			ctx.objId = g.String()
		}
	}
	return nil
}

func loadURLParams2(ctx *context) error {
	vars := mux.Vars(ctx.req)
	ctx.kind = vars["subkind"]
	ctx.objId = vars["id2"]
	if len(ctx.objId) == guid.Base64Length {
		if g := guid.ParseBase64(ctx.objId); !g.IsZero() {
			ctx.objId = g.String()
		}
	}
	ctx.parentKind = vars["kind"]
	ctx.parentId = vars["id"]
	if len(ctx.parentId) == guid.Base64Length {
		if g := guid.ParseBase64(ctx.parentId); !g.IsZero() {
			ctx.parentId = g.String()
		}
	}

	ctx.level = 1
	return nil
}

// Parse search query.
// Query could be
//  _q={} (see SearchQuery struct for valid JSON content of a query)
// or a single
//  key=val
// where val is an array of strings
func parseGetAllQuery(ctx *context) error {
	if q := ctx.req.FormValue("_q"); q != "" {
		if err := json.Unmarshal([]byte(q), &ctx.query); err != nil {
			ctx.code = http.StatusBadRequest
			return err
		}
		if ctx.query.Type == "" {
			ctx.query.Type = "match"
		}
	} else {
		// Use first unknown parameter as a query term
		for key, val := range ctx.req.Form {
			if key != "sid" && key != "apikey" && key != "token" && key != "" {
				ctx.query.Type = "match"
				ctx.query.Index = key
				ctx.query.Match = val
			}
		}
	}
	return nil
}

func restLogger(ctx *context) error {
	log.Println(ctx.req.Method, ctx.req.RequestURI)
	return nil
}

func denyAll(ctx *context) error {
	ctx.code = http.StatusForbidden
	return errors.New("Permission denyed")
}

func rootOnly(ctx *context) error {
	if !ctx.isRoot {
		ctx.code = http.StatusForbidden
		return errors.New("Permission denyed")
	}
	return nil
}

func create(ctx *context) error {
	// Write to storage
	id, code := storage.Create(ctx, ctx.body)
	ctx.code = code
	if id == "" {
		return errors.New("Failed to create object")
	}

	// Write response
	ctx.WriteHeaders()
	if err := json.NewEncoder(ctx.wrt).Encode(&ServerComMessage{
		Ctrl: &MsgServerCtrl{Code: ctx.code, Text: http.StatusText(ctx.code),
			Params: map[string]interface{}{"id": id}}}); err != nil {
		log.Println(err)
	}

	ctx.objId = id
	return nil
}

func get(ctx *context) error {
	// Load data from storage
	data, code := storage.Get(ctx)
	ctx.code = code
	if data == nil {
		return errors.New(http.StatusText(ctx.code))
	}

	// TODO(gene): add Last-Modified header
	//ctx.wrt.Head("Last-Modified:", ...)
	ctx.WriteHeaders()
	if err := json.NewEncoder(ctx.wrt).Encode(data); err != nil {
		log.Println(err)
	}

	return nil
}

func getAll(ctx *context) error {
	// Load a list of objects
	data, code := storage.GetAll(ctx)
	ctx.code = code

	if data == nil {
		return errors.New(http.StatusText(ctx.code))
	}

	// write response
	ctx.WriteHeaders()
	if err := json.NewEncoder(ctx.wrt).Encode(data); err != nil {
		log.Println(err)
	}

	return nil
}

func update(ctx *context) error {
	// update resource
	ctx.code = storage.Update(ctx, ctx.body)

	// write response
	ctx.WriteHeaders()
	if err := json.NewEncoder(ctx.wrt).Encode(&ServerComMessage{
		Ctrl: &MsgServerCtrl{Code: ctx.code, Text: http.StatusText(ctx.code),
			Params: map[string]interface{}{"id": ctx.objId, "updatedAt": ctx.body["updatedAt"]}}}); err != nil {
		log.Println(err)
	}

	return nil
}

func del(ctx *context) error {
	// delete resource
	ctx.code = storage.Delete(ctx)

	// write response
	ctx.WriteHeaders()
	if err := json.NewEncoder(ctx.wrt).Encode(&ServerComMessage{
		Ctrl: &MsgServerCtrl{Code: ctx.code, Text: http.StatusText(ctx.code)}}); err != nil {
		log.Println(err)
	}

	return nil
}

func optionsKind(wrt http.ResponseWriter, req *http.Request) {
	h := wrt.Header()

	h.Add("Allow", "PUT")
	h.Add("Allow", "PATCH")
	h.Add("Allow", "GET")
	h.Add("Allow", "DELETE")
	h.Add("Allow", "OPTIONS")

	wrt.WriteHeader(http.StatusOK)
}

func optionsResource(wrt http.ResponseWriter, req *http.Request) {
	h := wrt.Header()

	h.Add("Allow", "POST")
	h.Add("Allow", "GET")
	h.Add("Allow", "OPTIONS")

	wrt.WriteHeader(http.StatusOK)
}
