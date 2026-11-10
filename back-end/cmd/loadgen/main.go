// Command loadgen measures the throughput and latency of the chat server
// under many concurrent WebSocket connections.
//
// It registers its own users, opens one connection per user, has them
// send messages into a shared group and reports the latency distribution
// of the acknowledgements.
//
// Usage:
//
//	go run ./cmd/loadgen -server http://localhost:8080 -clients 200 -messages 20
//
// The flags are deliberately small by default so the tool is safe to run
// against a local server without thinking about it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

func main() {
	var (
		serverURL   = flag.String("server", "http://localhost:8080", "base HTTP URL of the server")
		clients     = flag.Int("clients", 100, "number of connected users")
		perClient   = flag.Int("messages", 10, "messages each client sends")
		runFor      = flag.Duration("duration", 0, "optional overall deadline")
		reportEvery = flag.Duration("report", 0, "optional progress interval")
	)
	flag.Parse()

	if err := run(*serverURL, *clients, *perClient, *runFor, *reportEvery); err != nil {
		log.Fatalf("loadgen failed: %v", err)
	}
}

// stats collects the latencies and counts observed during the run.
type stats struct {
	mu        sync.Mutex
	latencies []time.Duration
	sent      atomic.Int64
	acked     atomic.Int64
	failed    atomic.Int64
}

func (s *stats) record(d time.Duration) {
	s.mu.Lock()
	s.latencies = append(s.latencies, d)
	s.mu.Unlock()

	s.acked.Add(1)
}

// percentile returns the p-th percentile of the recorded latencies.
func (s *stats) percentile(p float64) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.latencies) == 0 {
		return 0
	}

	sorted := make([]time.Duration, len(s.latencies))
	copy(sorted, s.latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func run(serverURL string, clients, perClient int, runFor, reportEvery time.Duration) error {
	ctx := context.Background()
	if runFor > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, runFor)
		defer cancel()
	}

	client := &http.Client{Timeout: 15 * time.Second}

	runID := rand.Int63()

	fmt.Printf("loadgen: registering %d users on %s\n", clients+1, serverURL)

	// One owner creates the group; everybody else joins it.
	ownerToken, err := registerAndLogin(ctx, client, serverURL, fmt.Sprintf("load_owner_%d", runID))
	if err != nil {
		return fmt.Errorf("register owner: %w", err)
	}

	tokens := make([]string, 0, clients)
	memberIDs := make([]int64, 0, clients)

	for i := 0; i < clients; i++ {
		username := fmt.Sprintf("load_%d_%d", runID, i)

		token, id, err := registerLoginAndID(ctx, client, serverURL, username)
		if err != nil {
			return fmt.Errorf("register %s: %w", username, err)
		}
		tokens = append(tokens, token)
		memberIDs = append(memberIDs, id)
	}

	conversationID, err := createGroup(ctx, client, serverURL, ownerToken, memberIDs)
	if err != nil {
		return fmt.Errorf("create group: %w", err)
	}

	fmt.Printf("loadgen: group %d created, opening %d connections\n", conversationID, len(tokens))

	start := time.Now()
	st := &stats{}

	var wg sync.WaitGroup

	// Optional progress reporting so a long run is not silent.
	stopReport := make(chan struct{})
	if reportEvery > 0 {
		go func() {
			ticker := time.NewTicker(reportEvery)
			defer ticker.Stop()

			for {
				select {
				case <-stopReport:
					return
				case <-ticker.C:
					elapsed := time.Since(start).Seconds()
					fmt.Printf("  ... sent=%d acked=%d failed=%d elapsed=%.1fs rate=%.0f msg/s\n",
						st.sent.Load(), st.acked.Load(), st.failed.Load(),
						elapsed, float64(st.acked.Load())/elapsed)
				}
			}
		}()
	}

	for i, token := range tokens {
		wg.Add(1)
		go func(idx int, tk string) {
			defer wg.Done()
			if err := driveClient(ctx, serverURL, tk, conversationID, perClient, idx, st); err != nil {
				st.failed.Add(1)
			}
		}(i, token)
	}

	wg.Wait()
	close(stopReport)

	elapsed := time.Since(start)

	printReport(st, elapsed, clients)
	return nil
}

