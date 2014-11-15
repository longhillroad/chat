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
 *  File        :  topic.go
 *  Author      :  Gene Sokolov
 *  Created     :  18-May-2014
 *
 ******************************************************************************
 *
 *  Description : 
 *    An isolated communication channel (chat room, 1:1 conversation, control
 *    connection) for usualy multiple users. There is no communication across topics
 *  
 *
 *****************************************************************************/
package main

import (
	"encoding/json"
	"errors"
	"github.com/or-else/guid"
	"log"
	"strings"
	"time"
)

// Topic: an isolated communication channel
type Topic struct {
	// expanded name of the topic (!usr:UID or !pres:UID)
	name string
	// original name of the topic (!me or !pres)
	original string

	// AppID
	appid uint32

	// User ID of the topic owner
	uid guid.GUID
	//owner string

	// Sessions subscribed to this topic
	connections map[*Session]bool

	// Inbound messages from sessions or other topics, already converted to SCM. Buffered chan
	incoming chan *ServerComMessage

	// Subscribe requests from sessions, buffered
	reg chan sessionReg

	// Unsubscribe requests from sessions, buffered
	unreg chan *Session
}

func loadUserTopicsAndReply(hub *Hub, sess *Session, msgId string,
	uid guid.GUID, topic string, firstCall bool) {

	lastSeen, status := GetLastSeenAndStatus(sess.appid, uid)
	topics := LoadTopics(sess.appid, uid, lastSeen, 64)

	log.Println("Loaded topics ", len(topics), " since ", lastSeen)

	reply := NoErr(msgId, topic)
	reply.Ctrl.Params = map[string]interface{}{}
	if len(topics) > 0 {
		reply.Ctrl.Params["topicsUpdated"] = topics
	}
	reply.Ctrl.Params["lastSeen"] = lastSeen
	if status != nil {
		reply.Ctrl.Params["status"] = status
	}
	simpleByteSender(sess.send, reply)

	if firstCall {
		// This is a newly created !me topic, announce presence
		hub.presence <- &PresenceChange{AppId: sess.appid, Id: uid,
			Action: ActionOnline, Status: status}
	}
}

func (t *Topic) run(hub *Hub) {
	defer func() {
		hub.unreg <- topicUnreg{appid: t.appid, topic: t.name}
		log.Printf("Topic stopped: '%s'", t.original)
	}()

	log.Printf("Topic started: '%s'", t.name)

	var keepAlive time.Duration

	killTimer := time.NewTimer(time.Hour)
	killTimer.Stop()

	var self, pres bool
	firstCall := true

	if strings.HasPrefix(t.name, "!usr") {
		self = true
	} else if strings.HasPrefix(t.name, "!pres") {
		pres = true
	}

	for {
		select {
		case sreg := <-t.reg:
			// Request to add a conection to topic
			// Set keepAlive value if not set earlier
			killTimer.Stop()
			if t.uid.Equal(sreg.sess.uid) && keepAlive == 0 {
				keepAlive = time.Second * time.Duration(modelGetInt64Param(sreg.params, "keepAlive")) // in seconds
				if keepAlive <= 0 {
					keepAlive = time.Second * 5
				}
			}
			// give a broadcast channel to connection (read)
			// give channel to use when shutting down (done)
			sreg.sess.subs[t.name] = &Subscription{read: t.incoming, done: t.unreg}
			t.connections[sreg.sess] = true

			var replied = false
			if pres {
				list, err := presParseContacts(t.appid, t.uid, sreg.params["filter"])
				if err != nil {
					log.Println("Failed to fetch !pres parameters ", err)
				}
				hub.presence <- &PresenceChange{AppId: t.appid, Id: t.uid,
					Action: ActionSubscribed, Sess: sreg.sess, ListOfGUIDs: list}
			} else if self {
				go loadUserTopicsAndReply(hub, sreg.sess, sreg.msgId, t.uid, t.original, firstCall)
				replied = true
			}

			if !replied {
				go simpleByteSender(sreg.sess.send, NoErr(sreg.msgId, t.original))
			}
		case sess := <-t.unreg:
			// Remove connection from topic
			// This is just an unsubscribe, session may continue to function
			delete(sess.subs, t.name)
			delete(t.connections, sess)
			// Kill this topic after a timeout
			if len(t.connections) == 0 {
				killTimer.Reset(keepAlive)
			}
			if self {
				UpdateLastSeen(t.appid, t.uid)
			}
			// Session sends reply

		case msg := <-t.incoming:
			// Message to broadcast to subscribed sessions
			log.Printf("topic[%s].run: got message '%v'", t.name, msg)

			// Broadcast the message
			var data, _ = json.Marshal(msg)
			for sess := range t.connections {
				select {
				case sess.send <- data:
				default:
					log.Printf("topic[%s].run: connection stuck, unsubscribing", t.name)
					delete(sess.subs, t.name)
					delete(t.connections, sess)
				}
			}

		case <-killTimer.C:
			log.Println("Topic timeout: ", t.name)
			if self {
				// This is a !me topic, announce going offline
				hub.presence <- &PresenceChange{AppId: t.appid, Id: t.uid, Action: ActionOffline}
			} else if pres {
				// This is a !pres topic, stop getting notifications
				hub.presence <- &PresenceChange{AppId: t.appid, Id: t.uid, Action: ActionUnsubscribed}
			}
			return
		}
		firstCall = false
	}
}

func presParseContacts(appid uint32, uid guid.GUID, f interface{}) ([]guid.GUID, error) {
	var list []guid.GUID

	if f == nil {
		// Default filter - load from DB
		var err error
		if list, err = contactsLoad(appid, uid); err != nil {
			log.Println(err)
			return nil, err
		}
	} else {
		switch f.(type) {
		case []string:
			// explicit list of user ids
			filter, _ := f.([]string)
			for _, one := range filter {
				id := guid.ParseGUID(one)
				if !id.IsZero() {
					list = append(list, id)
				}
			}
		default:
			return nil, errors.New("failed to parse filter")
		}
	}

	return list, nil
}
