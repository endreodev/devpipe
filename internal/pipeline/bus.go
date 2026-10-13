package pipeline

import "sync"

// Message é enviada aos assinantes (SSE). Type: "event" | "demand" | "stats".
type Message struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type Bus struct {
	mu   sync.Mutex
	subs map[chan Message]struct{}
}

func NewBus() *Bus { return &Bus{subs: map[chan Message]struct{}{}} }

func (b *Bus) Subscribe() chan Message {
	ch := make(chan Message, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Bus) Unsubscribe(ch chan Message) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
	close(ch)
}

// Publish nunca bloqueia: se um cliente estiver lento, a mensagem é descartada
// para ele (o histórico continua íntegro no banco).
func (b *Bus) Publish(m Message) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- m:
		default:
		}
	}
}
