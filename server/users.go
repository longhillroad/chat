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
 *  File        :  users.go
 *  Author      :  Gene Sokolov
 *  Created     :  18-May-2014
 *
 ******************************************************************************
 *
 *  Description : 
 *
 *   Persistence of data related to users. Hard dependency on RethinkDB.
 *
 *****************************************************************************/
package main

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	rethink "github.com/dancannon/gorethink"
	"github.com/gorilla/mux"
	"github.com/or-else/guid"
	"log"
	"net/http"
	"strings"
	"time"
)

func userLogin(ctx *context) error {

	username := ctx.req.FormValue("username")
	password := ctx.req.FormValue("password")

	userId := authUser(ctx.appid, username, password)
	if userId.IsZero() {
		ctx.code = http.StatusUnauthorized
		return errors.New("Invalid user name or password")
	}

	expires := time.Now().Add(TOKEN_LIFETIME_DEFAULT).UTC().Round(time.Second)
	params := map[string]interface{}{
		"token":   makeSecurityToken(userId, expires),
		"expires": expires,
		"uid":     userId.String()}

	json.NewEncoder(ctx.wrt).Encode(
		&ServerComMessage{Ctrl: &MsgServerCtrl{
			Code:   http.StatusOK,
			Text:   http.StatusText(http.StatusOK),
			Params: params}})

	return nil
}

func userKindParams(ctx *context) error {
	vars := mux.Vars(ctx.req)
	ctx.kind = "users"
	if vars["id"] == "me" {
		ctx.objId = ctx.uid.String()
	} else {
		ctx.objId = vars["id"]
	}

	return nil
}

func messageKindParams(ctx *context) error {
	vars := mux.Vars(ctx.req)
	ctx.kind = "messages"
	ctx.objId = vars["id"]
	return nil
}

// User fetches a list of messages on a topic. Parse user request and prepare a search query.
// In case of simple search the only valid string is a
//	topic=<topic name>
// for example: topic=!me
// no other query terms are allowed. In case of full search sintax
//	_q = { "match": [<topic name here>], "index": "topic", "limit": 123, "offset": 234 }
// limit and offset are optional
func parseGetMessagesQuery(ctx *context) error {
	if err := parseGetAllQuery(ctx); err != nil {
		ctx.code = http.StatusBadRequest
		return err
	}
	if len(ctx.query.Match) != 1 || ctx.query.Index != "topic" {
		ctx.code = http.StatusBadRequest
		return errors.New("Invalid search request")
	}

	topic := ctx.query.Match[0]
	if strings.HasPrefix(topic, "!usr:") {
		topic = makeP2PTopic(ctx.uid, guid.ParseGUID(topic[5:]))
	} else if topic == "!me" {
		topic = makeP2PTopic(ctx.uid, ctx.uid)
	} else if topic == "" || topic[0] == '!' {
		ctx.code = http.StatusBadRequest
		return errors.New("Invalid search request")
	}

	ctx.query.Type = "range"
	ctx.query.Range.From = []interface{}{topic, time.Unix(0, 0)}
	ctx.query.Range.To = []interface{}{topic, time.Now().Add(time.Second * 5)}
	ctx.query.Index = "topic_createdAt"

	ctx.query.OrderBy.UseIndex = true
	ctx.query.OrderBy.IndexDesc = true
	//ctx.query.OrderBy.Terms = []SearchQuerySort{SearchQuerySort{Term: "createdAt", Desc: true}}

	return nil
}

func contactsLoad(appid uint32, uid guid.GUID) ([]guid.GUID, error) {

	q := storage.Db(appid).Table("contacts").GetAllByIndex("parentId", uid.String()).
		Filter(rethink.Row.Field("active")).Pluck("contactId")

	rows, err := q.Limit(128).Run(storage.sess)
	if err != nil {
		return nil, err
	}

	var contacts []guid.GUID
	var contact struct {
		ContactId string `gorethink:"contactId"`
	}
	for rows.Next() {
		err = rows.Scan(&contact)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, guid.ParseGUID(contact.ContactId))
	}

	return contacts, nil
}

/*
func contactsAdd(appid uint32, uid guid.GUID, ids []guid.GUID) error {
	return nil
}

func contactsRemove(appid uint32, uid guid.GUID, ids []guid.GUID) error {
	return nil
}
*/

type StoredMessage struct {
	Topic     string         `gorethink:"topic"`
	CreatedAt time.Time      `gorethink:"createdAt"`
	Data      *MsgServerData `gorethink:"data"`
}

/**
Save message to database
Messages could be person to person or generic. If a message is p2p, a topic record is created or updated
*/
func SaveMessage(appid uint32, p2p []guid.GUID, data *MsgServerData) bool {
	message := StoredMessage{
		Topic:     data.Topic,
		CreatedAt: time.Now(),
		Data:      data}

	if p2p != nil {
		// Record p2p topic update
		message.Topic = makeP2PTopic(p2p[0], p2p[1])
		var owner []string
		if p2p[0].Equal(p2p[1]) {
			// Repeating the owner causes duplicate results in LoadTopics
			owner = []string{p2p[0].String()}
		} else {
			owner = []string{p2p[0].String(), p2p[1].String()}
		}
		topic := map[string]interface{}{"id": message.Topic, "owner": owner, "updatedAt": message.CreatedAt}
		_, err := storage.Db(appid).Table("_topics").Insert(topic, rethink.InsertOpts{Durability: "soft", Upsert: true}).
			RunWrite(storage.sess)
		if err != nil {
			log.Println("storage.SaveMessage in topic update/" + err.Error())
			return false
		}
	}
	// Actually store message
	resp, err := storage.Db(appid).Table("messages").Insert(message).RunWrite(storage.sess)
	if err != nil {
		log.Println("storage.SaveMessage /" + err.Error())
		return false
	}

	if resp.FirstError != "" {
		log.Println("storage.SaveMessage in response/" + resp.FirstError)
		return false
	}

	if len(resp.GeneratedKeys) == 0 {
		log.Println("storage.SaveMessage no inserted keys")
		return false
	}

	return true
}

