// Mantel: a self-hosted ambient dashboard with buttons. It collects data,
// lays out pages, runs server-side actions and serves the animated page to
// any screen. Keeping a Google Nest Hub on it is Mantel Cast's job
// (github.com/okchum/mantel-cast), which runs inside the LAN.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/okchum/mantel/internal/action"
	"github.com/okchum/mantel/internal/alert"
	"github.com/okchum/mantel/internal/collect"
	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/dash"
	"github.com/okchum/mantel/internal/plans"
	"github.com/okchum/mantel/internal/state"
	"github.com/okchum/mantel/web"
)

var version = "dev"

const usage = `mantel %s — ambient dashboard with buttons

Usage:
  mantel [run] [-config config.json] [-demo]  serve the dashboard
  mantel layout [-config f] [-templates]       sketch every page's grid, or list templates
  mantel token                                 print a new random access token
  mantel alert-test [-config f]                send a test notification (Bark)
  mantel plans [-config f] [-reset key]        read subscription usage through CLIProxyAPI
                                               (or spend one saved reset)

Keeping a Google Nest Hub on the page is Mantel Cast's job (github.com/okchum/mantel-cast).
`

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "run":
		err = run(ctx, args)
	case "layout":
		err = layoutCmd(args)
	case "token":
		fmt.Println(newToken())
	case "alert-test":
		err = alertTest(ctx, args)
	case "plans":
		err = plansCmd(ctx, args)
	case "keep", "discover", "status", "cast", "stop":
		hint := "mantel-cast " + cmd
		if cmd == "keep" {
			hint = "mantel-cast"
		}
		fmt.Fprintf(os.Stderr, "%q moved to Mantel Cast: run `%s` (github.com/okchum/mantel-cast)\n", cmd, hint)
		os.Exit(2)
	case "version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Printf(usage, version)
	default:
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// layoutCmd prints each page as a character grid so a layout can be checked
// (and auto placement seen) before it reaches the screen.
func layoutCmd(args []string) error {
	fs := flag.NewFlagSet("layout", flag.ExitOnError)
	cfgPath := fs.String("config", "config.json", "config file")
	tpl := fs.Bool("templates", false, "list the page templates and their slots")
	_ = fs.Parse(args)
	draw := func(title string, ws []config.Widget, cols, rows int) {
		lines, labels := config.Sketch(ws, cols, rows)
		fmt.Println(title)
		for i, l := range lines {
			side := ""
			if i < len(labels) {
				side = "   " + labels[i]
			}
			fmt.Println("  " + l + side)
		}
		for _, l := range labels[min(len(labels), len(lines)):] {
			fmt.Println("  " + strings.Repeat(" ", cols) + "   " + l)
		}
		fmt.Println()
	}
	if *tpl {
		for _, t := range config.Templates {
			ws := make([]config.Widget, len(t.Slots))
			for i, s := range t.Slots {
				ws[i] = config.Widget{Type: s.Name, At: s.At[:]}
			}
			draw(fmt.Sprintf("%s — %s", t.Name, t.Title), ws, 12, 8)
		}
		return nil
	}
	cfg, err := loadConfig(*cfgPath, false)
	if err != nil {
		return err
	}
	pages, note := cfg.Pages, ""
	if len(pages) == 0 {
		pages, note = dash.ClassicPages(cfg), "（没写 pages，用经典布局）"
		if cfg.Demo {
			pages, note = dash.DemoPages(), "（演示模式的页面）"
		}
	}
	cols, rows := cfg.Display.Cols, cfg.Display.Rows
	if cols == 0 {
		cols, rows = 12, 8
	}
	fmt.Printf("%d 页，网格 %d×%d %s\n\n", len(pages), cols, rows, note)
	for _, p := range pages {
		title := fmt.Sprintf("%s「%s」", p.Name, p.Title)
		if p.Template != "" {
			title += " template=" + p.Template
		}
		if p.Auto {
			title += " auto"
		}
		draw(title, p.Widgets, cols, rows)
	}
	return nil
}

