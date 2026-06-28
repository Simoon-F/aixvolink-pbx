// Package sdp adapts SIP audio offer/answer bodies to bounded media types.
package sdp

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	pionsdp "github.com/pion/sdp/v3"
)

const (
	clockRateG711 = 8000
	defaultPTime  = 20
)

var (
	// ErrNoAudio indicates that SDP contains no usable audio description.
	ErrNoAudio = errors.New("SDP has no supported audio media")
	// ErrNoCommonCodec indicates that the two legs share no G.711 codec.
	ErrNoCommonCodec = errors.New("SDP has no common G.711 codec")
)

// Direction is the RFC 3264 media direction.
type Direction string

const (
	DirectionSendRecv Direction = "sendrecv"
	DirectionSendOnly Direction = "sendonly"
	DirectionRecvOnly Direction = "recvonly"
	DirectionInactive Direction = "inactive"
)

// Codec is one supported RTP payload mapping.
type Codec struct {
	Name        string
	PayloadType uint8
	ClockRate   uint32
}

// Endpoint is the remote RTP/RTCP and codec information from one SDP body.
type Endpoint struct {
	RTP       netip.AddrPort
	RTCP      netip.AddrPort
	Direction Direction
	Codecs    []Codec
}

// Mapping correlates payload types on the caller and callee legs.
type Mapping struct {
	Name         string
	ClockRate    uint32
	CallerPT     uint8
	CalleePT     uint8
	CallerDTMFPT uint8
	CalleeDTMFPT uint8
	HasDTMF      bool
}

// ParseAudio validates and extracts one plain RTP/AVP audio description.
func ParseAudio(body []byte) (Endpoint, error) {
	var description pionsdp.SessionDescription
	if err := description.Unmarshal(body); err != nil {
		return Endpoint{}, fmt.Errorf("parse SDP: %w", err)
	}
	media := audioDescription(&description)
	if media == nil || media.MediaName.Port.Value <= 0 || media.MediaName.Port.Value > 65535 {
		return Endpoint{}, ErrNoAudio
	}
	if strings.Join(media.MediaName.Protos, "/") != "RTP/AVP" {
		return Endpoint{}, fmt.Errorf("unsupported audio profile %q", strings.Join(media.MediaName.Protos, "/"))
	}
	addressText := ""
	if media.ConnectionInformation != nil && media.ConnectionInformation.Address != nil {
		addressText = media.ConnectionInformation.Address.Address
	} else if description.ConnectionInformation != nil && description.ConnectionInformation.Address != nil {
		addressText = description.ConnectionInformation.Address.Address
	}
	address, err := netip.ParseAddr(addressText)
	if err != nil || address.IsUnspecified() {
		return Endpoint{}, fmt.Errorf("invalid SDP connection address %q", addressText)
	}
	rtpPort := uint16(media.MediaName.Port.Value)
	rtcpPort := rtpPort + 1
	if value, exists := media.Attribute("rtcp"); exists {
		fields := strings.Fields(value)
		if len(fields) == 0 {
			return Endpoint{}, fmt.Errorf("invalid SDP RTCP port %q", value)
		}
		parsed, parseErr := strconv.ParseUint(fields[0], 10, 16)
		if parseErr != nil || parsed == 0 {
			return Endpoint{}, fmt.Errorf("invalid SDP RTCP port %q", value)
		}
		rtcpPort = uint16(parsed)
	}
	codecs, err := parseCodecs(media)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{
		RTP: netip.AddrPortFrom(address, rtpPort), RTCP: netip.AddrPortFrom(address, rtcpPort),
		Direction: mediaDirection(&description, media), Codecs: codecs,
	}, nil
}

func audioDescription(description *pionsdp.SessionDescription) *pionsdp.MediaDescription {
	for _, media := range description.MediaDescriptions {
		if media.MediaName.Media == "audio" {
			return media
		}
	}
	return nil
}

func parseCodecs(media *pionsdp.MediaDescription) ([]Codec, error) {
	rtpMaps := make(map[uint8]Codec, len(media.Attributes))
	for _, attribute := range media.Attributes {
		if attribute.Key != "rtpmap" {
			continue
		}
		fields := strings.Fields(attribute.Value)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid SDP rtpmap %q", attribute.Value)
		}
		payload, err := strconv.ParseUint(fields[0], 10, 7)
		if err != nil {
			return nil, fmt.Errorf("invalid SDP payload type %q", fields[0])
		}
		encoding := strings.Split(fields[1], "/")
		if len(encoding) < 2 {
			return nil, fmt.Errorf("invalid SDP encoding %q", fields[1])
		}
		clockRate, err := strconv.ParseUint(encoding[1], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid SDP clock rate %q", encoding[1])
		}
		rtpMaps[uint8(payload)] = Codec{Name: strings.ToUpper(encoding[0]), PayloadType: uint8(payload), ClockRate: uint32(clockRate)}
	}
	codecs := make([]Codec, 0, len(media.MediaName.Formats))
	for _, format := range media.MediaName.Formats {
		payload, err := strconv.ParseUint(format, 10, 7)
		if err != nil {
			continue
		}
		payloadType := uint8(payload)
		codec, exists := rtpMaps[payloadType]
		if !exists {
			switch payloadType {
			case 0:
				codec = Codec{Name: "PCMU", PayloadType: 0, ClockRate: clockRateG711}
			case 8:
				codec = Codec{Name: "PCMA", PayloadType: 8, ClockRate: clockRateG711}
			default:
				continue
			}
		}
		if isSupported(codec) {
			codecs = append(codecs, codec)
		}
	}
	if !hasAudioCodec(codecs) {
		return nil, ErrNoAudio
	}
	return codecs, nil
}

