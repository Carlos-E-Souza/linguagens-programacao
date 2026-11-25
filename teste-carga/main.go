// Comando teste-carga simula vários usuários trocando mensagens em conversas
// diretas e grupos contra o servidor de chat real, coleta métricas do
// cliente (ACK, entrega, perdas, ordem) e do servidor (pprof) e grava um
// relatório em results/<execução>/report.{json,md}.
//
// Usuários e grupos têm nomes fixos e são reaproveitados entre execuções;
// cada execução só reenvia mensagens. Veja README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/client"
	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/metrics"
	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/profiler"
	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/report"
	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/scenario"
)

// password é a senha fixa de todos os usuários do teste.
const password = "carga-senha-123"

// setupParallelism limita as chamadas REST simultâneas na preparação.
const setupParallelism = 10

type runConfig struct {
	Prefix         string
	ServerURL      string
	PprofURL       string
	Users          int
	DirectPerUser  int
	GroupSizes     []int
	Rate           float64
	Duration       time.Duration
	Ramp           time.Duration
	Drain          time.Duration
	Receipts       bool
	Seed           int64
	SampleInterval time.Duration
	OutDir         string
}

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("teste-carga: ")

	var cfg runConfig
	var groupSizes string
	flag.StringVar(&cfg.Prefix, "prefix", "carga", "prefixo dos usuários e grupos fixos")
	flag.StringVar(&cfg.ServerURL, "server", "http://localhost:8080", "URL HTTP do servidor")
	flag.StringVar(&cfg.PprofURL, "pprof", "http://localhost:6060", "URL do pprof do servidor (vazio desativa)")
	flag.IntVar(&cfg.Users, "users", 100, "usuários simultâneos")
	flag.IntVar(&cfg.DirectPerUser, "direct-per-user", 3, "conversas diretas iniciadas por usuário")
	flag.StringVar(&groupSizes, "group-sizes", "5,5,10,10,20,50", "tamanhos dos grupos, separados por vírgula")
	flag.Float64Var(&cfg.Rate, "rate", 1, "mensagens por segundo por usuário")
	flag.DurationVar(&cfg.Duration, "duration", 60*time.Second, "duração da fase de carga")
	flag.DurationVar(&cfg.Ramp, "ramp", 10*time.Second, "tempo para conectar todos os usuários")
	flag.DurationVar(&cfg.Drain, "drain", 5*time.Second, "espera por ACKs e entregas após parar de enviar")
	flag.BoolVar(&cfg.Receipts, "receipts", true, "destinatários enviam message.delivered e message.read")
	flag.Int64Var(&cfg.Seed, "seed", 1, "semente dos grupos e da escolha de conversas")
	flag.DurationVar(&cfg.SampleInterval, "sample", time.Second, "intervalo de amostragem do pprof")
	flag.StringVar(&cfg.OutDir, "out", "results", "pasta dos relatórios")
	flag.Parse()

	sizes, err := parseSizes(groupSizes)
	if err != nil {
		log.Fatalf("-group-sizes: %v", err)
	}
	cfg.GroupSizes = sizes

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if _, err := run(ctx, cfg); err != nil {
		log.Fatalf("teste de carga falhou: %v", err)
	}
}

func parseSizes(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var sizes []int
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		sizes = append(sizes, n)
	}
	return sizes, nil
}

