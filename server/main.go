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
 *  File        :  main.go
 *  Author      :  Gene Sokolov
 *  Created     :  18-May-2014
 *
 ******************************************************************************
 *
 *  Description : 
 *
 *  Setup & initialization.
 *
 *****************************************************************************/

package main

import (
	"crypto/rand"
	_ "expvar"
	"flag"
	"github.com/gorilla/mux"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"
)

const (
	IDLETIMEOUT  = time.Second * 55  // Terminate session after this timeout.
	TOPICTIMEOUT = time.Minute * 5   // Tear down topic after this period of silence.

	// API version
	VERSION = "0.01"

	// Lofetime of authentication tokens
	TOKEN_LIFETIME_DEFAULT = time.Hour * 12     // 12 hours
	TOKEN_LIFETIME_MAX     = time.Hour * 24 * 7 // 1 week
)

var (
	globalhub *Hub

	storage *Storage

	sessionStore *SessionStore
)

func main() {
	// For serving static content
	path, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Home dir: '%s'", path)

	_, err = rand.Read(randSeed)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Server started with processes: %d",
		runtime.GOMAXPROCS(runtime.NumCPU()))

	var listenOn = flag.String("bind", "127.0.0.1:8080", "0.0.0.0:0 - IP address/host name and port number to listen on")
	// Database selector. Currently not implemented.
	var dbsource = flag.String("db", "file:tinode.sqlite?cache=shared&mode=rwc",
		"Database source (this is currently ignored)")
	flag.Parse()

	log.Printf("Listening on [%s]", *listenOn)

	storage = NewStorage(*dbsource)
	defer storage.Close()

	sessionStore = NewSessionStore(2 * time.Hour)

	globalhub = newHub()

	// Static content from http://<host>/x/: just read files from disk
	http.Handle("/x/", http.StripPrefix("/x/", http.FileServer(http.Dir(path+"/static"))))

	// Streaming channels
	// Handle long polling clients
	http.HandleFunc("/v0/channels/lp", serveLongPoll)
	// Handle websocket clients
	http.HandleFunc("/v0/channels", serveWebSocket)

	// REST
	rest := mux.NewRouter().PathPrefix("/v0").Subrouter()
	MountCrud(rest)

	http.Handle("/", rest)

	log.Fatal(http.ListenAndServe(*listenOn, nil))
}