// driveClient opens one connection and sends its messages, timing each
// acknowledgement.
func driveClient(ctx context.Context, serverURL, token string, conversationID int64, count, idx int, st *stats) error {
	wsURL := strings.Replace(serverURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL += "/ws"

	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	cancel()
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Latency is measured per message, so each send time has to be
	// matched with the acknowledgement that carries its client_id. The
	// reader records arrival times keyed by that ID and the sender picks
	// them up, which keeps the two goroutines independent.
	acks := make(chan ackEvent, count+8)
	readErr := make(chan error, 1)

	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				readErr <- err
				return
			}

			var env struct {
				Type string `json:"type"`
				Data struct {
					ClientID string `json:"client_id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(data, &env); err != nil {
				continue
			}
			if env.Type == "message.ack" {
				select {
				case acks <- ackEvent{clientID: env.Data.ClientID, at: time.Now()}:
				default:
				}
			}
		}
	}()

	// sentAt maps a client_id to the moment it was written.
	sentAt := make(map[string]time.Time, count)
	pending := make(map[string]bool, count)

	for i := 0; i < count; i++ {
		clientID := fmt.Sprintf("load-%d-%d-%d", idx, i, time.Now().UnixNano())

		payload := map[string]any{
			"type": "message.send",
			"data": map[string]any{
				"client_id":       clientID,
				"conversation_id": conversationID,
				"body":            fmt.Sprintf("load message %d from client %d", i, idx),
			},
		}

		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}

		writeCtx, writeCancel := context.WithTimeout(ctx, 10*time.Second)
		at := time.Now()
		err = conn.Write(writeCtx, websocket.MessageText, raw)
		writeCancel()
		if err != nil {
			return fmt.Errorf("write: %w", err)
		}

		sentAt[clientID] = at
		pending[clientID] = true
		st.sent.Add(1)

		// Collect whatever acknowledgements have already arrived, without
		// blocking, so sending keeps up with the server.
		drainAcks(st, acks, sentAt, pending)
	}

	deadline := time.After(30 * time.Second)

	for len(pending) > 0 {
		select {
		case ev := <-acks:
			recordAck(st, ev, sentAt, pending)
		case <-readErr:
			return nil
		case <-deadline:
			return errors.New("timed out waiting for acknowledgements")
		}
	}

	return nil
}

// ackEvent is an acknowledgement together with the instant it arrived.
type ackEvent struct {
	clientID string
	at       time.Time
}

// drainAcks consumes every acknowledgement that is ready right now.
func drainAcks(st *stats, acks <-chan ackEvent, sentAt map[string]time.Time, pending map[string]bool) {
	for {
		select {
		case ev := <-acks:
			recordAck(st, ev, sentAt, pending)
		default:
			return
		}
	}
}

// recordAck matches an acknowledgement with its send time and records the
// round trip.
func recordAck(st *stats, ev ackEvent, sentAt map[string]time.Time, pending map[string]bool) {
	if !pending[ev.clientID] {
		// An acknowledgement for an unknown or already-counted message
		// would otherwise skew the percentiles.
		return
	}
	delete(pending, ev.clientID)

	start, ok := sentAt[ev.clientID]
	if !ok {
		return
	}

	latency := ev.at.Sub(start)
	if latency < 0 {
		return
	}
	st.record(latency)
}

func printReport(st *stats, elapsed time.Duration, clients int) {
	seconds := elapsed.Seconds()
	if seconds <= 0 {
		seconds = 1
	}

	fmt.Println()
	fmt.Println("================ loadgen report ================")
	fmt.Printf("clients:          %d\n", clients)
	fmt.Printf("messages sent:    %d\n", st.sent.Load())
	fmt.Printf("messages acked:   %d\n", st.acked.Load())
	fmt.Printf("failures:         %d\n", st.failed.Load())
	fmt.Printf("elapsed:          %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("throughput:       %.0f msg/s\n", float64(st.acked.Load())/seconds)
	fmt.Printf("latency p50:      %s\n", st.percentile(0.50).Round(time.Microsecond))
	fmt.Printf("latency p90:      %s\n", st.percentile(0.90).Round(time.Microsecond))
	fmt.Printf("latency p99:      %s\n", st.percentile(0.99).Round(time.Microsecond))
	fmt.Printf("goroutines:       %d\n", runtime.NumGoroutine())

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Printf("heap in use:      %.1f MiB\n", float64(mem.HeapInuse)/(1024*1024))
	fmt.Println("================================================")
}

func registerAndLogin(ctx context.Context, client *http.Client, serverURL, username string) (string, error) {
	token, _, err := registerLoginAndID(ctx, client, serverURL, username)
	return token, err
}

func registerLoginAndID(ctx context.Context, client *http.Client, serverURL, username string) (string, int64, error) {
	registerBody := map[string]any{
		"username":     username,
		"display_name": username,
		"password":     "loadgen-password",
	}

	var created struct {
		ID int64 `json:"id"`
	}
	if err := postJSON(ctx, client, serverURL+"/api/auth/register", "", registerBody, &created); err != nil {
		return "", 0, err
	}

	var loggedIn struct {
		Token string `json:"token"`
	}
	if err := postJSON(ctx, client, serverURL+"/api/auth/login", "", map[string]any{
		"username": username, "password": "loadgen-password",
	}, &loggedIn); err != nil {
		return "", 0, err
	}

	return loggedIn.Token, created.ID, nil
}

func createGroup(ctx context.Context, client *http.Client, serverURL, token string, members []int64) (int64, error) {
	body := map[string]any{
		"type":    "group",
		"name":    "loadgen",
		"members": members,
	}

	var created struct {
		ID int64 `json:"id"`
	}
	if err := postJSON(ctx, client, serverURL+"/api/conversations", token, body, &created); err != nil {
		return 0, err
	}

	return created.ID, nil
}

func postJSON(ctx context.Context, client *http.Client, url, token string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}
