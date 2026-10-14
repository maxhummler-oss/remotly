package server

import (
	"context"
	"errors"
	"net"
)

// wake sends Wake-on-LAN magic packets for the given MAC addresses to the
// limited broadcast address and to the directed broadcast of every local
// IPv4 network (or only the one containing lastIP, if known).
func wake(macs []string, lastIP string) error {
	if len(macs) == 0 {
		return errors.New("keine MAC-Adresse bekannt – Agent muss einmal online gewesen sein")
	}
	targets := []string{"255.255.255.255"}
	if ifaces, err := net.Interfaces(); err == nil {
		ip := net.ParseIP(lastIP)
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, _ := iface.Addrs()
			for _, a := range addrs {
				ipn, ok := a.(*net.IPNet)
				if !ok || ipn.IP.To4() == nil {
					continue
				}
				if ip != nil && !ipn.Contains(ip) {
					continue
				}
				bc := make(net.IP, 4)
				for i := range bc {
					bc[i] = ipn.IP.To4()[i] | ^ipn.Mask[len(ipn.Mask)-4+i]
				}
				targets = append(targets, bc.String())
			}
		}
	}

	// Broadcasting requires SO_BROADCAST on the socket.
	lc := net.ListenConfig{Control: setBroadcast}
	pc, err := lc.ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		return err
	}
	defer pc.Close()

	var sent bool
	var lastErr error
	for _, m := range macs {
		hw, err := net.ParseMAC(m)
		if err != nil || len(hw) != 6 {
			continue
		}
		packet := make([]byte, 0, 102)
		for i := 0; i < 6; i++ {
			packet = append(packet, 0xff)
		}
		for i := 0; i < 16; i++ {
			packet = append(packet, hw...)
		}
		for _, t := range targets {
			for _, port := range []int{9, 7} {
				if _, err := pc.WriteTo(packet, &net.UDPAddr{IP: net.ParseIP(t), Port: port}); err == nil {
					sent = true
				} else {
					lastErr = err
				}
			}
		}
	}
	if !sent {
		if lastErr == nil {
			lastErr = errors.New("keine gültige MAC-Adresse")
		}
		return lastErr
	}
	return nil
}
