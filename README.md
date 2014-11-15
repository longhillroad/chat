Tinode chat
===========

Instant messaging as a service - backend in Go, client-side binding in Java and Javascript

Licenses
--------
* Backend is licensed under [GPL v.3](http://www.gnu.org/copyleft/gpl.html)
* Android and Javascrip bindings are licensed under [Apache 2.0](http://www.apache.org/licenses/LICENSE-2.0.html)
* Sample web app is in public domain

Dependencies
------------

* [RethinkDB](https://github.com/rethinkdb/rethinkdb) is the only supported backend at the moment
* [android-websockets](https://github.com/codebutler/android-websockets) is required by the Android client
* [jackson](https://github.com/FasterXML/jackson) for serializing and unserializing Java into JSON on Android
* [jQuery](http://jquery.com/) is used in the sample web application but it's not required
