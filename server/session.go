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
 *  File        :  session.go
 *  Author      :  Gene Sokolov
 *  Created     :  18-May-2014
 *
 ******************************************************************************
 *
 *  Description : 
 *
 *  Handling of user sessions/connections. One user may have multiple sesions. 
 *  Each session may handle multiple topics 
 *
 *****************************************************************************/

package main

import (
	"container/list"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"github.com/or-else/guid"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	NONE = iota
	WEBSOCK
	LPOLL
)

/*
  A single WS connection or a long poll session. A user may have multiple
  connections (control connection, multiple simultaneous group chat)
*/
type Session struct {
	// protocol - NONE (REST) or streaming WEBSOCK, LPOLL
	proto int

	// Set only for websockets
	ws *websocket.Conn

	// Set only for Long Poll sessions
	wrt http.ResponseWriter

	// Application ID
	appid uint32

	// Username who created the session (if any)
	//owner string

	// ID of the current user or guid.Zero
	uid guid.GUID

	// Session ID
	sid string

	// Time when the session was last touched
	lastTouched time.Time

	// outbound mesages, buffered
	send chan []byte

	// Map of topic subscriptions, indexed by topic name
	subs map[string]*Subscription

	//Needed for long polling
	rw sync.RWMutex
}

// Mapper of sessions to topics
type Subscription struct {
	// Channel to communicate with the topic, copy of Topic.incoming
	read chan<- *ServerComMessage

	// Session sends a signal to Topic when this session is unsubscribed
	// This is a copy of Topic.unreg
	done chan<- *Session
}

type sessionStoreElement struct {
	key string
	val *Session
}

type SessionStore struct {
	rw       sync.RWMutex
	sessions map[string]*list.Element
	lru      *list.List
	lifeTime time.Duration
}

func (ss *SessionStore) Create(conn interface{}, appid uint32) *Session {
	var s Session

	switch conn.(type) {
	case *websocket.Conn:
		s.proto = WEBSOCK
		s.ws, _ = conn.(*websocket.Conn)
	case http.ResponseWriter:
		s.proto = LPOLL
		s.wrt, _ = conn.(http.ResponseWriter)
	default:
		s.proto = NONE
	}

	if s.proto != NONE {
		s.subs = make(map[string]*Subscription)
		s.send = make(chan []byte, 16)
	}

	s.appid = appid
	s.lastTouched = time.Now()
	s.sid = getRandomString()

	if s.proto != WEBSOCK {
		// Websocket connections are not managed by SessionStore
		ss.rw.Lock()

		elem := ss.lru.PushFront(&sessionStoreElement{s.sid, &s})
		ss.sessions[s.sid] = elem

		// Remove expired sessions
		expire := s.lastTouched.Add(-ss.lifeTime)
		for elem = ss.lru.Back(); elem != nil; elem = ss.lru.Back() {
			if elem.Value.(*sessionStoreElement).val.lastTouched.Before(expire) {
				ss.lru.Remove(elem)
				delete(ss.sessions, elem.Value.(*sessionStoreElement).key)
			} else {
				break // don't need to traverse further
			}
		}
		ss.rw.Unlock()
	}

	return &s
}

func (ss *SessionStore) Get(sid string) *Session {
	ss.rw.Lock()
	defer ss.rw.Unlock()

	if elem := ss.sessions[sid]; elem != nil {
		ss.lru.MoveToFront(elem)
		elem.Value.(*sessionStoreElement).val.lastTouched = time.Now()
		return elem.Value.(*sessionStoreElement).val
	}

	return nil
}

func (ss *SessionStore) Delete(sid string) *Session {
	ss.rw.Lock()
	defer ss.rw.Unlock()

	if elem := ss.sessions[sid]; elem != nil {
		ss.lru.Remove(elem)
		delete(ss.sessions, sid)

		return elem.Value.(*sessionStoreElement).val
	}

	return nil
}

func NewSessionStore(lifetime time.Duration) *SessionStore {
	store := &SessionStore{
		sessions: make(map[string]*list.Element),
		lru:      list.New(),
		lifeTime: lifetime,
	}

	return store
}

func (s *Session) close() {
	if s.proto == WEBSOCK {
		s.ws.Close()
	}
}

func (s *Session) writePkt(pkt *ServerComMessage) error {
	data, _ := json.Marshal(pkt)
	switch s.proto {
	case WEBSOCK:
		return ws_write(s.ws, websocket.TextMessage, data)
	case LPOLL:
		_, err := s.wrt.Write(data)
		return err
	default:
		return errors.New("invalid session")
	}
}

