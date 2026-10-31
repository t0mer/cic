// Package mqtt ingests DRADIS telemetry from a single MQTT broker.
package mqtt

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// SegmentPattern is the regular expression a prefix or device topic segment must match.
const SegmentPattern = `^[A-Za-z0-9_-]{1,64}$`

var segmentRe = regexp.MustCompile(SegmentPattern)

// Telemetry leaves subscribed to under each <prefix>/<device>.
const (
	LeafStatus     = "status"
	LeafVersion    = "version"
	LeafBattery    = "battery"
	LeafWifi       = "wifi"
	LeafLocation   = "location"
	LeafDeviceInfo = "device_info"
	LeafSMSResult  = "sendsms/result"
)

var leaves = []string{LeafStatus, LeafVersion, LeafBattery, LeafWifi, LeafLocation, LeafDeviceInfo, LeafSMSResult}

// ValidSegment reports whether s is a valid prefix or device topic segment.
func ValidSegment(s string) bool {
	return segmentRe.MatchString(s)
}

// ParseTopic splits a telemetry topic into its prefix, device and leaf. It
// reports ok=false for unknown prefixes, invalid devices, unknown leaves and
// any wildcard or extra segment.
func ParseTopic(topic string, prefixes []string) (prefix, device, leaf string, ok bool) {
	parts := strings.Split(topic, "/")
	if len(parts) < 3 || len(parts) > 4 {
		return "", "", "", false
	}
	if !slices.Contains(prefixes, parts[0]) || !ValidSegment(parts[1]) {
		return "", "", "", false
	}
	leaf = strings.Join(parts[2:], "/")
	if !slices.Contains(leaves, leaf) {
		return "", "", "", false
	}
	return parts[0], parts[1], leaf, true
}

// SubscribeFilters returns the topic filters to subscribe to for prefixes.
func SubscribeFilters(prefixes []string) []string {
	out := make([]string, 0, len(prefixes)*len(leaves))
	for _, p := range prefixes {
		for _, l := range leaves {
			out = append(out, p+"/+/"+l)
		}
	}
	return out
}

// Builder builds validated command topics.
type Builder struct {
	prefixes []string
}

// NewBuilder returns a Builder that only accepts the given prefixes.
func NewBuilder(prefixes []string) *Builder {
	return &Builder{prefixes: slices.Clone(prefixes)}
}

// Command leaves DRADIS accepts under <prefix>/<device>.
const (
	CmdPing        = "ping"
	CmdNotify      = "notify"
	CmdSendSMS     = "sendsms"
	CmdGetStatus   = "getstatus"
	CmdGetLocation = "getlocation"
)

var commandLeaves = []string{CmdPing, CmdNotify, CmdSendSMS, CmdGetStatus, CmdGetLocation}

// Command returns the topic <prefix>/<device>/<leaf> after validating every
// segment. Only the known command leaves are accepted.
func (b *Builder) Command(prefix, device, leaf string) (string, error) {
	if !slices.Contains(b.prefixes, prefix) {
		return "", fmt.Errorf("unknown prefix %q", prefix)
	}
	if !ValidSegment(device) {
		return "", errors.New("invalid device segment")
	}
	if !slices.Contains(commandLeaves, leaf) {
		return "", fmt.Errorf("unknown command leaf %q", leaf)
	}
	return prefix + "/" + device + "/" + leaf, nil
}

// SplitKey splits a device key "<prefix>/<device>" into its parts, requiring a
// configured prefix and a valid device segment.
func SplitKey(key string, prefixes []string) (prefix, device string, ok bool) {
	prefix, device, found := strings.Cut(key, "/")
	if !found || !slices.Contains(prefixes, prefix) || !ValidSegment(device) {
		return "", "", false
	}
	return prefix, device, true
}
