package event

// Sink 接收内核发出的事件。Emit 在内核的 goroutine 里同步调用，
// 实现不得 panic，也不应长时间阻塞（会直接拖慢 ReAct 循环）。
type Sink interface {
	Emit(Event)
}

// SinkFunc 让普通函数满足 Sink。
type SinkFunc func(Event)

func (f SinkFunc) Emit(e Event) { f(e) }

type multi []Sink

func (m multi) Emit(e Event) {
	for _, s := range m {
		s.Emit(e)
	}
}

// Multi 把事件依次分发给多个 Sink，nil 会被忽略。
func Multi(sinks ...Sink) Sink {
	var out multi
	for _, s := range sinks {
		if s != nil {
			out = append(out, s)
		}
	}
	return out
}