func (s *Session) subscribe(msg *ClientComMessage) *ServerComMessage {
	// Request to subscribe to a topic
	log.Printf("Sub to '%s' from '%s'", msg.Sub.Topic, msg.from)

	if msg.Sub.Topic == "" {
		return ErrMalformed(msg.Sub.Id, "")
	}

	original := msg.Sub.Topic
	switch msg.Sub.Topic {
	case "!new":
		// Request to create a new named topic
		msg.Sub.Topic = getRandomString()
		original = msg.Sub.Topic
	case "!me":
		// Make self available to receive messages and announce presence
		if s.uid.IsZero() {
			log.Printf("sess.subscribe: must authenticate first")
			return ErrAuthRequired(msg.Sub.Id, original)
		}
		msg.Sub.Topic = "!usr:" + s.uid.String()
	case "!pres":
		if s.uid.IsZero() {
			return ErrAuthRequired(msg.Sub.Id, original)
		}
		msg.Sub.Topic = "!pres:" + s.uid.String()
	default:
		// Subscribe to topic by explicitly provoided name
		msg.Sub.Topic = strings.TrimSpace(msg.Sub.Topic)
		if strings.HasPrefix(msg.Sub.Topic, "!") {
			return ErrSubscribeFailed(msg.Sub.Id, original)
		}
	}

	if _, ok := s.subs[msg.Sub.Topic]; ok {
		log.Printf("sess.subscribe: already subscribed to '%s'", msg.Sub.Topic)
		return ErrAlreadySubscribed(msg.Sub.Id, original)
	}

	log.Printf("sess.subscribe: asking hub to subscribe to '%s'", msg.Sub.Topic)
	globalhub.reg <- sessionReg{topic: msg.Sub.Topic, params: msg.Sub.Params, msgId: msg.Sub.Id,
		original: original, sess: s}
	// Hub will send Ctrl success/failure packets back to session

	log.Printf("Sub to '%s' (%s) from '%s' -- OK!", msg.Sub.Topic, original, msg.from)

	return nil
}

func (s *Session) unsubscribe(msg *ClientComMessage) *ServerComMessage {
	var reply *ServerComMessage

	if msg.Unsub.Topic == "" {
		return ErrMalformed(msg.Unsub.Id, "")
	}

	topic := msg.Unsub.Topic
	if msg.Unsub.Topic == "!me" {
		topic = "!usr:" + s.uid.String()
	} else if msg.Unsub.Topic == "!pres" {
		topic = "!pres:" + s.uid.String()
	}

	if sub, ok := s.subs[topic]; ok {
		sub.done <- s
		reply = NoErr(msg.Unsub.Id, msg.Unsub.Topic)
	} else {
		reply = ErrNotSubscribed(msg.Unsub.Id, msg.Unsub.Topic)
	}

	return reply
}

// Message from this session
func (s *Session) publish(msg *ClientComMessage) *ServerComMessage {
	// Broadcast the message to all topic subscribers

	// TODO(gene): add sanity/permission checking, i.e. can user send "pub"
	// from a group chat?"

	var data *MsgServerData
	var p2p []guid.GUID
	var self bool

	original := msg.Pub.Topic
	userFromTopic := "!usr:" + msg.from

	// Validate topic name
	if msg.Pub.Topic == "" {
		return ErrMalformed(msg.Pub.Id, "")
	} else if msg.Pub.Topic == "!me" {
		self = true

		if !s.uid.IsZero() {
			// Receiver should see message on sender's topic
			msg.Pub.Topic = userFromTopic
			p2p = []guid.GUID{s.uid, s.uid}
		} else {
			// !me is invalid without authentication
			return ErrAuthRequired(msg.Pub.Id, original)
		}
	} else if strings.HasPrefix(msg.Pub.Topic, "!usr:") {
		uid2 := guid.ParseGUID(msg.Pub.Topic[5:]) // len("!usr:") == 5
		if uid2.IsZero() {
			return ErrMalformed(msg.Pub.Id, original)
		}
		p2p = []guid.GUID{s.uid, uid2}
	} else if msg.Pub.Topic == "!pres" {
		// TODO(gene): expand !pres
	}

	if sub, ok := s.subs[msg.Pub.Topic]; ok {
		// This is a post to a subscribed topic. The message is sent to the topic only
		data = &MsgServerData{
			Id:      msg.Pub.Id,
			Topic:   original, // show just "!me" or "!pres" instead of expanded topic name
			Origin:  msg.from,
			Content: msg.Pub.Content}
		if self && data.Content == nil {
			data.Content = msg.Pub.Params
		}
		sub.read <- &ServerComMessage{Data: data}
		// TODO(genes): Send AKN
	} else if p2p != nil {
		// This is a message to a user, possbly self (addressed as !usr:...) while not being subscribed to !me
		// The receiving user (rcptto) should see communication on the originator's !usr: topic, the sender on
		// receiver's (this way the whole p2p conversation can be aggregated by topic by both parties, the
		// send/receive are on the same topic)
		// Global hub sends a Ctrl.201 response on receiver's topic
		data = &MsgServerData{
			Id:      msg.Pub.Id,
			Topic:   userFromTopic,
			Origin:  msg.from,
			Content: msg.Pub.Content}
		if self && data.Content == nil {
			data.Content = msg.Pub.Params
		}
		globalhub.route <- &ServerComMessage{Data: data, appid: s.appid, rcptto: msg.Pub.Topic, akn: s.send}
	} else {
		//TODO(gene): handle other cases, such as "!pres"
	}

	// Check if user requested an echo packet
	if msg.Pub.GetBoolParam("echo") {
		// Send echo only if user is subscribed to !me. Echo should be on the same topic, as non-echo messages
		if self, ok := s.subs[userFromTopic]; ok {
			self.read <- &ServerComMessage{Data: &MsgServerData{
				Id:      msg.Pub.Id,
				Topic:   msg.Pub.Topic,
				Origin:  msg.from,
				Content: msg.Pub.Content}}
		}
	}

	// Persistence
	if data != nil {
		SaveMessage(s.appid, p2p, data)

		// Check for status update
		if self {
			if status := msg.Pub.Params["status"]; status != nil {
				UpdateUserStatus(s.appid, s.uid, status)
				globalhub.presence <- &PresenceChange{AppId: s.appid, Id: s.uid, Action: ActionStatus, Status: status}
			}
		}
	}

	return nil
}

