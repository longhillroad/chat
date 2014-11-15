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
 *  File        :  tinode-0.2.js
 *  Author      :  Gene Sokolov
 *  Created     :  18-May-2014
 *
 *****************************************************************************
 *
 *  Description : 
 *
 *  Common module methods and parameters
 *
 *****************************************************************************/

var Tinode = (function() {
	var tinode = {
		"PROTOVERSION": "0",
		"VERSION": "0.2",
		"TOPIC_ME": "!me",
		"TOPIC_PRES": "!pres",
		"TOPIC_P2P": "!usr:"
	}

	var _logging_on = false
	
	/**
	* Initialize Tinode module.
	* 
	* @param apikey API key issued by Tinode
	* @baseurl debugging only. It will be hardcoded in the release version as https://api.tinode.com/
	*/
	tinode.init = function(apikey, baseurl) {
		// API Key
		tinode.apikey = apikey
		// Authentication token returned by call to Login
		tinode.token = null
		// Expiration time of the current security token
		tinode.tokenExpires = null
		// UID of the current user
		tinode.myUID = null
		// API endpoint. This should be hardcoded in the future
		// tinode.baseUrl = "https://api.tinode.com/v0"
		var parser = document.createElement('a')
		parser.href = baseurl
		parser.pathname = parser.pathname || "/"
		parser.pathname += "v" + Tinode.PROTOVERSION
		tinode.baseUrl = parser.protocol + '//' + parser.host + parser.pathname + parser.search
		
		// Tinode-global contacts cache
		tinode.contCache = {}
	}
	
	/**
	* Basic cross-domain requester.
	* Supports normal browsers and IE8+
	*/
	tinode.xdreq = function() {
		var xdreq = null
		
		// Detect browser support for CORS
		if ('withCredentials' in new XMLHttpRequest()) {
			// Support for standard cross-domain requests
			xdreq = new XMLHttpRequest()
		} else if (typeof XDomainRequest !== "undefined") {
			// IE-specific "CORS" with XDR
			xdreq = new XDomainRequest()
		} else {
			// Browser without CORS support, don't know how to handle
			throw new Error("browser not supported");
		}
		
		return xdreq
	}
	
	tinode.getCurrentUserID = function() {
		return tinode.myUID
	}
	
	tinode.getTokenExpiration = function() {
		return tinode.tokenExpires
	}
	
	tinode.getToken = function() {
		return tinode.token
	}
	
	// Utility functions
	tinode._jsonHelper = function(key, val) {
		if (val == null || val.length === 0) {
			return undefined
		}
		return val
	}
	
	tinode.logToConsole = function(val) {
		_logging_on = val
	}
	
	tinode._logger = function(str) {
		if (_logging_on) {
			console.log(str)
		}
	}
	
	return tinode
})()

