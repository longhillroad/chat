package main

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
 *  File        :  datamode.go
 *  Author      :  Gene Sokolov
 *  Created     :  18-May-2014
 *
 ******************************************************************************
 *
 *  Description : 
 *
 * Messaging structures
 *
 * ==Client to server messages
 *
 *  login: authenticate user
 *    scheme string // optional, defaults to "basic"
 *      "basic": secret = uname+ ":" + password (not base64 encoded)
 *      "token": secret = token, obtained earlier
 *    secret string // optional, authentication string
 *    expireIn string // optional, string of the form "5m"
 *
 *  sub: subsribe to a topic
 *    topic string; // required, name of the topic, [A-Za-z0-9+/=].
 *    // Special topics:
 *      "!new": create a new topic
 *      "!me": declare your online presence, start receiving targeted publications
 *      "!pres": topic for presence updates
 *    params map[string]; // optional, topic-dependent parameters
 *
 *  unsub: unsubscribe from a topic
 *    topic string; // required
 *    params map[string]; // optional, topic-dependent parameters
 *
 *  pub: publish a message to a topic
 *    topic string; // name of the topic to publish to
 *    // Special topics:
 *      "!usr:<username>" send a message to another user
 *      "!pres" - update presence information
 *    params map[string]; // optional, message delivery parameters
 *    echo bool (false) = echo this message back to publisher as ctrl payload
 *    content interface{};   // required, payload, passed unchanged
 *
 *
 * ==Server to client messages
 *
 *  ctrl: error or control message
 *    code int; // HTTP Status code
 *    text string; // optional text string
 *    topic string; // optional topic name if the packet is a response in context of a topic
 *    params map[string]; // optional params
 *
 *  data: content, generic data
 *    topic string; // name of the originating topic, could be "!usr:<username>"
 *    origin string: // channel of the person who sent the message, optional
 *    id int; // optional message id
 *    content interface{}; // required, payload, passed unchanged
 *
 *****************************************************************************/

import (
	"net/http"
	"reflect"
	"strings"
	"time"
)

type JsonDuration time.Duration

func (jd *JsonDuration) UnmarshalJSON(data []byte) (err error) {
	d, err := time.ParseDuration(strings.Trim(string(data), "\""))
	*jd = JsonDuration(d)
	return err
}

// Client to Server messages

type MsgClientLogin struct {
	Id       string       `json:"id,omitempty"`
	Scheme   string       `jdon:"scheme,omitempty"`
	Secret   string       `json:"secret"`
	ExpireIn JsonDuration `json:"expireIn,omitempty"`
}

type MsgClientHeader struct {
	Id     string                 `json:"id,omitempty"`
	Topic  string                 `json:"topic"`
	Params map[string]interface{} `json:"params"`
}

type MsgClientSub struct {
	MsgClientHeader
}

func (msg *MsgClientSub) GetBoolParam(name string) bool {
	return modelGetBoolParam(msg.Params, name)
}

type MsgClientUnsub struct {
	MsgClientHeader
}

type MsgClientPub struct {
	MsgClientHeader
	Content interface{} `json:"content"`
}

func (msg *MsgClientPub) GetBoolParam(name string) bool {
	return modelGetBoolParam(msg.Params, name)
}

type ClientComMessage struct {
	Login *MsgClientLogin `json:"login"`
	Sub   *MsgClientSub   `json:"sub"`
	Unsub *MsgClientUnsub `json:"unsub"`
	Pub   *MsgClientPub   `json:"pub"`
	// from: userid as string
	from string
}

// Server to client messages

type MsgServerCtrl struct {
	Id     string                 `json:"id,omitempty"`
	Code   int                    `json:"code"`
	Text   string                 `json:"text,omitempty"`
	Topic  string                 `json:"topic,omitempty"`
	Params map[string]interface{} `json:"params,omitempty"`
}

type MsgServerData struct {
	Id      string      `json:"id,omitempty" gorethink:"id,omitempty"`
	Topic   string      `json:"topic"  gorethink:"topic"`
	Origin  string      `json:"origin,omitempty" gorethink:"origin,omitempty"` // could be empty if sent by system (like !pres)
	Content interface{} `json:"content" gorethink:"content"`
}

type ServerComMessage struct {
	Ctrl *MsgServerCtrl `json:"ctrl,omitempty"`
	Data *MsgServerData `json:"data,omitempty"`
	// to: topic
	rcptto string
	// appid, also for routing
	appid uint32
	// originating session, copy of Session.send
	akn chan<- []byte
}

// Combined message
type ComMessage struct {
	*ClientComMessage
	*ServerComMessage
}

// REST API structures
type User struct {
	Username string      `json:"username"`
	Password string      `json:"-"`
	Passhash []byte      `json:"-"`
	Other    interface{} `json:"other"`
}

