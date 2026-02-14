package main

import (
	"bufio"
	_ "embed"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

//go:embed zone.txt
var zoneFile string

var (
	registeredDomains   = make(map[string]struct{})
	registeredDomainsMu sync.RWMutex
)

// initializeDNSDomains は DB から登録済みユーザー名を読み込み、DNS 解決対象ドメインを再構築する
func initializeDNSDomains() error {
	var names []string
	if err := dbConn.Select(&names, "SELECT name FROM users"); err != nil {
		return err
	}

	registeredDomainsMu.Lock()
	defer registeredDomainsMu.Unlock()

	registeredDomains = make(map[string]struct{})

	// ゾーンファイルの静的エントリを登録
	scanner := bufio.NewScanner(strings.NewReader(zoneFile))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			registeredDomains[line] = struct{}{}
		}
	}

	// DB のユーザー名を登録
	for _, name := range names {
		registeredDomains[name] = struct{}{}
	}

	return nil
}

// registerDNSDomain はユーザー登録時にドメインを追加する
func registerDNSDomain(name string) {
	registeredDomainsMu.Lock()
	registeredDomains[name] = struct{}{}
	registeredDomainsMu.Unlock()
}

// isDomainRegistered はサブドメインが登録済みかどうかを返す
func isDomainRegistered(fqdn string) bool {
	// "username.t.isucon.pw." → "username" を抽出
	subdomain := strings.TrimSuffix(fqdn, ".t.isucon.pw.")
	if subdomain == fqdn {
		// t.isucon.pw ゾーン外のクエリ
		return false
	}
	// "t.isucon.pw." 自体へのクエリ (subdomain == "")
	if subdomain == "" {
		return true
	}

	registeredDomainsMu.RLock()
	_, ok := registeredDomains[subdomain]
	registeredDomainsMu.RUnlock()
	return ok
}

func startDNSServer() {
	ip := net.ParseIP(powerDNSSubdomainAddress)
	if ip == nil {
		log.Fatalf("invalid ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS: %s", powerDNSSubdomainAddress)
	}

	handler := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Authoritative = true

		for _, q := range r.Question {
			if !isDomainRegistered(q.Name) {
				m.Rcode = dns.RcodeNameError
				continue
			}

			switch q.Qtype {
			case dns.TypeA:
				m.Answer = append(m.Answer, &dns.A{
					Hdr: dns.RR_Header{
						Name:   q.Name,
						Rrtype: dns.TypeA,
						Class:  dns.ClassINET,
						Ttl:    600,
					},
					A: ip,
				})
			case dns.TypeNS:
				m.Answer = append(m.Answer, &dns.NS{
					Hdr: dns.RR_Header{
						Name:   q.Name,
						Rrtype: dns.TypeNS,
						Class:  dns.ClassINET,
						Ttl:    0,
					},
					Ns: "ns1.t.isucon.pw.",
				})
			case dns.TypeSOA:
				m.Answer = append(m.Answer, &dns.SOA{
					Hdr: dns.RR_Header{
						Name:   q.Name,
						Rrtype: dns.TypeSOA,
						Class:  dns.ClassINET,
						Ttl:    0,
					},
					Ns:      "ns1.t.isucon.pw.",
					Mbox:    "hostmaster.t.isucon.pw.",
					Serial:  1,
					Refresh: 10800,
					Retry:   3600,
					Expire:  604800,
					Minttl:  3600,
				})
			}
		}

		// NXDOMAIN レスポンスを 1 秒遅延させる
		if m.Rcode == dns.RcodeNameError {
			time.Sleep(1 * time.Second)
		}

		w.WriteMsg(m)
	})

	server := &dns.Server{
		Addr:    ":53",
		Net:     "udp",
		Handler: handler,
	}

	log.Printf("starting DNS server on :53")
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("failed to start DNS server: %v", err)
	}
}