// run executa todas as fases e devolve o resultado gravado em disco.
func run(ctx context.Context, cfg runConfig) (report.Result, error) {
	if cfg.Rate <= 0 || cfg.Duration <= 0 {
		return report.Result{}, errors.New("rate e duration precisam ser positivos")
	}
	startedAt := time.Now()
	runID := startedAt.Format("20060102-150405")
	outDir := filepath.Join(cfg.OutDir, runID)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return report.Result{}, err
	}

	plan, err := scenario.Build(scenario.Config{
		Prefix: cfg.Prefix, Users: cfg.Users, DirectPerUser: cfg.DirectPerUser,
		GroupSizes: cfg.GroupSizes, Seed: cfg.Seed,
	})
	if err != nil {
		return report.Result{}, err
	}
	rest := &client.REST{BaseURL: cfg.ServerURL, HTTP: &http.Client{Timeout: 30 * time.Second}}
	wsURL, err := client.WSURL(cfg.ServerURL)
	if err != nil {
		return report.Result{}, err
	}

	log.Printf("preparando %d usuários e %d conversas em %s", cfg.Users, len(plan.Conversations), cfg.ServerURL)
	users, usersCreated, err := setupUsers(ctx, rest, plan)
	if err != nil {
		return report.Result{}, err
	}
	groupsCreated, err := setupConversations(ctx, rest, plan, users)
	if err != nil {
		return report.Result{}, err
	}

	res := report.Result{
		RunID:     runID,
		StartedAt: startedAt,
		Config: report.Config{
			Prefix: cfg.Prefix, UsersCreated: usersCreated, GroupsCreated: groupsCreated,
			ServerURL: cfg.ServerURL, Users: cfg.Users, DirectConversations: cfg.Users * cfg.DirectPerUser,
			Groups: len(cfg.GroupSizes), GroupSizes: cfg.GroupSizes, Rate: cfg.Rate,
			Duration: cfg.Duration, Ramp: cfg.Ramp, Drain: cfg.Drain, Receipts: cfg.Receipts, Seed: cfg.Seed,
		},
	}

	// A linha do tempo começa quando a carga começa, depois da rampa.
	loadStart := time.Now().Add(cfg.Ramp)
	rec := metrics.NewRecorder(loadStart)

	log.Printf("conectando %d usuários em %s", len(users), cfg.Ramp)
	connected := connectUsers(ctx, users, wsURL, rec, cfg)
	defer closeUsers(users)

	sampler := &profiler.Sampler{BaseURL: cfg.PprofURL, Interval: cfg.SampleInterval}
	res.PprofAvailable = cfg.PprofURL != "" && sampler.Available(ctx)
	samplerCtx, stopSampler := context.WithCancel(context.Background())
	samplesCh := make(chan []profiler.Sample, 1)
	cpuDone := make(chan bool, 1)
	if res.PprofAvailable {
		go func() { samplesCh <- sampler.Run(samplerCtx, loadStart) }()
		go func() {
			err := sampler.CaptureCPU(ctx, int(cfg.Duration.Round(time.Second)/time.Second), filepath.Join(outDir, "cpu.pprof"))
			cpuDone <- err == nil
		}()
	} else {
		samplesCh <- nil
		cpuDone <- false
		log.Printf("pprof indisponível em %q; seguindo sem métricas do servidor", cfg.PprofURL)
	}

	log.Printf("carga: %.2f msg/s por usuário durante %s", cfg.Rate, cfg.Duration)
	generateLoad(ctx, connected, rec, cfg)
	res.Interrupted = ctx.Err() != nil

	if ctx.Err() == nil {
		log.Printf("drenando por %s", cfg.Drain)
		select {
		case <-ctx.Done():
		case <-time.After(cfg.Drain):
		}
	}

	if res.PprofAvailable {
		if <-cpuDone {
			res.ProfileFiles = append(res.ProfileFiles, "cpu.pprof")
		}
		heapCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if sampler.CaptureHeap(heapCtx, filepath.Join(outDir, "heap.pprof")) == nil {
			res.ProfileFiles = append(res.ProfileFiles, "heap.pprof")
		}
		cancel()
	}
	stopSampler()
	res.Server = <-samplesCh

	closeUsers(users)
	res.Summary = rec.Finalize()
	if err := report.Write(outDir, res); err != nil {
		return res, err
	}

	s := res.Summary
	delivery := s.DeliveryGroup
	if s.DeliveryDirect.P95 > delivery.P95 {
		delivery = s.DeliveryDirect
	}
	log.Printf("relatório em %s", filepath.Join(outDir, "report.md"))
	log.Printf("enviadas %d · ACK %d · entregas %d/%d · p95 de entrega (pior tipo) %.1f ms",
		s.Sent, s.Acked, s.Delivered, s.ExpectedDeliveries, float64(delivery.P95)/float64(time.Millisecond))
	return res, nil
}

// forEach roda fn para 0..n-1 com no máximo setupParallelism ao mesmo tempo
// e devolve o primeiro erro.
func forEach(ctx context.Context, n int, fn func(i int) error) error {
	sem := make(chan struct{}, setupParallelism)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			if err := fn(i); err != nil {
				once.Do(func() { first = err })
			}
		}(i)
	}
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}

func setupUsers(ctx context.Context, rest *client.REST, plan scenario.Plan) ([]*client.User, int, error) {
	users := make([]*client.User, len(plan.Usernames))
	var created atomic.Int64
	err := forEach(ctx, len(users), func(i int) error {
		isNew, err := rest.EnsureUser(ctx, plan.Usernames[i], password)
		if err != nil {
			return err
		}
		if isNew {
			created.Add(1)
		}
		token, id, err := rest.Login(ctx, plan.Usernames[i], password)
		if err != nil {
			return err
		}
		users[i] = &client.User{ID: id, Token: token}
		return nil
	})
	return users, int(created.Load()), err
}