/*
* Submodule for handling streaming connections. Websocket or long polling
*/
Tinode.streaming = (function() {
	var streaming = {}

	// Private variables
	// Websocket
	var _socket = null
	// Composed endpoint URL, with API key but without long polling SID
	var _channelURL = null
	// Url for long polling, complete with SID
	var _lpURL = null
	// Long polling xdreqs, one for long polling, the other for sending:
	var _poller = null
	var _sender = null
	// counter of received packets
	var _inPacketCount = 0
	
	/**
	* Initialize the module. 
	*
	* @param transport "lp" for long polling or "ws" (default) for websocket
	*/
	streaming.init = function(transport) {
		// Counter used for generation of message IDs
		streaming.messageId = 0

		// Callbacks:
		// connection established
		streaming.onConnect = null
		// login completed
		streaming.onLogin = null
		// control message received
		streaming.onCtrlMessage = null
		// content message received
		streaming.onDataMessage = null
		// any message received
		streaming.onMessage = null
		// connection closed
		streaming.onDisconnect = null

		// Presence notifications
		streaming.onPresenceChange = null
		
		// Request echo packets
		streaming.Echo = false;
		
		if (transport === "lp") {
			// explicit request to use long polling
			init_lp()
		} else if (transport === "ws") {
			// explicit request to use web socket
			// if websockets are not available, horrible things will happen
			init_ws();
		} else {
			// Default transport selection
			if (!window["WebSocket"]) {
				// The browser has no websockets
				init_lp();
			} else {
				// Using web sockets -- default
				init_ws();
			}
		}
	}
	
	// Packet definitions
	function make_packet(type, topic) {
		var pkt = null
		switch (type) {
			case "login":
				return {
					"login": {
						"scheme": null,
						"secret": null,
						"id": "login", // Hardcoded message id
						"expireIn": null
					}
				}
			case "sub":
				pkt = {
					"sub": {
						"topic": topic,
						"params": {}
					}
				}
				if (streaming.messageId != 0) {
					pkt.sub.id = "" + streaming.messageId++
				}
				return pkt
			case "unsub":
				pkt = {
					"unsub": {
						"topic": topic
					}
				}
				if (streaming.messageId != 0) {
					pkt.unsub.id = "" + streaming.messageId++
				}
				return pkt
			case "pub":
				pkt = {
					"pub": {
						"topic": topic,
						"params": {},
						"content": {}
					}
				}
				if (streaming.messageId != 0) {
					pkt.pub.id = "" + streaming.messageId++
				}
				return pkt
			default:
				throw new Error("unknown packet type requested: " + type)
		}
	}
		
	// Helper function for creating endpoint URL
	function channel_url(url, protocol) {
		var parser = document.createElement('a')
		parser.href = url
		parser.pathname += "/channels"
		if (protocol === "http" || protocol === "https") {
			// Long polling endpoint end with "lp", i.e.
			// '/v0/channels/lp' vs just '/v0/channels'
			parser.pathname += "/lp"
		}
		parser.search = (parser.search ? parser.search : "?") + "apikey=" + Tinode.apikey
		return protocol + '://' + parser.host + parser.pathname + parser.search
	}

	// Message dispatcher
	function on_message(data) {
		// Skip empty response. This happens when LP times out.
		if (!data) return

		_inPacketCount++

		Tinode._logger("in: " + data)

		// Send raw message to listener
		if (streaming.onMessage) {
			streaming.onMessage(data)
		}

		var pkt = JSON.parse(data)
		if (!pkt) {
			Tinode._logger("ERROR: failed to parse data '" + data + "'")
		} else if (pkt.ctrl != null) {
			if (_inPacketCount == 1) {
				// The very first incoming packet. This is response to connection attempt
				if (streaming.onConnect) {
					streaming.onConnect(pkt.ctrl.code, pkt.ctrl.text, pkt.ctrl.params)
				}
			} else if (pkt.ctrl.id == "login") {
				if (pkt.ctrl.code == 200) {
					// This is a response to a successful login, extract UID and security token, save it in Tinode module
					Tinode.token = pkt.ctrl.params.token
					Tinode.myUID = pkt.ctrl.params.uid
					Tinode.tokenExpires = new Date(pkt.ctrl.params.expires)
				}

				if (streaming.onLogin) {
						streaming.onLogin(pkt.ctrl.code, pkt.ctrl.text)
				}
			}

			if (streaming.onCtrlMessage) {
					streaming.onCtrlMessage(pkt.ctrl)
			}
		} else if (pkt.data != null) {
			if (pkt.data.topic === "!pres") {
				// this is a presence notification
				for (var idx in pkt.data.content) {
					// update cached contacts, if any
					var upd = pkt.data.content[idx]
					var contact = Tinode.contCache[upd.who]
					if (contact) {
						contact.online = upd.online
						contact.status = upd.status
						Tinode.contCache[upd.who] = contact
					} else {
						contact = upd
					}
					
					// Notify listener of contact change
					if (streaming.onPresenceChange) {
						streaming.onPresenceChange(upd.who, contact)
					}
				}
			}
				
			if (streaming.onDataMessage) {
				streaming.onDataMessage(pkt.data)
			}
		} else {
			Tinode._logger("unknown packet received")
		}
	}
	
	streaming.Login = function(uname, secret) {
		var pkt = make_packet("login")
		pkt.login.scheme = "basic"
		pkt.login.secret = uname + ":" + secret
		pkt.login.expireIn = "24h"
		streaming._send(pkt);
		return pkt.login.id
	}

	streaming.Subscribe = function(topic, params) {
		var pkt = make_packet("sub", topic)
		pkt.sub.params = params
		streaming._send(pkt);
		return pkt.sub.id
	}

	streaming.Unsubscribe = function(topic) {
		var pkt = make_packet("unsub", topic)
		streaming._send(pkt);
		return pkt.unsub.id
	}

	streaming.Publish = function(topic, params, data) {
		var pkt = make_packet("pub", topic)
		if (params) {
			pkt.pub.params = params
		}
		if (streaming.Echo) {
			pkt.pub.params["echo"] = true
		}
		pkt.pub.content = data
		streaming._send(pkt);
		return pkt.pub.id
	}
	
	// Initialization for Websocket
	function init_ws() {
		_socket = null

		streaming.Connect = function(secure) {
			_channelURL = channel_url(Tinode.baseUrl, secure ? "wss" : "ws")
			Tinode._logger("Connecting to: " + _channelURL)
			var conn = new WebSocket(_channelURL)

			conn.onopen = function(evt) {
				if (streaming.onSocketOpen) {
					streaming.onSocketOpen()
				}
			}
			
			conn.onclose = function(evt) {
				_socket = null
				_inPacketCount = 0
				if (streaming.onDisconnect) {
					streaming.onDisconnect()
				}
			}
			
			conn.onmessage = function(evt) {
				on_message(evt.data)
			}

			_socket = conn
		}

		streaming.Disconnect = function() {
			if (_socket) {
				_socket.close()
			}
		}

		streaming._send = function(pkt) {
			var msg = JSON.stringify(pkt, Tinode._jsonHelper)
			Tinode._logger("out: " + msg)
			
			if (_socket.readyState == _socket.OPEN) {
				_socket.send(msg);
			} else {
				throw new Error("websocket not connected")
			}			
		}		
	}

	function init_lp() {
		// Fully composed endpoint URL, with key & SID
		_lpURL = null
		
		_poller = null
		_sender = null

		function lp_sender(url) {
			var sender = Tinode.xdreq()
			sender.open('POST', url, true);

			sender.onreadystatechange = function(evt) {
				if (sender.readyState == 4 && sender.status >= 400) {
					// Some sort of error response 
					Tinode._logger("ERROR: lp sender failed, " + sender.status)
				}
			}

			return sender
		}

		function lp_poller(url) {			
			var poller = Tinode.xdreq()
			poller.open('GET', url, true);

			poller.onreadystatechange = function(evt) {
				if (poller.readyState == 4) { // 4 == DONE
					if (poller.status == 201) { // 201 == HTTP.Created, get SID
						var pkt = JSON.parse(poller.responseText)
						var text = poller.responseText
						
						_lpURL = _channelURL + "&sid=" + pkt.ctrl.params.sid
						poller = lp_poller(_lpURL, true)
						poller.send(null)

						on_message(text)
						
						//if (streaming.onConnect) {
						//	streaming.onConnect()
						//}
					} else if (poller.status == 200) { // 200 = HTTP.OK
						on_message(poller.responseText)
						poller = lp_poller(_lpURL)
						poller.send(null)
					} else {
						// Don't throw an error here, gracefully handle server errors
						on_message(poller.responseText)
						if (streaming.onDisconnect) {
							streaming.onDisconnect()
						}
					}
				}
			}

			return poller
		}

		streaming._send = function(pkt) {
			var msg = JSON.stringify(pkt, Tinode._jsonHelper)			
			Tinode._logger("out: " + msg)
			
			_sender = lp_sender(_lpURL)
			if (_sender.readyState == 1) { // 1 == OPENED
				_sender.send(msg);
			} else {
				throw new Error("long poller failed to connect")
			}
		}
		
		streaming.Connect = function(secure) {
			_channelURL = channel_url(Tinode.baseUrl, secure ? "https" : "http")
			_poller = lp_poller(_channelURL)
			_poller.send(null)
		}

		streaming.Disconnect = function() {
			if (_sender) {
				_sender.abort()
				_sender = null
			}
			if (_poller) {
				_poller.abort()
				_poller = null
			}
			if (streaming.onDisconnect) {
				streaming.onDisconnect()
			}
		}
	}

	streaming.wantAkn = function(status) {
		if (status) {
			streaming.messageId = Math.floor((Math.random()*0xFFFFFF)+0xFFFFFF)
		} else {
			streaming.messageId = 0
		}
	}
	
	streaming.setEcho = function(state) {
		streaming.Echo = state
	}
	
	return streaming
})()
		