func loadConfig(path string, demo bool) (*config.Config, error) {
	if path == "" {
		path = "config.json"
	}
	cfg, err := config.Load(path)
	if errors.Is(err, os.ErrNotExist) && demo {
		cfg = &config.Config{Demo: true, Title: "Home", Listen: ":8080"}
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	cfg.Demo = cfg.Demo || demo
	return cfg, nil
}

// actionGrace is how long shutdown waits for cancelled actions to wind down
// (a killed command gets exec's WaitDelay of 2s to release its pipes).
const actionGrace = 5 * time.Second

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", "config.json", "config file")
	demo := fs.Bool("demo", false, "fill the dashboard with generated data")
	_ = fs.Bool("no-cast", false, "ignored (casting is Mantel Cast's job now)")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*cfgPath, *demo)
	if err != nil {
		return err
	}
	loc := time.Local
	tzName := cfg.Timezone
	if tzName != "" {
		if loc, err = time.LoadLocation(tzName); err != nil {
			return fmt.Errorf("timezone: %w", err)
		}
	} else {
		tzName = systemTZ()
	}

	var names [3][]string
	for _, s := range cfg.Servers {
		names[0] = append(names[0], s.Name)
	}
	for _, p := range cfg.Probes {
		names[1] = append(names[1], p.Name)
	}
	for _, q := range cfg.Quotas {
		names[2] = append(names[2], q.Name)
	}
	var sources []string
	for _, s := range cfg.Sources {
		sources = append(sources, s.Name)
	}
	if cfg.Demo {
		sources = append(sources, config.DemoSources...)
	} else if cfg.Plans != nil {
		sources = append(sources, config.PlanSource)
	}
	st := state.NewStore(cfg.Title, tzName, names[0], names[1], names[2], sources)
	d := dash.New(cfg)
	d.Backfill = cfg.Demo

	// Restore before any collector writes, so histories continue where they stopped.
	// Demo data is never saved: it would leak into the real dashboard later.
	// Autosave runs on its own context, ended only after running actions have
	// finished, so their results make it into the final save.
	saveCtx, stopSave := context.WithCancel(context.Background())
	defer stopSave()
	saved := make(chan struct{})
	if cfg.StateFile != "" && !cfg.Demo {
		n, err := st.Restore(cfg.StateFile, 6*time.Hour)
		if err != nil {
			log.Printf("state: %v (starting empty)", err)
		} else if n > 0 {
			log.Printf("state: restored %d entries from %s", n, cfg.StateFile)
		}
		go st.Autosave(saveCtx, cfg.StateFile, time.Minute, saved)
	} else {
		close(saved)
	}

	if a := cfg.Alerts; a != nil && a.Bark != nil {
		if cfg.Demo {
			log.Printf("alerts: off in demo mode")
		} else {
			e := &alert.Engine{
				Store: st, Notifier: &alert.Bark{Cfg: *a.Bark, URL: a.URL}, After: a.After.Or(2 * time.Minute),
				Servers: cfg.Servers, Probes: cfg.Probes, Quotas: cfg.Quotas, Sources: cfg.Sources, Plans: cfg.Plans,
				Maintenance: a.Maintenance, Loc: loc,
			}
			go e.Run(ctx)
			log.Printf("alerts: Bark on, reporting after %s down", e.After)
			if len(a.Maintenance) > 0 {
				log.Printf("alerts: no outage reports during %s", strings.Join(a.Maintenance, ", "))
			}
		}
	}

	go d.Historian(ctx, st, 10*time.Second)
	for _, src := range cfg.Sources {
		if src.Type != "push" {
			go collect.Source(ctx, st, src)
		}
	}
	var planSvc *plans.Service
	if cfg.Demo {
		go collect.Demo(ctx, st, loc)
		go collect.DemoSources(ctx, st)
	} else {
		if p := cfg.Plans; p != nil {
			if planSvc, err = newPlans(p); err != nil {
				return err
			}
			go collect.Plans(ctx, st, planSvc, p.Interval.Or(2*time.Minute))
			log.Printf("plans: reading subscriptions through %s", p.CLIProxy.URL)
		}
		for i, s := range cfg.Servers {
			go collect.NodeExporter(ctx, st, i, s, cfg.Interval.Or(5*time.Second))
		}
		for i, p := range cfg.Probes {
			go collect.Probe(ctx, st, i, p)
		}
		for i, q := range cfg.Quotas {
			go collect.Quota(ctx, st, i, q)
		}
		if len(cfg.Calendars) > 0 {
			go collect.Calendars(ctx, st, cfg.Calendars, loc, 10*time.Minute)
		}
		if cfg.Weather != nil {
			go collect.Weather(ctx, st, *cfg.Weather, tzName)
		}
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", noCache(http.FileServerFS(web.FS)))
	// Actions run under ctx: shutdown cancels them (killing commands) and then
	// waits briefly below so no child process outlives Mantel.
	runner := &action.Runner{Store: st, Actions: d.Actions(), Kick: collect.Kick, Demo: collect.DemoAction(st), Context: ctx}
	if planSvc != nil {
		runner.Plan = planSvc.Reset
	}
	if a := cfg.Alerts; a != nil && a.Bark != nil && !cfg.Demo {
		b := &alert.Bark{Cfg: *a.Bark, URL: a.URL}
		runner.Notify = func(title, body string) {
			nctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if err := b.Notify(nctx, alert.Message{Title: title, Body: body, Level: "active"}); err != nil {
				log.Printf("action: notify: %v", err)
			}
		}
	}
	(&api{cfg: cfg, st: st, dash: d, runner: runner}).register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok\n")) })

	g := newGate(cfg)
	if len(g.tokens) == 0 {
		extra := "none"
		if len(g.hosts) > 0 {
			extra = strings.Join(g.hosts, ", ")
		}
		log.Printf("WARNING access: open (no tokens configured) — anyone who reaches this port can view, and LAN/this machine can run actions; fine on a home LAN, never on the internet")
		log.Printf("WARNING access: open mode only answers Host names that are IP addresses, localhost, single-label or .local/.lan/.home.arpa/.internal names, plus public_url/allowed_hosts: %s", extra)
	} else {
		var ns []string
		for _, t := range g.tokens {
			ns = append(ns, t.Name)
		}
		log.Printf("access: token required (%s)", strings.Join(ns, ", "))
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           g.wrap(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second, // request bodies are small (push ≤ 1 MB)
		// Dashboard, state and image replies; the action handler extends its own
		// deadline to the action's timeout (see api.action).
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	if cfg.TLSCert != "" {
		log.Printf("serving on https://%s/ (demo=%v)", ln.Addr(), cfg.Demo)
		err = srv.ServeTLS(ln, cfg.TLSCert, cfg.TLSKey)
	} else {
		log.Printf("serving on http://%s/ (demo=%v)", ln.Addr(), cfg.Demo)
		err = srv.Serve(ln)
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	// Also when Serve failed (e.g. a bad certificate): finish running
	// actions and the final state save before returning the error.
	if !runner.Shutdown(actionGrace) {
		log.Printf("action: some actions were still running after %s", actionGrace)
	}
	stopSave()
	<-saved // let the final state save finish
	return err
}

// alertTest sends one notification through the configured channel.
func alertTest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("alert-test", flag.ExitOnError)
	cfgPath := fs.String("config", "config.json", "config file")
	_ = fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if cfg.Alerts == nil || cfg.Alerts.Bark == nil {
		return errors.New(`no "alerts": {"bark": {...}} in the config`)
	}
	b := &alert.Bark{Cfg: *cfg.Alerts.Bark, URL: cfg.Alerts.URL}
	if err := b.Notify(ctx, alert.Message{Title: cfg.Title + " 测试", Body: "收到这条，说明告警通道是通的。", Level: "active"}); err != nil {
		return err
	}
	fmt.Println("sent")
	return nil
}

func newPlans(p *config.Plans) (*plans.Service, error) {
	tlsConf, err := plans.TLSConfig(p.CLIProxy.CAFile, p.CLIProxy.Insecure)
	if err != nil {
		return nil, fmt.Errorf("plans: cliproxy.ca_file: %w", err)
	}
	if p.CLIProxy.Insecure {
		log.Printf("plans: WARNING cliproxy.insecure is on: the management key goes to %s without TLS verification; prefer cliproxy.ca_file", p.CLIProxy.URL)
	}
	return &plans.Service{Backend: plans.NewCPA(p.CLIProxy.URL, p.CLIProxy.Key, tlsConf), Only: p.Accounts}, nil
}

// plansCmd prints what the plans page would show, to check the CLIProxyAPI
// link without a screen; -reset spends one saved reset on that account.
func plansCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plans", flag.ExitOnError)
	cfgPath := fs.String("config", "config.json", "config file")
	reset := fs.String("reset", "", "account key to reset (as printed), e.g. codex:codex-me.json")
	asJSON := fs.Bool("json", false, "print raw JSON")
	_ = fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if cfg.Plans == nil {
		return errors.New(`no "plans": {"cliproxy": {...}} in the config`)
	}
	svc, err := newPlans(cfg.Plans)
	if err != nil {
		return err
	}
	accts, err := svc.Refresh(ctx)
	if err != nil {
		return err
	}
	if *reset != "" {
		msg, err := svc.Reset(ctx, *reset)
		if err != nil {
			return err
		}
		fmt.Println(msg)
		return nil
	}
	if *asJSON {
		e := json.NewEncoder(os.Stdout)
		e.SetIndent("", "  ")
		return e.Encode(accts)
	}
	if len(accts) == 0 {
		fmt.Println("no Claude or Codex logins in CLIProxyAPI")
	}
	for _, a := range accts {
		fmt.Printf("%s  %s %s %s\n", a.Key, a.Label, a.Plan, a.Email)
		if a.Error != "" {
			fmt.Printf("    error: %s\n", a.Error)
		}
		for _, w := range a.Windows {
			when := ""
			if w.ResetsAt != nil {
				when = "  resets " + w.ResetsAt.Local().Format("Jan 2 15:04")
			}
			fmt.Printf("    %-10s %5.1f%%%s\n", w.Label, w.Used, when)
		}
		if r := a.Resets; r != nil {
			line := fmt.Sprintf("    resets: %d saved", r.Available)
			if r.NextExpiry != nil {
				line += ", next expires " + r.NextExpiry.Local().Format("Jan 2 15:04")
			}
			if r.Usable {
				line += ", usable now"
			} else if r.Blocker != "" {
				line += " (" + r.Blocker + ")"
			}
			fmt.Println(line)
		}
	}
	return nil
}

// systemTZ returns the host's IANA zone name so the page renders in the
// server's zone even if the device is set differently; "" if unknown.
func systemTZ() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return strings.TrimPrefix(tz, ":")
	}
	if l := time.Local.String(); l != "Local" {
		return l
	}
	if p, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(p, "zoneinfo/"); ok {
			return name
		}
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/fonts/") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		h.ServeHTTP(w, r)
	})
}