/*
This is not used

// Load recent messages by topic, no older than time [past]
func LoadMessages(appid uint32, topic string, past time.Time, page, pageSize int) []*MsgServerData {
	data := []*MsgServerData{}

	rows, err := storage.Db(appid).Table("messages").Between(
		[]interface{}{topic, past},
		[]interface{}{topic, time.Now().Add(time.Second * 5)},
		rethink.BetweenOpts{Index: "topic_createdAt"}).
		OrderBy(rethink.Desc("createdAt"), map[string]interface{}{"index": "topic_createdAt"}).
		Skip(page * pageSize).Limit(pageSize).Run(storage.sess)

	if err != nil {
		log.Println("storage.GetMessages/" + err.Error())
		return nil
	}

	for rows.Next() {
		var obj StoredMessage
		if err = rows.Scan(&obj); err != nil {
			log.Println("storage.GetMessages in Scan/" + err.Error())
			return nil
		}

		data = append(data, obj.Data)
	}

	return data
}
*/

// Schema:
//   id: topic as "!p2p:"md5(uid1, uid2) (otherwise [upsert] won't work)
//   array of owners
//   updatedAt
// Fetch a list of topic updated since time [past]
func LoadTopics(appid uint32, uid guid.GUID, past time.Time, pageSize int) []map[string]interface{} {
	var data []map[string]interface{}
	owner := uid.String()

	rows, err := storage.Db(appid).Table("_topics").Between(
		[]interface{}{owner, past},
		[]interface{}{owner, time.Now().Add(time.Second * 5)},
		rethink.BetweenOpts{Index: "owner_updatedAt"}).
		OrderBy(map[string]interface{}{"index": "owner_updatedAt"}).
		Pluck("owner", "updatedAt").
		Limit(pageSize).Run(storage.sess)

	if err != nil {
		log.Println("storage.GetMessages/" + err.Error())
		return nil
	}

	for rows.Next() {
		var obj struct {
			Owner     []string
			UpdatedAt time.Time
		}
		var topic string
		if err = rows.Scan(&obj); err != nil {
			log.Println("storage.GetMessages in Scan/" + err.Error())
			return nil
		}

		// In case of self-messages the Owner contains just self
		if len(obj.Owner) == 2 && obj.Owner[0] == owner {
			topic = "!usr:" + obj.Owner[1]
		} else {
			topic = "!usr:" + obj.Owner[0]
		}
		data = append(data, map[string]interface{}{"topic": topic, "updatedAt": obj.UpdatedAt.UTC().Round(time.Second)})
		log.Println("Loaded topic: ", topic)
	}

	return data
}

func makeP2PTopic(u1 guid.GUID, u2 guid.GUID) string {
	hasher := md5.New()
	if u1.Less(u2) {
		hasher.Write(u1.ToBytes())
		hasher.Write(u2.ToBytes())
	} else {
		hasher.Write(u2.ToBytes())
		hasher.Write(u1.ToBytes())
	}
	return "!p2p:" + base64.URLEncoding.EncodeToString(hasher.Sum(nil))
}

func UpdateLastSeen(appid uint32, uid guid.GUID) bool {
	var upd = map[string]interface{}{"lastSeen": time.Now()}

	if _, err := storage.Db(appid).Table("users").Get(uid.String()).
		Update(upd, rethink.UpdateOpts{Durability: "soft"}).RunWrite(storage.sess); err != nil {
		return false
	}

	return true
}

func UpdateUserStatus(appid uint32, uid guid.GUID, status interface{}) bool {
	update := map[string]interface{}{"status": status}

	_, err := storage.Db(appid).Table("users").Get(uid.String()).
		Update(update, rethink.UpdateOpts{Durability: "soft"}).RunWrite(storage.sess)

	if err != nil {
		log.Println("Failed to update user status: ", err)
	}
	return err == nil
}

func GetLastSeenAndStatus(appid uint32, uid guid.GUID) (time.Time, interface{}) {
	row, err := storage.Db(appid).Table("users").Get(uid.String()).Pluck("lastSeen", "status").RunRow(storage.sess)
	var timeDefault = time.Unix(1388534400, 0).UTC() // Jan 1st 2014
	if err == nil && !row.IsNil() {
		var data struct {
			LastSeen time.Time   `gorethink:"lastSeen"`
			Status   interface{} `gorethink:"status"`
		}
		if err = row.Scan(&data); err == nil {
			if data.LastSeen.IsZero() {
				data.LastSeen = timeDefault
			}
			return data.LastSeen, data.Status
		}
	}

	return timeDefault, nil
}