func (s *Session) login(msg *ClientComMessage) *ServerComMessage {
	var res *ServerComMessage
	var uid guid.GUID
	var expires time.Time

	if !s.uid.IsZero() {
		res = ErrAlreadyAuthenticated(msg.Login.Id, "")
	} else if msg.Login.Scheme == "" || msg.Login.Scheme == "basic" {
		if splitAt := strings.Index(msg.Login.Secret, ":"); splitAt > 0 {
			if uid = authUser(s.appid, msg.Login.Secret[:splitAt], msg.Login.Secret[splitAt+1:]); uid.IsZero() {
				res = ErrAuthFailed(msg.Login.Id, "")
			}
		} else {
			res = ErrAuthFailed(msg.Login.Id, "")
		}
	} else if msg.Login.Scheme == "token" {
		if uid, expires = checkSecurityToken(msg.Login.Secret); uid.IsZero() {
			res = ErrAuthFailed(msg.Login.Id, "")
		}
	} else {
		res = ErrAuthUnknownScheme(msg.Login.Id, "")
	}

	if res == nil {
		s.uid = uid
		expireIn := time.Duration(msg.Login.ExpireIn)
		if expireIn <= 0 || expireIn > TOKEN_LIFETIME_MAX {
			expireIn = TOKEN_LIFETIME_DEFAULT
		}

		newExpires := time.Now().Add(expireIn).UTC().Round(time.Second)
		if !expires.IsZero() && expires.Before(newExpires) {
			newExpires = expires
		}
		params := map[string]interface{}{
			"uid":     uid.String(),
			"expires": newExpires,
			"token":   makeSecurityToken(uid, expires)}

		res = &ServerComMessage{Ctrl: &MsgServerCtrl{
			Id:     msg.Login.Id,
			Code:   http.StatusOK,
			Text:   http.StatusText(http.StatusOK),
			Params: params}}
	}
	return res
}

func (s *Session) dispatch(raw []byte) *ServerComMessage {
	var msg ClientComMessage
	var res *ServerComMessage

	log.Printf("Session.dispatch got '%s'", raw)

	if err := json.Unmarshal(raw, &msg); err != nil {
		// Malformed message
		log.Println("Session.dispatch: " + err.Error())
		return ErrMalformed("", "")
	}

	msg.from = s.uid.String()

	// Locking-unlocking is needed for long polling.
	// Should not affect performance
	s.rw.Lock()
	defer s.rw.Unlock()

	switch {
	case msg.Pub != nil:
		res = s.publish(&msg)
		log.Println("Pub: dispatch done")

	case msg.Sub != nil:
		res = s.subscribe(&msg)
		log.Println("Sub: dispatch done")

	case msg.Unsub != nil:
		res = s.unsubscribe(&msg)
		log.Println("Unsub: dispatch done")

	case msg.Login != nil:
		res = s.login(&msg)
		log.Println("Login: dispatch done")

	default:
		// Unknown message
		log.Println("Session.dispatch: unknown message")
		return ErrUnrecognized("", "")
	}

	return res
}

func (s *Session) getUser() guid.GUID {
	if s == nil {
		return guid.Zero()
	} else {
		return s.uid
	}
}