/**
* Module for handling REST requests
*
*/
Tinode.rest = (function() {
	var rest = {}
	
	// Private utility function to construct base URL
	function rest_url(url, query, secure) {
		var parser = document.createElement('a')
		parser.href = url
		parser.search = (parser.search ? parser.search : "?") + "apikey=" + Tinode.apikey +
			"&token=" + Tinode.token
		if (query) {
			parser.search += "&_q=" + JSON.stringify(query)
		} 
			
		return (secure ? "https" : "http") + '://' + parser.host + parser.pathname + parser.search
	}
	
	// Inititialization for consistency with other modules
	rest.init = function() {}
	
	// REST Login
	rest.Login = function(user, secret, callback) {
		var url = rest_url(Tinode.baseUrl + "/login", false)
		var params = "username=" + user + "&secret=" + secret
		var requester = Tinode.xdreq()
		requester.open('POST', url, true)
		requester.setRequestHeader("Content-type", "application/x-www-form-urlencoded");
		requester.setRequestHeader("Content-length", params.length);
		requester.onreadystatechange = function(evt) {
			if (requester.readyState == 4) {
				callback(requester.status, JSON.parse(requester.responseText))
			}
		}
				
		requester.send(params)
	}

	// Constructor for generic REST methods
	rest.request = function() {
		return {
			"_url": Tinode.baseUrl,
			"_q": null,
			"addKind": function(kind) {
				this._url += "/" + kind
				return this
			},
			"addObjectId": function(objectId) {
				this._url += "/:" + objectId
				return this
			},
			"setSearchQuery": function(query) {
				this._q = query
				return this 
			},
			"addSearchTerm": function(key, val) {
				if (!this._q) {
					this._q = { type: "match", match: [] }
				}
				this._q.index = key
				this._q.match.push(val)
				return this
			},
			"build": function(secure) {
				return rest_url(this._url, this._q, secure)
			}
		}
	}
			
	rest.Get = function(req, callback) {
		var url = req.build(false)
		var requester = Tinode.xdreq()
		requester.open('GET', url, true)
		requester.onreadystatechange = function(evt) {
			if (requester.readyState == 4) {
				callback(requester.status, JSON.parse(requester.responseText))
			}
		}
		requester.send(null)
	}
	
	rest.Update = function(req, object, callback) {
		var url = req.build(false)
		var requester = Tinode.xdreq()
		requester.open('PUT', url, true)
		requester.onreadystatechange = function(evt) {
			if (requester.readyState == 4) {
				callback(requester.status, JSON.parse(requester.responseText))
			}
		}
		requester.send(JSON.stringify(object))
	}

	rest.Create = function(req, object, callback) {
		var url = req.build(false)
		var requester = Tinode.xdreq()
		requester.open('POST', url, true)
		requester.onreadystatechange = function(evt) {
			if (requester.readyState == 4) {
				callback(requester.status, JSON.parse(requester.responseText))
			}
		}
		requester.send(JSON.stringify(object))
	}

	rest.Delete = function(req, id, callback) {
		var url = req.build(false)
		var requester = this.xdreq()
		requester.open('DELETE', url, true)
		requester.onreadystatechange = function(evt) {
			if (requester.readyState == 4) {
				callback(requester.status, JSON.parse(requester.responseText))
			}
		}
		requester.send(null)
	}
	
	return rest
})()


