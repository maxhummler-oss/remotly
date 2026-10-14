package server

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/pion/turn/v4"
)

const turnRealm = "remotly"

// turnCredentials issues short-lived TURN credentials (the "TURN REST API"
// scheme): username = "<expiry>:<subject>", password = HMAC(secret, username).
// Credentials leaked from a browser expire on their own.
func turnCredentials(secret, subject string, ttl time.Duration) (string, string) {
	user := strconv.FormatInt(time.Now().Add(ttl).Unix(), 10) + ":" + subject
	return user, turnPassword(secret, user)
}

func turnPassword(secret, user string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(user))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// startTURN starts the embedded TURN relay. Peers fall back to it when a
// direct connection is impossible (e.g. thin clients and rack PCs in
// different VLANs) or when the viewer explicitly requests relayed mode.
func startTURN(cfg Config, secret string) (*turn.Server, error) {
	if cfg.TURNPort == 0 {
		return nil, nil
	}
	relayIP := net.ParseIP(cfg.PublicIP)
	if relayIP == nil {
		return nil, fmt.Errorf("invalid publicIp %q", cfg.PublicIP)
	}
	conn, err := net.ListenPacket("udp4", "0.0.0.0:"+strconv.Itoa(cfg.TURNPort))
	if err != nil {
		return nil, fmt.Errorf("turn listen: %w", err)
	}

	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: turnRealm,
		AuthHandler: func(username, realm string, src net.Addr) ([]byte, bool) {
			expiry, _, ok := strings.Cut(username, ":")
			if !ok {
				return nil, false
			}
			ts, err := strconv.ParseInt(expiry, 10, 64)
			if err != nil || time.Now().Unix() > ts {
				return nil, false
			}
			return turn.GenerateAuthKey(username, realm, turnPassword(secret, username)), true
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn: conn,
			RelayAddressGenerator: &turn.RelayAddressGeneratorPortRange{
				RelayAddress: relayIP,
				Address:      "0.0.0.0",
				MinPort:      uint16(cfg.RelayPortMin),
				MaxPort:      uint16(cfg.RelayPortMax),
			},
		}},
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	log.Printf("TURN relay listening on udp/%d (relay %s, ports %d-%d)", cfg.TURNPort, relayIP, cfg.RelayPortMin, cfg.RelayPortMax)
	return srv, nil
}
