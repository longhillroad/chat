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
 *  File        :  auth.go
 *  Author      :  Gene Sokolov
 *  Created     :  18-May-2014
 *
 ******************************************************************************
 *
 *  Description : 
 *
 *  Authentication
 *
 *****************************************************************************/
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"github.com/or-else/guid"
	"log"
	"strings"
	"time"
)

// 32 random bytes
var hmac_salt = []byte{
	0x4f, 0xbd, 0x77, 0xfe, 0xb6, 0x18, 0x81, 0x6e,
	0xe0, 0xe2, 0x6d, 0xef, 0x1b, 0xac, 0xc6, 0x46,
	0x1e, 0xfe, 0x14, 0xcd, 0x6d, 0xd1, 0x3f, 0x23,
	0xd7, 0x79, 0x28, 0x5d, 0x27, 0x0e, 0x02, 0x3e}

// FIXME(gene) there is a potential race condition on 'counter'. Should not be a
// problem for now, but not production-quality
var counter uint64 = 0

var randSeed = make([]byte, 16)

// Returns 22 char long string
// TODO(gene): replace with crypt/Rand or snowflake
func getRandomString() string {
	counter++

	hasher := md5.New()
	hasher.Write(randSeed)
	binary.Write(hasher, binary.LittleEndian, time.Now().UnixNano())
	binary.Write(hasher, binary.LittleEndian, counter)
	return strings.TrimRight(base64.URLEncoding.EncodeToString(hasher.Sum(nil)), "=")
}

// End-user authentication function
func authUser(appid uint32, uname, secret string) (userId guid.GUID) {
	userId = guid.Zero()

	var err error
	defer func() {
		if err != nil {
			log.Println(err)
		}
	}()

	rows, err := storage.Db(appid).Table("users").GetAllByIndex("username", uname).
		Pluck("id", "passhash").Run(storage.sess)
	if err != nil {
		return
	}

	if rows.Next() {
		var user map[string]interface{}
		err = rows.Scan(&user)
		if err != nil {
			return
		}

		passhash, err := base64.URLEncoding.DecodeString(user["passhash"].(string))
		if err != nil {
			err = errors.New("internal: invalid passhash")
			return
		}

		if !isValidPass(secret, passhash) {
			err = errors.New("invalid password")
			return
		}

		userId = guid.ParseGUID(user["id"].(string))
		if userId.IsZero() {
			err = errors.New("internal: invalid GUID " + user["id"].(string))
		}

	} else {
		// User not found
		err = errors.New("user not found")
		return
	}

	return
}

func passHash(password string) []byte {
	hasher := hmac.New(md5.New, hmac_salt)
	hasher.Write([]byte(password))
	return hasher.Sum(nil)
}

func isValidPass(password string, validMac []byte) bool {
	return hmac.Equal(validMac, passHash(password))
}

// Client signature validation
//   key: client's secret key
// Returns application id
func checkApiKey(apikey string) (appid uint32, isRoot bool) {
	// Composition:
	//   [1:algorithm version][4:appid][2:clientid][2:key sequence][1:isRoot][4:expiration][16:signature] = 30 bytes
	// convertible to base64 without padding
	// All integers are little-endian
	appid = 0
	isRoot = false

	if declen := base64.URLEncoding.DecodedLen(len(apikey)); declen != 30 {
		return
	}

	data, err := base64.URLEncoding.DecodeString(apikey)
	if err != nil {
		log.Println("failed to decode.base64 appid ", err)
		return
	}
	if data[0] != 1 {
		log.Println("unknown appid signature algorithm ", data[0])
		return
	}

	hasher := hmac.New(md5.New, hmac_salt)
	hasher.Write(data[:14])
	check := hasher.Sum(nil)
	if !bytes.Equal(data[14:], check) {
		log.Println("invalid apikey signature")
		return
	}

	exp := binary.LittleEndian.Uint32(data[10:14])
	if time.Unix(int64(exp), 0).Before(time.Now()) {
		log.Println("expired apikey")
		return
	}

	appid = binary.LittleEndian.Uint32(data[1:5])
	isRoot = (data[9] == 1)
	return
}

func makeSecurityToken(uid guid.GUID, expires time.Time) string {
	// [16:GUID][4:expires][16:signature] == 36 bytes

	var buf [36]byte
	copy(buf[:], uid.ToBytes())
	binary.LittleEndian.PutUint32(buf[16:], uint32(expires.Unix()))

	hasher := hmac.New(md5.New, hmac_salt)
	hasher.Write(buf[:20])
	signature := hasher.Sum(nil)

	copy(buf[20:], signature)
	return base64.URLEncoding.EncodeToString(buf[:])
}

func checkSecurityToken(token string) (uid guid.GUID, expires time.Time) {
	// [16:GUID][4:expires][16:signature] == 36 bytes

	if declen := base64.URLEncoding.DecodedLen(len(token)); declen != 36 {
		return
	}

	data, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		log.Println("failed to decode.base64 sectoken ", err)
		return
	}
	hasher := hmac.New(md5.New, hmac_salt)
	hasher.Write(data[:20])
	check := hasher.Sum(nil)
	if !bytes.Equal(data[20:], check) {
		log.Println("invalid sectoken signature")
		return
	}

	expires = time.Unix(int64(binary.LittleEndian.Uint32(data[16:20])), 0).UTC()
	if expires.Before(time.Now()) {
		log.Println("expired sectoken")
		return
	}

	return guid.FromBytes(data[0:16]), expires
}

// Check API key for origin, and revocation. The key must be valid
//   origin: browser-provided Origin URL
//   dbcheck: if true, validate key & origin by calling the database (shold do one on the first connection)
func authClient(appid int32, apikey, origin string) error {
	// TODO(gene): validate key with database
	// data, err := base64.URLEncoding.DecodeString(apikey)
	// var clientid = binary.LittleEndian.Uint16(data[5:7])
	// var version = binary.LittleEndian.Uint16(data[7:9])

	return nil
}