func isSupported(codec Codec) bool {
	return codec.ClockRate == clockRateG711 && (codec.Name == "PCMU" || codec.Name == "PCMA" || codec.Name == "TELEPHONE-EVENT")
}

func hasAudioCodec(codecs []Codec) bool {
	for _, codec := range codecs {
		if codec.Name == "PCMU" || codec.Name == "PCMA" {
			return true
		}
	}
	return false
}

func mediaDirection(description *pionsdp.SessionDescription, media *pionsdp.MediaDescription) Direction {
	for _, direction := range []Direction{DirectionInactive, DirectionSendOnly, DirectionRecvOnly, DirectionSendRecv} {
		if _, exists := media.Attribute(string(direction)); exists {
			return direction
		}
		if _, exists := description.Attribute(string(direction)); exists {
			return direction
		}
	}
	return DirectionSendRecv
}

// Negotiate selects the answer's first G.711 codec that appeared in the offer.
func Negotiate(offer, answer Endpoint) (Mapping, error) {
	for _, answered := range answer.Codecs {
		if answered.Name != "PCMU" && answered.Name != "PCMA" {
			continue
		}
		for _, offered := range offer.Codecs {
			if offered.Name != answered.Name || offered.ClockRate != answered.ClockRate {
				continue
			}
			mapping := Mapping{
				Name: answered.Name, ClockRate: answered.ClockRate,
				CallerPT: offered.PayloadType, CalleePT: answered.PayloadType,
			}
			callerDTMF, callerOK := findCodec(offer.Codecs, "TELEPHONE-EVENT")
			calleeDTMF, calleeOK := findCodec(answer.Codecs, "TELEPHONE-EVENT")
			if callerOK && calleeOK {
				mapping.HasDTMF = true
				mapping.CallerDTMFPT = callerDTMF.PayloadType
				mapping.CalleeDTMFPT = calleeDTMF.PayloadType
			}
			return mapping, nil
		}
	}
	return Mapping{}, ErrNoCommonCodec
}

func findCodec(codecs []Codec, name string) (Codec, bool) {
	for _, codec := range codecs {
		if codec.Name == name {
			return codec, true
		}
	}
	return Codec{}, false
}

// BuildAudio creates one plain RTP/AVP SDP body for the supplied leg.
func BuildAudio(localRTP, localRTCP netip.AddrPort, codecs []Codec, direction Direction) ([]byte, error) {
	if !localRTP.IsValid() || !localRTCP.IsValid() || localRTP.Addr() != localRTCP.Addr() {
		return nil, fmt.Errorf("local RTP and RTCP addresses are invalid")
	}
	if !hasAudioCodec(codecs) {
		return nil, ErrNoAudio
	}
	if direction == "" {
		direction = DirectionSendRecv
	}
	formats := make([]string, 0, len(codecs))
	attributes := make([]pionsdp.Attribute, 0, len(codecs)+3)
	for _, codec := range codecs {
		if !isSupported(codec) {
			continue
		}
		formats = append(formats, strconv.Itoa(int(codec.PayloadType)))
		attributes = append(attributes, pionsdp.NewAttribute("rtpmap", fmt.Sprintf("%d %s/%d", codec.PayloadType, codec.Name, codec.ClockRate)))
		if codec.Name == "TELEPHONE-EVENT" {
			attributes = append(attributes, pionsdp.NewAttribute("fmtp", fmt.Sprintf("%d 0-16", codec.PayloadType)))
		}
	}
	attributes = append(attributes,
		pionsdp.NewAttribute("rtcp", strconv.Itoa(int(localRTCP.Port()))),
		pionsdp.NewPropertyAttribute(string(direction)),
		pionsdp.NewAttribute("ptime", strconv.Itoa(defaultPTime)),
	)
	addressType := "IP4"
	if localRTP.Addr().Is6() {
		addressType = "IP6"
	}
	sessionID := uint64(time.Now().UTC().UnixNano())
	description := pionsdp.SessionDescription{
		Version:               0,
		Origin:                pionsdp.Origin{Username: "aixvolinkpbx", SessionID: sessionID, SessionVersion: sessionID, NetworkType: "IN", AddressType: addressType, UnicastAddress: localRTP.Addr().String()},
		SessionName:           "AixvoLinkPBX",
		ConnectionInformation: &pionsdp.ConnectionInformation{NetworkType: "IN", AddressType: addressType, Address: &pionsdp.Address{Address: localRTP.Addr().String()}},
		TimeDescriptions:      []pionsdp.TimeDescription{{Timing: pionsdp.Timing{StartTime: 0, StopTime: 0}}},
		MediaDescriptions: []*pionsdp.MediaDescription{{
			MediaName:  pionsdp.MediaName{Media: "audio", Port: pionsdp.RangedPort{Value: int(localRTP.Port())}, Protos: []string{"RTP", "AVP"}, Formats: formats},
			Attributes: attributes,
		}},
	}
	body, err := description.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshal SDP: %w", err)
	}
	return body, nil
}

// CodecListForAnswer returns the caller-facing selected codec and optional DTMF mapping.
func CodecListForAnswer(mapping Mapping) []Codec {
	codecs := []Codec{{Name: mapping.Name, PayloadType: mapping.CallerPT, ClockRate: mapping.ClockRate}}
	if mapping.HasDTMF {
		codecs = append(codecs, Codec{Name: "TELEPHONE-EVENT", PayloadType: mapping.CallerDTMFPT, ClockRate: clockRateG711})
	}
	return codecs
}

// CodecListForOffer filters a parsed offer to supported G.711 and telephone-event mappings.
func CodecListForOffer(endpoint Endpoint) []Codec {
	return append([]Codec(nil), endpoint.Codecs...)
}
