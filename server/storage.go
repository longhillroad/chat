/*****************************************************************************
 *
 * Copyright 2014, Tinode, All Rights Reserved
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 *  File        :  storage.go
 *  Author      :  Gene Sokolov
 *  Created     :  18-May-2014
 *
 *****************************************************************************
 *
 *  Description : 
 *
 *  An attempt to abstract away a persistence layer. This code handles RethinkDB.
 *
 *****************************************************************************/

package main

import (
	rethink "github.com/dancannon/gorethink"
	//	"github.com/or-else/mergemap"
	"encoding/base64"
	"fmt"
	"github.com/or-else/guid"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CRUD storage.
type Storage struct {
	sess *rethink.Session
}

const (
	CrudCreate = 1 << iota
	CrudRead
	CrudUpdate
	CrudDelete
)

const (
	CrudAnonym = iota
	CrudUser
	CrudOwner
	CrudRoot
)

func crudAccessMask(owner, user, anonym int) int {
	return owner<<(CrudOwner*8) | user<<(CrudUser*8) | anonym<<(CrudAnonym*8)
}

func crudCheckMask(mask, access, who int) bool {
	switch who {
	case CrudRoot:
		return true
	case CrudOwner:
		return (mask & (access << (CrudOwner * 8))) != 0
	case CrudUser:
		return (mask & (access << (CrudUser * 8))) != 0
	case CrudAnonym:
		return (mask & (access << (CrudAnonym * 8))) != 0
	default:
		return false
	}
}

type crudFieldList struct {
	readOnly []string
	hidden   []string
	required []string
	unique   []string
}

type crudKindSchema struct {
	level      int    // base object or object with a parent (level=1)
	parent     string // name of the parent kind for level=1 objects
	accessOne  int
	accessMany int
	pageSize   uint
	preproc    map[int]func(stor Storage, appid uint32, obj map[string]interface{}) int
	postproc   map[int]func(stor Storage, appid uint32, obj map[string]interface{}) int
	fields     crudFieldList
}

var crudSchema map[string]*crudKindSchema

// Default schemas for level=0 and level=1 kinds
var defaultSchema = map[int]*crudKindSchema{
	0: &crudKindSchema{
		accessOne: crudAccessMask(CrudRead|CrudUpdate|CrudDelete, CrudRead, CrudRead),
		// There is no owner when creating an object
		accessMany: crudAccessMask(0, CrudCreate|CrudRead, CrudRead),
		pageSize:   64,
	},
	1: &crudKindSchema{
		accessOne: crudAccessMask(CrudRead|CrudUpdate|CrudDelete, CrudRead, CrudRead),
		// There is no owner when creating an object
		accessMany: crudAccessMask(CrudCreate|CrudRead, CrudCreate|CrudRead, CrudRead),
		pageSize:   64,
	},
}

// Returns an initialized Storage
func NewStorage(source string) *Storage {
	r, err := rethink.Connect(map[string]interface{}{
		"address":   "localhost:28015",
		"database":  "tinode",
		"maxIdle":   2,
		"maxActive": 10})

	if err != nil {
		log.Fatal("Failed to connect to RethinkDB: ", err)
	}

	crudSchema = make(map[string]*crudKindSchema)
	// This is a schema for User object
	crudSchema["users"] = &crudKindSchema{
		accessOne: crudAccessMask(CrudRead|CrudUpdate, CrudRead, 0),
		// There is no owner when creating a user, anonymous should be able to POST to users
		accessMany: crudAccessMask(0, CrudRead, CrudCreate),
		pageSize:   64,
		preproc: map[int]func(stor Storage, appid uint32, obj map[string]interface{}) int{
			CrudCreate: func(stor Storage, appid uint32, obj map[string]interface{}) int {
				obj["passhash"] = base64.URLEncoding.EncodeToString(passHash(obj["password"].(string)))
				delete(obj, "password")
				return 0
			}},
		postproc: map[int]func(stor Storage, appid uint32, obj map[string]interface{}) int{
			CrudCreate: func(stor Storage, appid uint32, obj map[string]interface{}) int {
				owner := map[string]interface{}{"owner": obj["id"]}
				if _, err := storage.Db(appid).Table("users").Get(obj["id"]).Update(owner).RunWrite(stor.sess); err != nil {
					return http.StatusInternalServerError
				}
				return 0
			}},
		fields: crudFieldList{
			readOnly: []string{"username", "passhash"},
			hidden:   []string{"passhash"},
			required: []string{"username", "password"},
			unique:   []string{"username"}},
	}

	crudSchema["contacts"] = &crudKindSchema{
		level:      1,
		parent:     "users",
		accessOne:  crudAccessMask(CrudRead|CrudUpdate|CrudDelete, 0, 0),
		accessMany: crudAccessMask(CrudCreate|CrudRead, 0, 0),
		pageSize:   128,
	}

	crudSchema["messages"] = &crudKindSchema{
		accessOne:  crudAccessMask(CrudRead|CrudDelete, CrudRead|CrudDelete, 0),
		accessMany: crudAccessMask(CrudCreate|CrudRead, CrudRead|CrudDelete, 0),
		pageSize:   64,
		fields: crudFieldList{
			hidden: []string{"topic"},
		},
	}

	// Root-only access
	crudSchema["topics"] = &crudKindSchema{
		accessOne:  crudAccessMask(0, 0, 0),
		accessMany: crudAccessMask(0, 0, 0),
		pageSize:   64,
	}

	return &Storage{sess: r}
}

// Gracefuly close storage
func (stor Storage) Close() {
	stor.sess.Close()
}

func (stor Storage) Db(appid uint32) rethink.RqlTerm {
	return rethink.Db("app_" + strconv.Itoa(int(appid)))
}

// Rethink does not support unique fields.
// Enforce unique field on object creation
func uniqueCreateObj(appid uint32, kind string, toCheck []string,
	obj map[string]interface{}) (bool, string) {
	var keys []interface{}
	for _, key := range toCheck {
		log.Println("Checking field ", key)
		val := obj[key]
		var strVal string
		switch val.(type) {
		case fmt.Stringer:
			strVal = val.(fmt.Stringer).String()
		case string:
			strVal = val.(string)
		default:
			log.Println("Failed to stringify")
			return false, key
		}

		id := kind + "!" + key + "!" + strVal // unique value=primary key
		name := kind + "!" + key              // name of the unique index
		resp, err := storage.Db(appid).Table("_uniques").Insert(
			map[string]string{"id": id, "name": name}, rethink.InsertOpts{Durability: "soft"}).RunWrite(storage.sess)
		if err != nil || resp.Inserted == 0 {
			// Delete inserted entries in case of errors
			if len(keys) > 0 {
				storage.Db(appid).Table("_uniques").GetAll(keys...).
					Delete(rethink.DeleteOpts{Durability: "soft"}).RunWrite(storage.sess)
			}
			return false, id
		}
		keys = append(keys, id)
	}
	return true, ""
}

// Delete/Create unique index records on object update
func uniqueUpdateObj(appid uint32, kind string, toCheck []string,
	oldobj map[string]interface{}, newobj map[string]interface{}) (bool, string) {
	if ok, key := uniqueCreateObj(appid, kind, toCheck, newobj); !ok {
		return false, key
	}
	uniqueDeleteObj(appid, kind, toCheck, oldobj)

	return true, ""
}

// Delete unique index record on object deletion
func uniqueDeleteObj(appid uint32, kind string, toCheck []string,
	obj map[string]interface{}) bool {
	var keys []interface{}
	for _, key := range toCheck {
		val := obj[key]
		if str, ok := val.(fmt.Stringer); ok {
			keys = append(keys, kind+"!"+key+"!"+str.String())
		}
	}
	if len(keys) > 0 {
		storage.Db(appid).Table("_uniques").GetAll(keys...).
			Delete(rethink.DeleteOpts{Durability: "soft"}).RunWrite(storage.sess)
	}
	return true
}

// Drop unique index
func uniqueDropIndex(appid uint32, kind string, index string) bool {
	_, err := storage.Db(appid).Table("_uniques").GetAll("name", kind+"!"+index).
		Delete(rethink.DeleteOpts{Durability: "soft"}).RunWrite(storage.sess)
	if err != nil {
		log.Println("Failed to drop index: ", err)
	}
	return err == nil
}

// Default Create pre-processing: check access mask, ensure required fields are present
func defaultPreCreate(schema *crudKindSchema, ctx *context, obj map[string]interface{},
	parentObj map[string]interface{}) int {

	if schema == nil {
		schema = defaultSchema[ctx.level]
	} else if ctx.level == 1 && parentObj == nil {
		return http.StatusNotFound
	}

	// Zero-level kinds have no owner, level-1 have owner
	who := CrudAnonym
	if !ctx.uid.IsZero() {
		who = CrudUser
		if ctx.isRoot {
			who = CrudRoot
		} else if ctx.level == 1 {
			owner := guid.ParseGUID(parentObj["owner"].(string))
			if ctx.uid.Equal(owner) {
				who = CrudOwner
			}
		}
	}

	if ok := crudCheckMask(schema.accessMany, CrudCreate, who); !ok {
		log.Println("storage.Create access forbidden by schema")
		return http.StatusForbidden
	}

	for _, req := range schema.fields.required {
		var val interface{}
		var ok bool
		if val, ok = obj[req]; !ok {
			log.Println("storage.Create required field is missing: ", req)
			return http.StatusBadRequest
		}
		// Check if field is present but an empty string
		var strval string
		if strval, ok = val.(string); ok {
			if strings.TrimSpace(strval) == "" {
				log.Println("storage.Create required field is empty: ", req)
				return http.StatusBadRequest
			}
		}
	}

	if len(schema.fields.unique) > 0 {
		if ok, field := uniqueCreateObj(ctx.appid, ctx.kind, schema.fields.unique, obj); !ok {
			log.Printf("storage.Create field '%s' is not unique", field)
			return http.StatusConflict
		}
	}

	return 0
}

// Default Get pre-processing, subkind only
func defaultPreGet(schema *crudKindSchema, ctx *context) int {
	if ctx.level == 1 && schema != nil {
		if schema.parent != "" && schema.parent != ctx.parentKind {
			log.Println("get called for subkind but parent is wrong")
			return http.StatusNotFound
		}
	}
	return 0
}

// Default Get post-processing: check access, remove hidden fields
func defaultPostGet(schema *crudKindSchema, ctx *context, obj map[string]interface{}) int {

	who := CrudAnonym
	if !ctx.uid.IsZero() {
		if ctx.isRoot {
			who = CrudRoot
		} else {
			owner := guid.ParseGUID(obj["owner"].(string))
			if ctx.uid.Equal(owner) {
				who = CrudOwner
			} else {
				who = CrudUser
			}
		}
	}

	if ok := crudCheckMask(schema.accessOne, CrudRead, who); !ok {
		log.Println("storage.Get access forbidden by schema")
		return http.StatusForbidden
	}

	return 0
}

// Default Update pre-processing: check access mask, ensure required fields are not empty strings,
// remove read-only fields
func defaultPreUpdate(schema *crudKindSchema, ctx *context,
	newobj map[string]interface{}, oldobj map[string]interface{}) int {

	if schema == nil {
		schema = defaultSchema[ctx.level]
	} else if ctx.level == 1 {
		if schema.parent != "" && schema.parent != ctx.parentKind {
			return http.StatusNotFound
		}
	}

	who := CrudAnonym
	if !ctx.uid.IsZero() {
		owner := guid.ParseGUID(oldobj["owner"].(string))
		if ctx.uid.Equal(owner) {
			who = CrudOwner
		} else if ctx.isRoot {
			who = CrudRoot
		} else {
			who = CrudUser
		}
	}

	if ok := crudCheckMask(schema.accessOne, CrudUpdate, who); !ok {
		log.Println("storage.Update access forbidden by schema")
		return http.StatusForbidden
	}

	// Ensure required fields are not cleared in Update
	for _, req := range schema.fields.required {
		var val interface{}
		var strval string
		var ok bool
		if val, ok = newobj[req]; ok {
			// TODO(gene): handle float64, bool, null
			if strval, ok = val.(string); ok {
				if strings.TrimSpace(strval) == "" {
					log.Println("storage.Update required field is empty: ", req)
					return http.StatusBadRequest
				}
			}
		}
	}

	// Remove read-only fields
	for _, ro := range schema.fields.readOnly {
		delete(newobj, ro)
	}

	// Check unique fields for changes
	// First, see if there are any changes in the unique fields
	var uniqueKeys []string
	for _, unique := range schema.fields.unique {
		var uniqueKeys []string
		if val, ok := newobj[unique]; ok { // unique field is present
			if val != oldobj[unique] {
				uniqueKeys = append(uniqueKeys, unique)
			}
		}
	}
	// Validate changed unique fields: insert new values into index, then delete old values
	if len(uniqueKeys) > 0 {
		if ok, field := uniqueUpdateObj(ctx.appid, ctx.kind, uniqueKeys, oldobj, newobj); !ok {
			log.Println("storage.Update field %s is not unique: ", field)
			return http.StatusConflict
		}
	}

	return 0
}

// Default Delete pre-processing: check access mask
func defaultPreDelete(schema *crudKindSchema, ctx *context, obj map[string]interface{}) int {

	if schema == nil {
		schema = defaultSchema[ctx.level]
	} else if ctx.level == 1 {
		if schema.parent != "" && schema.parent != ctx.parentKind {
			return http.StatusNotFound
		}
	}

	who := CrudAnonym
	if !ctx.uid.IsZero() {
		owner := guid.ParseGUID(obj["owner"].(string))
		if ctx.uid.Equal(owner) {
			who = CrudOwner
		} else if ctx.isRoot {
			who = CrudRoot
		} else {
			who = CrudUser
		}
	}

	if ok := crudCheckMask(schema.accessOne, CrudDelete, who); !ok {
		log.Println("storage.Delete access forbidden by schema")
		return http.StatusForbidden
	}

	return 0
}

// Default Delete post-processing: delete unique records
func defaultPostDelete(schema *crudKindSchema, ctx *context, obj map[string]interface{}) int {
	if len(schema.fields.unique) > 0 {
		uniqueDeleteObj(ctx.appid, ctx.kind, schema.fields.unique, obj)
	}
	return 0
}

// Session could be nil
func (stor Storage) Create(ctx *context, obj map[string]interface{}) (id string, code int) {
	log.Printf("Create %s", ctx.kind)

	schema := crudSchema[ctx.kind]
	var parentObj map[string]interface{}
	if ctx.level == 1 {
		// TODO(gene): check for existence and ownership of the parent object
		parentObj, code = stor.GetParent(ctx)
		if code != http.StatusOK {
			return
		}
	}

	// Default preprocessor
	if code = defaultPreCreate(schema, ctx, obj, parentObj); code >= http.StatusBadRequest {
		return
	}

	if schema == nil {
		schema = defaultSchema[ctx.level]
	}

	// Kind-specific preprocessor
	if preproc, ok := schema.preproc[CrudCreate]; ok {
		if code = preproc(stor, ctx.appid, obj); code >= http.StatusBadRequest {
			return
		}
	}

	delete(obj, "id")
	if ctx.uid.IsZero() {
		obj["owner"] = nil
	} else {
		obj["owner"] = ctx.uid.String()
	}
	obj["createdAt"] = time.Now()
	delete(obj, "updatedAt")

	// TODO(gene): validate object against schema
	// TODO(gene): check for kind existence

	// Insert the new item into the database
	resp, err := storage.Db(ctx.appid).Table(ctx.kind).Insert(obj).RunWrite(stor.sess)
	if err != nil {
		code = http.StatusInternalServerError
		log.Println("storage.Create in Insert/" + err.Error())
		return
	}

	if resp.FirstError != "" {
		code = http.StatusInternalServerError
		log.Println("storage.Create in response/" + resp.FirstError)
		return
	}

	if len(resp.GeneratedKeys) == 0 {
		code = http.StatusInternalServerError
		log.Println("storage.Create no inserted keys")
		return
	}

	id = resp.GeneratedKeys[0]

	if postproc, ok := schema.postproc[CrudCreate]; ok {
		obj["id"] = id
		if code = postproc(stor, ctx.appid, obj); code >= http.StatusBadRequest {
			return
		}
	}

	code = http.StatusCreated

	return
}

func (stor Storage) Get(ctx *context) (data map[string]interface{}, code int) {
	log.Printf("Get %s.%s", ctx.kind, ctx.objId)

	schema := crudSchema[ctx.kind]

	if code = defaultPreGet(schema, ctx); code >= http.StatusBadRequest {
		return
	}

	if schema == nil {
		schema = defaultSchema[ctx.level]
	}

	q := storage.Db(ctx.appid).Table(ctx.kind).Get(ctx.objId)
	if len(schema.fields.hidden) > 0 {
		// Remove hidden fields from result
		q = q.Without(schema.fields.hidden)
	}

	row, err := q.RunRow(stor.sess)
	if err != nil {
		code = http.StatusInternalServerError
		log.Println("storage.Get in load/" + err.Error())
		return
	}

	if row.IsNil() {
		code = http.StatusNotFound
		return
	}

	data = make(map[string]interface{})
	if err = row.Scan(&data); err != nil {
		code = http.StatusInternalServerError
		data = nil
		log.Println("storage.Get in Scan/" + err.Error())
		return
	}

	if code = defaultPostGet(schema, ctx, data); code >= http.StatusBadRequest {
		return
	}

	code = http.StatusOK

	return
}

func (stor Storage) GetParent(ctx *context) (data map[string]interface{}, code int) {
	ctx2 := *ctx
	ctx2.kind = ctx2.parentKind
	ctx2.objId = ctx2.parentId
	ctx2.level = 0

	return stor.Get(&ctx2)
}

func (stor Storage) GetAll(ctx *context) (data []map[string]interface{}, code int) {
	log.Println("GetAll ", ctx.parentKind, ".", ctx.parentId, ".", ctx.kind)

	schema := crudSchema[ctx.kind]

	if code = defaultPreGet(schema, ctx); code >= http.StatusBadRequest {
		return
	}

	if schema == nil {
		schema = defaultSchema[ctx.level]
	}

	// Check if for anomynous user access
	if !ctx.isRoot && ctx.uid.IsZero() {
		if ok := crudCheckMask(schema.accessMany, CrudRead, CrudAnonym); !ok {
			log.Println("storage.GetAll access forbidden by schema")
			code = http.StatusForbidden
			return
		}
	}

	q := storage.Db(ctx.appid).Table(ctx.kind)

	// User provided an explicit search query
	if ctx.query.Type == "match" {
		var iface []interface{}
		for _, str := range ctx.query.Match {
			iface = append(iface, str)
		}
		q = q.GetAllByIndex(ctx.query.Index, iface...)
	} else if ctx.query.Type == "range" {
		q = q.Between(ctx.query.Range.From, ctx.query.Range.To, rethink.BetweenOpts{Index: ctx.query.Index})
	} else if ctx.level == 1 {
		// Or use an implied restriction by parentId
		q = q.GetAllByIndex("parentId", ctx.parentId)
	}

	// Add OrderBy
	if len(ctx.query.OrderBy.Terms) > 0 || ctx.query.OrderBy.UseIndex {
		var orderBy []interface{}
		if ctx.query.OrderBy.UseIndex {
			if ctx.query.OrderBy.IndexDesc {
				orderBy = append(orderBy, map[string]interface{}{"index": rethink.Desc(ctx.query.Index)})
			} else {
				orderBy = append(orderBy, map[string]interface{}{"index": ctx.query.Index})
			}
		} else {
			for _, term := range ctx.query.OrderBy.Terms {
				if term.Desc {
					orderBy = append(orderBy, rethink.Desc(term.Term))
				} else {
					orderBy = append(orderBy, term.Term)
				}
			}
		}
		q = q.OrderBy(orderBy...)
	}

	// Only owner can access objects, filter out non-own stuff
	if !ctx.isRoot {
		if ok := crudCheckMask(schema.accessMany, CrudRead, CrudUser); !ok {
			// TODO(gene): create and use index on "owner"
			q = q.Filter(rethink.Row.Field("owner").Eq(ctx.uid.String()))
		}
	}

	// Remove hidden fields from result
	if len(schema.fields.hidden) > 0 {
		q = q.Without(schema.fields.hidden)
	}

	// Number of rows to load: choose smaller: searchQuery.pageSize or schema.PageSize
	pageSize := schema.pageSize
	if ctx.query.Limit > 0 && ctx.query.Limit < schema.pageSize {
		pageSize = ctx.query.Limit
	}
	rows, err := q.Limit(pageSize).Run(stor.sess)
	if err != nil {
		code = http.StatusInternalServerError
		log.Println("storage.GetAll in load/" + err.Error())
		return
	}

	for rows.Next() {
		var obj map[string]interface{}
		if err = rows.Scan(&obj); err != nil {
			code = http.StatusInternalServerError
			data = nil
			log.Println("storage.GetAll in Scan/" + err.Error())
			return
		}

		data = append(data, obj)
	}

	// No data, initialize to empty array
	if data == nil {
		data = []map[string]interface{}{}
	}

	code = http.StatusOK

	return
}

func (stor Storage) Update(ctx *context, obj map[string]interface{}) int {
	log.Printf("Update %s.%s", ctx.kind, ctx.objId)

	data, code := stor.Get(ctx)
	if code != http.StatusOK {
		return code
	}

	schema := crudSchema[ctx.kind]
	if code = defaultPreUpdate(schema, ctx, obj, data); code >= http.StatusBadRequest {
		return code
	}

	// Remove default read-only fields, update updatedAt
	delete(obj, "id")
	delete(obj, "createdAt")
	delete(obj, "owner")
	obj["updatedAt"] = time.Now().UTC().Round(time.Second)

	if _, err := storage.Db(ctx.appid).Table(ctx.kind).Get(ctx.objId).Update(obj).RunWrite(stor.sess); err != nil {
		return http.StatusInternalServerError
	}

	return http.StatusOK
}

func (stor Storage) Delete(ctx *context) int {
	log.Printf("Delete %s.%s", ctx.kind, ctx.objId)

	data, code := stor.Get(ctx)
	if code != http.StatusOK {
		return code
	}

	schema := crudSchema[ctx.kind]
	if code = defaultPreDelete(schema, ctx, data); code >= http.StatusBadRequest {
		return code
	}

	if _, err := storage.Db(ctx.appid).Table(ctx.kind).Get(ctx.objId).Delete().RunWrite(stor.sess); err != nil {
		return http.StatusInternalServerError
	}

	return http.StatusOK
}
