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
 *  File        :  hub.go
 *  Author      :  Gene Sokolov
 *  Created     :  18-May-2014
 *
 ******************************************************************************
 *
 *  Description : 
 *
 *    Create/tear down conversation topics, route messages between topics.
 *
 *****************************************************************************/
package main

import (
	"encoding/json"
	"expvar"
	"log"
	"time"
)

// Subscribe session to topic
type sessionReg struct {
	// Name of the topic to subscribe to
	topic string
	// Parameters of the Subscribe request
	params map[string]interface{}
	// Message ID for reporting errors back to origin
	msgId string
	// Original name of the topic for reporting back to user (!me or !pres only)
	original string
	// Session to subscribe
	sess *Session
}

type topicUnreg struct {
	appid uint32
	// Name of the topic to drop
	topic string
}

type Hub struct {

	// Topics must be indexed by appid!name
	// FIXME(gene): use appid
	topics map[string]*Topic

	// Channel for routing messages between topics
	route chan *ServerComMessage

	// subscribe session to topic, possibly creating a new topic
	reg chan sessionReg

	// Remove topic
	unreg chan topicUnreg

	// Exported counter of live topics
	topicsLive *expvar.Int

	// Channel for reporting presence changes
	presence chan<- *PresenceChange
}

func (h *Hub) topicKey(appid uint32, name string) string {
	return string(appid>>16) + string(appid&0xFFFF) + "!" + name
}

func (h *Hub) topicGet(appid uint32, name string) *Topic {
	return h.topics[h.topicKey(appid, name)]
}

func (h *Hub) topicPut(appid uint32, name string, t *Topic) {
	h.topics[h.topicKey(appid, name)] = t
}

func (h *Hub) topicDel(appid uint32, name string) {
	delete(h.topics, h.topicKey(appid, name))
}

func newHub() *Hub {
	var h = &Hub{
		topics: make(map[string]*Topic),
		// unbuffered - throttle topic
		route:      make(chan *ServerComMessage),
		reg:        make(chan sessionReg),
		unreg:      make(chan topicUnreg),
		topicsLive: new(expvar.Int),
		presence:   InitPresenceHandler()}

	expvar.Publish("LiveTopics", h.topicsLive)

	go h.run()

	return h
}

func simpleSender(sendto chan<- *ServerComMessage, msg *ServerComMessage) {
	select {
	case sendto <- msg:
	case <-time.After(time.Second):
	}
}

func simpleByteSender(sendto chan<- []byte, msg *ServerComMessage) {
	data, _ := json.Marshal(msg)
	select {
	case sendto <- data:
	case <-time.After(time.Second):
	}
}

func (h *Hub) run() {
	log.Println("Hub started")

	for {
		select {
		case sreg := <-h.reg:
			// handle incoming connection: attach it to an existing topic or create
			// a new topic, if permitted
			t := h.topicGet(sreg.sess.appid, sreg.topic)
			if t == nil && !sreg.sess.uid.IsZero() {
				// Only authenticated users can create new topics
				t = &Topic{name: sreg.topic,
					original:    sreg.original,
					appid:       sreg.sess.appid,
					uid:         sreg.sess.uid,
					connections: make(map[*Session]bool),
					incoming:    make(chan *ServerComMessage, 256),
					reg:         make(chan sessionReg, 32),
					unreg:       make(chan *Session, 32)}
				h.topicPut(sreg.sess.appid, sreg.topic, t)

				log.Println("Hub. Topic created: " + t.name)
				h.topicsLive.Add(1)

				go t.run(h)
			}

			if t != nil {
				t.reg <- sreg
				// Reply sent from topic
			} else {
				log.Println("Cannot create session " + sreg.topic)
				go simpleByteSender(sreg.sess.send, ErrPermissionDenied(sreg.msgId, sreg.original))
			}

		case msg := <-h.route:
			// This is a message from a connection not subscribed to topic
			// Route incoming message to topic if topic permits such routing
			var reply *ServerComMessage
			var id string
			if msg.Ctrl != nil {
				id = msg.Ctrl.Id
			} else {
				id = msg.Data.Id
			}
			if dst := h.topicGet(msg.appid, msg.rcptto); dst != nil {
				// Everything is OK, sending packet to known topic
				log.Printf("Hub. Sending message to '%s'", dst.name)

				// Launching a separate goroutine - do not want to block the Hub
				// waiting for a possibly busy topic
				go simpleSender(dst.incoming, msg)

				reply = NoErrAccepted(id, dst.name)
			} else {
				log.Printf("Hub. Topic '%d.%s' is unknown or offline", msg.appid, msg.rcptto)
				reply = ErrUserNotFound(id, msg.rcptto)
			}

			// Report delivery status back to originating session
			if msg.akn != nil {
				go simpleByteSender(msg.akn, reply)
			}

		case unreg := <-h.unreg:
			if t := h.topicGet(unreg.appid, unreg.topic); t != nil {
				h.topicDel(unreg.appid, unreg.topic)
				h.topicsLive.Add(-1)
				t.connections = nil
			}

		case <-time.After(IDLETIMEOUT):
		}
	}
}
