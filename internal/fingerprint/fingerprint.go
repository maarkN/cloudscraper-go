// Package fingerprint holds the browser profiles cloudscraper-go can impersonate:
// the uTLS ClientHello that shapes the TLS/JA3 fingerprint, plus the matching
// User-Agent and default request headers.
//
// Header *order* is not yet reproduced on the wire (net/http reorders them); the
// TLS ClientHello is the fingerprint that anti-bot vendors key on most, and that
// is what these profiles get right. See the README "Limitations" section.
package fingerprint

import (
	"fmt"
	"sort"

	utls "github.com/refraction-networking/utls"
)

// Header is a single default request header. A slice is used (rather than a map)
// to record the intended browser order for when wire-order support lands.
type Header struct {
	Name  string
	Value string
}

// Profile is one impersonated browser.
type Profile struct {
	// Name is the key used to select the profile (e.g. "chrome").
	Name string
	// ClientHelloID is the uTLS preset that drives the TLS fingerprint.
	ClientHelloID utls.ClientHelloID
	// Headers are the default request headers, in browser order.
	Headers []Header
}

var registry = map[string]Profile{
	"chrome": {
		Name:          "chrome",
		ClientHelloID: utls.HelloChrome_Auto,
		Headers: []Header{
			{"sec-ch-ua", `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`},
			{"sec-ch-ua-mobile", "?0"},
			{"sec-ch-ua-platform", `"macOS"`},
			{"Upgrade-Insecure-Requests", "1"},
			{"User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"},
			{"Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
			{"Sec-Fetch-Site", "none"},
			{"Sec-Fetch-Mode", "navigate"},
			{"Sec-Fetch-User", "?1"},
			{"Sec-Fetch-Dest", "document"},
			{"Accept-Encoding", "gzip, deflate"},
			{"Accept-Language", "en-US,en;q=0.9"},
		},
	},
	"firefox": {
		Name:          "firefox",
		ClientHelloID: utls.HelloFirefox_Auto,
		Headers: []Header{
			{"User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:133.0) Gecko/20100101 Firefox/133.0"},
			{"Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/png,image/svg+xml,*/*;q=0.8"},
			{"Accept-Language", "en-US,en;q=0.5"},
			{"Accept-Encoding", "gzip, deflate"},
			{"Upgrade-Insecure-Requests", "1"},
			{"Sec-Fetch-Dest", "document"},
			{"Sec-Fetch-Mode", "navigate"},
			{"Sec-Fetch-Site", "none"},
			{"Sec-Fetch-User", "?1"},
		},
	},
}

// DefaultProfile is used when none is requested.
const DefaultProfile = "chrome"

// Get returns the named profile, or an error listing the valid names.
func Get(name string) (Profile, error) {
	if name == "" {
		name = DefaultProfile
	}
	p, ok := registry[name]
	if !ok {
		return Profile{}, fmt.Errorf("unknown fingerprint profile %q (available: %v)", name, Names())
	}
	return p, nil
}

// Names lists the available profile names, sorted.
func Names() []string {
	names := make([]string, 0, len(registry))
	for k := range registry {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
