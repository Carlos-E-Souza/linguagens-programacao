package metrics

import (
	"sync"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/protocol"
)

// Bucket conta os eventos de um segundo do teste, a partir do início.
type Bucket struct {
	Second    int `json:"second"`
	Sent      int `json:"sent"`
	Acked     int `json:"acked"`
	Delivered int `json:"delivered"`
}

// Summary é o resultado consolidado de uma execução.
type Summary struct {
	Sent               int            `json:"sent"`
	Acked              int            `json:"acked"`
	MissingAcks        int            `json:"missing_acks"`
	ExpectedDeliveries int            `json:"expected_deliveries"`
	Delivered          int            `json:"delivered"`
	MissingDeliveries  int            `json:"missing_deliveries"`
	Duplicates         int            `json:"duplicates"`
	OrderViolations    int            `json:"order_violations"`
	Disconnects        int            `json:"disconnects"`
	Ack                Latency        `json:"ack"`
	DeliveryDirect     Latency        `json:"delivery_direct"`
	DeliveryGroup      Latency        `json:"delivery_group"`
	Errors             map[string]int `json:"errors"`
	Statuses           map[string]int `json:"statuses"`
	Timeline           []Bucket       `json:"timeline"`
}

// pending é o rastro de uma mensagem enviada.
type pending struct {
	kind       string
	sentAt     time.Time
	acked      bool
	recipients map[int64]bool // true quando o destinatário já recebeu
}

type userConversation struct {
	user, conversation int64
}

// Recorder é seguro para uso concorrente. Toda operação é curta e só segura
// o mutex, para que o leitor de cada conexão nunca fique bloqueado.
type Recorder struct {
	mu    sync.Mutex
	start time.Time

	messages     map[string]*pending
	lastSequence map[userConversation]int64

	sent, acked, delivered int
	duplicates, outOfOrder int
	disconnects            int
	ack, direct, group     []time.Duration
	errors, statuses       map[string]int
	timeline               []Bucket
}

// NewRecorder cria um coletor; start é a origem da linha do tempo.
func NewRecorder(start time.Time) *Recorder {
	return &Recorder{
		start:        start,
		messages:     map[string]*pending{},
		lastSequence: map[userConversation]int64{},
		errors:       map[string]int{},
		statuses:     map[string]int{},
	}
}

// bucket devolve o balde do instante at, criando os que faltarem.
func (r *Recorder) bucket(at time.Time) *Bucket {
	second := int(at.Sub(r.start) / time.Second)
	if second < 0 {
		second = 0
	}
	for len(r.timeline) <= second {
		r.timeline = append(r.timeline, Bucket{Second: len(r.timeline)})
	}
	return &r.timeline[second]
}

// Sent registra um envio. recipients não inclui o remetente.
func (r *Recorder) Sent(clientID string, conversation int64, kind string, sender int64, recipients []int64, at time.Time) {
	p := &pending{kind: kind, sentAt: at, recipients: make(map[int64]bool, len(recipients))}
	for _, id := range recipients {
		if id != sender {
			p.recipients[id] = false
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages[clientID] = p
	r.sent++
	r.bucket(at).Sent++
}

// Acked registra o ACK do servidor; client_id desconhecido é ignorado.
func (r *Recorder) Acked(clientID string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.messages[clientID]
	if !ok || p.acked {
		return
	}
	p.acked = true
	r.acked++
	r.ack = append(r.ack, at.Sub(p.sentAt))
	r.bucket(at).Acked++
}

// Received registra um message.new recebido pelo usuário user. O eco para o
// próprio remetente e mensagens de outras execuções são ignorados.
func (r *Recorder) Received(user int64, msg protocol.Message, at time.Time) {
	if msg.SenderID == user {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.messages[msg.ClientID]
	if !ok {
		return
	}
	got, expected := p.recipients[user]
	if !expected {
		return
	}
	if got {
		r.duplicates++
		return
	}
	p.recipients[user] = true

	key := userConversation{user, msg.ConversationID}
	if last, seen := r.lastSequence[key]; seen && msg.Sequence <= last {
		r.outOfOrder++
	} else {
		r.lastSequence[key] = msg.Sequence
	}

	r.delivered++
	latency := at.Sub(p.sentAt)
	if p.kind == "group" {
		r.group = append(r.group, latency)
	} else {
		r.direct = append(r.direct, latency)
	}
	r.bucket(at).Delivered++
}

// Error conta um frame de erro (ou falha local) pelo código.
func (r *Recorder) Error(code string) {
	r.mu.Lock()
	r.errors[code]++
	r.mu.Unlock()
}

// Status conta um message.status recebido.
func (r *Recorder) Status(status string) {
	r.mu.Lock()
	r.statuses[status]++
	r.mu.Unlock()
}

// Disconnected conta uma conexão perdida fora do encerramento normal.
func (r *Recorder) Disconnected(user int64) {
	r.mu.Lock()
	r.disconnects++
	r.mu.Unlock()
}

// Finalize consolida tudo o que foi registrado até agora.
func (r *Recorder) Finalize() Summary {
	r.mu.Lock()
	defer r.mu.Unlock()

	s := Summary{
		Sent:            r.sent,
		Acked:           r.acked,
		Delivered:       r.delivered,
		Duplicates:      r.duplicates,
		OrderViolations: r.outOfOrder,
		Disconnects:     r.disconnects,
		Ack:             Summarize(r.ack),
		DeliveryDirect:  Summarize(r.direct),
		DeliveryGroup:   Summarize(r.group),
		Errors:          map[string]int{},
		Statuses:        map[string]int{},
		Timeline:        append([]Bucket{}, r.timeline...),
	}
	for _, p := range r.messages {
		if !p.acked {
			s.MissingAcks++
		}
		s.ExpectedDeliveries += len(p.recipients)
	}
	s.MissingDeliveries = s.ExpectedDeliveries - s.Delivered
	for k, v := range r.errors {
		s.Errors[k] = v
	}
	for k, v := range r.statuses {
		s.Statuses[k] = v
	}
	return s
}
