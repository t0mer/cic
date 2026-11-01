// Package geocode resolves device coordinates to place names with the latlng.work reverse
// geocoding API. All lookups run in one background Worker so the MQTT callback and HTTP
// handlers never wait on the network; a Guard keeps the tight provider quota in check and a
// SQLite cache makes repeated places free.
package geocode