type Contact struct {
	Username string `json:"username"`
	Owner    string `json:"owner"`
	active   bool
	Tags     []string `json:"tags"`
}

func modelGetBoolParam(params map[string]interface{}, name string) bool {
	var val bool
	if params != nil {
		if param, ok := params[name]; ok {
			switch param.(type) {
			case bool:
				val = param.(bool)
			case float64:
				val = (param.(float64) != 0.0)
			}
		}
	}

	return val
}

func modelGetInt64Param(params map[string]interface{}, name string) int64 {
	var val int64
	if params != nil {
		if param, ok := params[name]; ok {
			switch param.(type) {
			case int8, int16, int32, int64, int:
				val = reflect.ValueOf(param).Int()
			case float32, float64:
				val = int64(reflect.ValueOf(param).Float())
			}
		}
	}

	return val
}

// Generators of error messages

func NoErr(id, topic string) *ServerComMessage {
	msg := &ServerComMessage{Ctrl: &MsgServerCtrl{
		Id:    id,
		Code:  http.StatusOK,
		Text:  "ok",
		Topic: topic}}
	return msg
}

func NoErrAccepted(id, topic string) *ServerComMessage {
	msg := &ServerComMessage{Ctrl: &MsgServerCtrl{
		Id:    id,
		Code:  http.StatusAccepted,
		Text:  "message accepted for delivery",
		Topic: topic}}
	return msg
}

func ErrAuthRequired(id, topic string) *ServerComMessage {
	msg := &ServerComMessage{Ctrl: &MsgServerCtrl{
		Id:    id,
		Code:  http.StatusUnauthorized,
		Text:  "authentication required",
		Topic: topic}}
	return msg
}
func ErrAuthFailed(id, topic string) *ServerComMessage {
	msg := &ServerComMessage{Ctrl: &MsgServerCtrl{
		Id:    id,
		Code:  http.StatusForbidden,
		Text:  "authentication failed",
		Topic: topic}}
	return msg
}
func ErrAlreadyAuthenticated(id, topic string) *ServerComMessage {
	msg := &ServerComMessage{Ctrl: &MsgServerCtrl{
		Id:    id,
		Code:  http.StatusConflict,
		Text:  "already authenticated",
		Topic: topic}}
	return msg
}
func ErrAuthUnknownScheme(id, topic string) *ServerComMessage {
	msg := &ServerComMessage{Ctrl: &MsgServerCtrl{
		Id:    id,
		Code:  http.StatusForbidden,
		Text:  "unknown authentication scheme",
		Topic: topic}}
	return msg
}
func ErrPermissionDenied(id, topic string) *ServerComMessage {
	msg := &ServerComMessage{Ctrl: &MsgServerCtrl{
		Id:    id,
		Code:  http.StatusForbidden,
		Text:  "permission denied",
		Topic: topic}}
	return msg
}
func ErrMalformed(id, topic string) *ServerComMessage {
	msg := &ServerComMessage{Ctrl: &MsgServerCtrl{
		Id:    id,
		Code:  http.StatusBadRequest,
		Text:  "malformed data",
		Topic: topic}}
	return msg
}
func ErrSubscribeFailed(id, topic string) *ServerComMessage {
	msg := &ServerComMessage{Ctrl: &MsgServerCtrl{
		Id:    id,
		Code:  http.StatusNotFound,
		Text:  "failed to subscribe",
		Topic: topic}}
	return msg
}
func ErrUserNotFound(id, topic string) *ServerComMessage {
	msg := &ServerComMessage{Ctrl: &MsgServerCtrl{
		Id:    id,
		Code:  http.StatusNotFound,
		Text:  "user not found or offline",
		Topic: topic}}
	return msg
}
func ErrUnknown(id, topic string) *ServerComMessage {
	msg := &ServerComMessage{Ctrl: &MsgServerCtrl{
		Id:    id,
		Code:  http.StatusInternalServerError,
		Text:  "internal error",
		Topic: topic}}
	return msg
}
func ErrUnrecognized(id, topic string) *ServerComMessage {
	msg := &ServerComMessage{Ctrl: &MsgServerCtrl{
		Id:    id,
		Code:  http.StatusBadRequest,
		Text:  "unrecognized input",
		Topic: topic}}
	return msg
}
func ErrAlreadySubscribed(id, topic string) *ServerComMessage {
	msg := &ServerComMessage{Ctrl: &MsgServerCtrl{
		Id:    id,
		Code:  http.StatusConflict,
		Text:  "already subscribed",
		Topic: topic}}
	return msg
}
func ErrNotSubscribed(id, topic string) *ServerComMessage {
	msg := &ServerComMessage{Ctrl: &MsgServerCtrl{
		Id:    id,
		Code:  http.StatusConflict,
		Text:  "not subscribed",
		Topic: topic}}
	return msg
}
