package transport

import (
	"fmt"
	"net"
)

const (
	ID       = "GANDON"
	port     = 2288
	buffsize = 2048
)

func ListenPacket() {
	pc, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", port))
	if err != nil {
		panic(err)
	}
	defer pc.Close()

	go func() {
		buf := make([]byte, buffsize)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			msg := string(buf[:n])
			if msg != ID {
				continue
			}
			fmt.Println("список устройств", addr.String())
		}
	}()
}

func SendPacket() {

}
