// clipboard-sync is the desktop hub for the Android prototype.
package main

import (
	"BufferSync/internal/clipsync"
	"BufferSync/internal/systemclipboard"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"
)

func main() {
	listen := flag.String("listen", ":7778", "address to listen on")
	host := flag.String("host", "", "IP to show in the pairing link (optional)")
	memory := flag.Bool("memory", false, "share an in-memory clipboard only; do not access the desktop clipboard")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var clipboard clipsync.Clipboard
	if !*memory {
		var err error
		clipboard, err = systemclipboard.New()
		if err != nil {
			log.Fatal(err)
		}
	}
	certificate, secret, pin, err := clipsync.Identity()
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	hub := clipsync.New(secret, clipboard)
	server := &http.Server{Handler: hub, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 35 * time.Second, IdleTimeout: 40 * time.Second, MaxHeaderBytes: 8192,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}}
	port := listener.Addr().(*net.TCPAddr).Port
	hosts := []string{*host}
	if *host == "" {
		hosts = nil
		addresses, _ := net.InterfaceAddrs()
		for _, address := range addresses {
			if network, ok := address.(*net.IPNet); ok && network.IP.To4() != nil && !network.IP.IsLoopback() {
				hosts = append(hosts, network.IP.String())
			}
		}
		if len(hosts) == 0 {
			hosts = []string{"127.0.0.1"}
		}
	}
	fmt.Println("BufferSync: текст и ссылки. Телефон и компьютер должны быть в одной сети.")
	if *memory {
		fmt.Println("Режим --memory: системный буфер компьютера НЕ используется.")
	}
	fmt.Println("Вставьте одну из ссылок в Android-приложение. Ссылка секретная; не публикуйте её:")
	for _, address := range hosts {
		fmt.Printf("buffersync://%s?key=%s&pin=%s\n", net.JoinHostPort(address, fmt.Sprint(port)), secret, pin)
	}
	fmt.Println("Ctrl+C — остановить. После перезапуска потребуется новая ссылка.")
	if clipboard != nil {
		// Do not upload the pre-existing clipboard until an OS change is observed.
		go func() {
			err := systemclipboard.Watch(ctx, func() {
				if err := hub.Capture(); err != nil {
					log.Printf("Не удалось прочитать буфер: %v", err)
				}
			})
			if err != nil && ctx.Err() == nil {
				log.Printf("Наблюдение остановлено: %v; перезапустите программу", err)
			}
		}()
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		_ = server.Close()
	}()
	err = server.Serve(tls.NewListener(listener, server.TLSConfig))
	if err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