// setupConversations garante cada conversa do plano no servidor e preenche
// User.Conversations. O criador é o primeiro membro.
func setupConversations(ctx context.Context, rest *client.REST, plan scenario.Plan, users []*client.User) (int, error) {
	ids := make([]int64, len(plan.Conversations))
	others := func(c scenario.Conversation, except int) []int64 {
		var out []int64
		for _, m := range c.Members {
			if m != except {
				out = append(out, users[m].ID)
			}
		}
		return out
	}

	var directs, groups []int
	for i, c := range plan.Conversations {
		if c.Kind == scenario.KindDirect {
			directs = append(directs, i)
		} else {
			groups = append(groups, i)
		}
	}

	err := forEach(ctx, len(directs), func(k int) error {
		i := directs[k]
		c := plan.Conversations[i]
		id, err := rest.CreateConversation(ctx, users[c.Members[0]].Token, scenario.KindDirect, "", others(c, c.Members[0]))
		ids[i] = id
		return err
	})
	if err != nil {
		return 0, err
	}

	created := 0
	listed := map[int][]client.RemoteConversation{}
	for _, i := range groups {
		c := plan.Conversations[i]
		creator := c.Members[0]
		if _, ok := listed[creator]; !ok {
			list, err := rest.ListConversations(ctx, users[creator].Token)
			if err != nil {
				return 0, err
			}
			listed[creator] = list
		}
		memberIDs := append([]int64{users[creator].ID}, others(c, creator)...)
		id, found, err := client.EnsureGroup(listed[creator], c.Name, memberIDs)
		if err != nil {
			return 0, err
		}
		if !found {
			if id, err = rest.CreateConversation(ctx, users[creator].Token, scenario.KindGroup, c.Name, others(c, creator)); err != nil {
				return 0, err
			}
			created++
		}
		ids[i] = id
	}

	for u, convs := range plan.ByUser {
		for _, i := range convs {
			c := plan.Conversations[i]
			users[u].Conversations = append(users[u].Conversations, client.UserConversation{
				ID: ids[i], Kind: c.Kind, Recipients: others(c, u),
			})
		}
	}
	return created, nil
}

// connectUsers conecta os usuários espalhados ao longo da rampa e devolve os
// que conectaram. Falhas viram o erro "connect_failed" no relatório.
func connectUsers(ctx context.Context, users []*client.User, wsURL string, rec *metrics.Recorder, cfg runConfig) []*client.User {
	start := time.Now()
	var mu sync.Mutex
	var wg sync.WaitGroup
	var connected []*client.User
	for i, u := range users {
		at := start.Add(time.Duration(i) * cfg.Ramp / time.Duration(len(users)))
		select {
		case <-ctx.Done():
			wg.Wait()
			return connected
		case <-time.After(time.Until(at)):
		}
		wg.Add(1)
		go func(u *client.User) {
			defer wg.Done()
			dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := u.Connect(dialCtx, wsURL, rec, cfg.Receipts); err != nil {
				log.Printf("conectar usuário %d: %v", u.ID, err)
				rec.Error("connect_failed")
				return
			}
			mu.Lock()
			connected = append(connected, u)
			mu.Unlock()
		}(u)
	}
	wg.Wait()
	return connected
}

// generateLoad faz cada usuário enviar cfg.Rate mensagens por segundo, em
// conversas sorteadas, durante cfg.Duration.
func generateLoad(ctx context.Context, users []*client.User, rec *metrics.Recorder, cfg runConfig) {
	loadCtx, cancel := context.WithTimeout(ctx, cfg.Duration)
	defer cancel()
	interval := time.Duration(float64(time.Second) / cfg.Rate)
	var wg sync.WaitGroup
	for _, u := range users {
		if len(u.Conversations) == 0 {
			continue
		}
		wg.Add(1)
		go func(u *client.User) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(cfg.Seed + u.ID))
			// Fase inicial aleatória para os envios não saírem todos juntos.
			select {
			case <-loadCtx.Done():
				return
			case <-time.After(time.Duration(rng.Int63n(int64(interval)))):
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for loadCtx.Err() == nil {
				conv := u.Conversations[rng.Intn(len(u.Conversations))]
				if err := u.Send(loadCtx, conv, rec); err != nil && loadCtx.Err() == nil {
					rec.Error("send_failed")
				}
				select {
				case <-loadCtx.Done():
					return
				case <-ticker.C:
				}
			}
		}(u)
	}
	wg.Wait()
}

func closeUsers(users []*client.User) {
	var wg sync.WaitGroup
	for _, u := range users {
		wg.Add(1)
		go func(u *client.User) { defer wg.Done(); u.Close() }(u)
	}
	wg.Wait()
}