// REST contacts management
Tinode.contacts = (function() {
	var cont = {}
	var rest = Tinode.rest
	
	// Initializer for consistency with other modules
	cont.init = function() {}
	
	// Load and cache contacts of the current user
	cont.Load = function(callback) {
		var req = rest.request()
		req = req.addKind("users").addObjectId(Tinode.getCurrentUserID()).addKind("contacts")
		rest.Get(req, function(code, res) {
			var count = 0
			if (code >= 400) {
				ctrl_message(res)
			} else {
				for (var idx in res) {
					contact = res[idx]
					Tinode.contCache[contact.contactId] = contact
					count++
				}
			}
			callback(count)
		})
	}
		
	cont.Create = function(contact, callback) {
		var req = rest.request()
		req = req.addKind("users").addObjectId(Tinode.getCurrentUserID()).addKind("contacts")		
		rest.Create(req, contact, callback)
	}
	
	cont.Update = function(who, contact, callback) {
		var req = rest.request()
		req = req.addKind("users").addObjectId(Tinode.getCurrentUserID()).addKind("contacts").addObjectId(who)		
		rest.Update(req, contact, callback)
	}
	
	// Get contact from local cache
	cont.get = function(who) {
		return Tinode.contCache[who]
	}
	
	// Iterate over loaded contacts
	cont.iterate = function(callback) {
		for (var contactId in Tinode.contCache) {
			callback(Tinode.contCache[contactId])
		}
	}
	
	// Get the number of contacts in local cache
	cont.getCount = function() {
		var count = 0
		if (Tinode.contCache) {
			count = Object.keys(Tinode.contCache).length
		}
		return count
	}
	
	// Clear local contacts cache
	cont.clear = function() {
		Tinode.contCache.clear()
	}
	
	return cont
})()

Tinode.messages = (function() {
	var messages = {}
	var rest = Tinode.rest
	
	// Initializer for consistency with other modules
	messages.init = function() {}
	
	// Load messages from server. 
	// Messages are ordered by time, from newest to oldest.
	// topic: string, name of the topic to load
	// limit: int, number of messages to load
	// offset: int, skip some messages
	// TODO(gene): Add fromTime parameter
	// TODO(gene): Use indexedStorage for caching messages locally
	messages.Load = function(topic, limit, offset, callback) {
		var req = rest.request()
		req = req.addKind("messages").setSearchQuery({
			"type":"match", 
			"index":"topic", 
			"match": [topic], 
			"limit":limit, "offset":offset
		})
		rest.Get(req, callback)
	}
	
	return messages
})()
