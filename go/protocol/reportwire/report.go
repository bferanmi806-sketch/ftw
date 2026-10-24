// Package reportwire is how the box's anonymous daily reports reach FTW
// Cloud: each report alone, as one padded inner request sealed with Oblivious
// HTTP (package ohttp) and posted to an independent relay. The gateway reads
// the report's schema and hands it to that report's decoder: the fleet report
// (package fleetwire) or the energy report (package energywire).
//
// Two reports, never one message. With a fleet of a few boxes a driver set
// and a price zone can point at one home, so energy sent beside them would
// belong to that home again. Each report leaves at its own random time.
package reportwire

import (
	"encoding/json"

	"github.com/srcfl/ftw/go/protocol/ohttp"
)

// The one inner request. The gateway answers no other.
const (
	Method    = "POST"
	Scheme    = "https"
	Authority = "cloud.ftw.energy"
	Path      = "/v1/report"
)

const (
	// RequestSize is every padded inner request and ResponseSize every padded
	// inner response, so the relay sees one length whatever the report, its
	// kind or the answer. The largest fleet report fits.
	RequestSize  = 1536
	ResponseSize = 16
)

// Request is the padded inner request carrying one report body. ok is false
// when the body does not fit, so a report never goes out at a length of its
// own.
func Request(body []byte) (msg []byte, ok bool) {
	msg = ohttp.Request{Method: Method, Scheme: Scheme, Authority: Authority, Path: Path, Content: body}.Marshal(RequestSize)
	return msg, len(msg) == RequestSize
}

// Body opens an inner request and returns the report it carries, or false
// for anything but the one padded request.
func Body(msg []byte) ([]byte, bool) {
	req, err := ohttp.ParseRequest(msg)
	if err != nil || len(msg) != RequestSize || req.Method != Method || req.Scheme != Scheme ||
		req.Authority != Authority || req.Path != Path || len(req.Fields) != 0 {
		return nil, false
	}
	return req.Content, true
}

// Schema reads only a report's schema, to choose its decoder. The decoder
// still checks every byte.
func Schema(body []byte) string {
	var head struct {
		Schema string `json:"schema"`
	}
	if json.Unmarshal(body, &head) != nil {
		return ""
	}
	return head.Schema
}

// Response is the padded inner answer.
func Response(status int) []byte {
	return ohttp.Response{Status: status}.Marshal(ResponseSize)
}
