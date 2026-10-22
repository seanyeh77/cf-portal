// Command portal serves the sdc.nycu.club service portal and DNS admin.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"golang.org/x/term"

	"github.com/nycu-sdc/sdc-portal/internal/assets"
	"github.com/nycu-sdc/sdc-portal/internal/auth"
	"github.com/nycu-sdc/sdc-portal/internal/cloudflare"
	"github.com/nycu-sdc/sdc-portal/internal/config"
	"github.com/nycu-sdc/sdc-portal/internal/health"
	"github.com/nycu-sdc/sdc-portal/internal/httpapi"
	"github.com/nycu-sdc/sdc-portal/internal/scanner"
	"github.com/nycu-sdc/sdc-portal/internal/snapshots"
	"github.com/nycu-sdc/sdc-portal/internal/store"
)

const usage = `usage: portal <command>

commands:
  serve    run the web server (default)
  hashpw   generate ADMIN_PASSWORD_HASH
  health   probe /healthz (for container health checks)
`

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		if err := serve(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "hashpw":
		if err := hashpw(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "health":
		addr := os.Getenv("LISTEN_ADDR")
		if addr == "" || strings.HasPrefix(addr, ":") {
			addr = "127.0.0.1" + addr
			if addr == "127.0.0.1" {
				addr += ":8080"
			}
		}
		c := http.Client{Timeout: 3 * time.Second}
		resp, err := c.Get("http://" + addr + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func hashpw() error {
	in := bufio.NewReader(os.Stdin)
	read := func(prompt string) (string, error) {
		fmt.Fprint(os.Stderr, prompt)
		if term.IsTerminal(int(os.Stdin.Fd())) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			return string(b), err
		}
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	pw, err := read("Password: ")
	if err != nil {
		return err
	}
	if len([]rune(pw)) < 12 {
		return errors.New("password must be at least 12 characters")
	}
	again, err := read("Repeat:   ")
	if err != nil {
		return err
	}
	if pw != again {
		return errors.New("passwords do not match")
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

func serve() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database %s: %w", cfg.DBPath, err)
	}
	defer st.Close()

	cf := cloudflare.New(cfg.CFAPIBase, cfg.CFAPIToken, cfg.CFZoneName, cfg.CFZoneID)
	snaps := &snapshots.Store{Dir: st.Dir + "/snapshots", Loc: cfg.Timezone}
	icons := assets.NewFetcher(st.Dir+"/icons", cfg.SimpleIconsBase, cfg.CheckTimeout*2, cfg.CheckVerifyTLS)
	hc := health.New(st, cfg.CheckInterval, cfg.CheckTimeout, cfg.CheckVerifyTLS)
	ports := func(ctx context.Context) []int {
		set, err := st.Settings(ctx, store.DefaultSettings(cfg.PortalTitle))
		if err == nil {
			if p, err := scanner.ParsePorts(set["scan_ports"]); err == nil {
				return p
			}
		}
		return []int{443, 80, 22}
	}
	sc := scanner.New(cf, st, cfg.ScanTimeout, cfg.RecordScanEvery, cfg.ScanDNSCanary, ports)
	mt := &scanner.Maint{CF: cf, Store: st, Snaps: snaps, Loc: cfg.Timezone, Keep: cfg.SnapshotKeep, Interval: cfg.MaintInterval}
	srv := httpapi.New(cfg, cf, st, sc, mt, hc, icons, snaps)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err := cf.ZoneID(ctx); err != nil {
		log.Printf("warning: cannot reach Cloudflare yet: %v", err)
	}
	go hc.Run(ctx)
	go sc.Run(ctx)
	go mt.Run(ctx)

	hs := &http.Server{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second,
		WriteTimeout: 120 * time.Second, IdleTimeout: 120 * time.Second}
	errc := make(chan error, 1)
	go func() {
		log.Printf("listening on %s (zone %s, db %s)", cfg.Listen, cfg.CFZoneName, cfg.DBPath)
		errc <- hs.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Print("shutting down")
	shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return hs.Shutdown(shut)
}
