package llm

import "fmt"

// Sequence checks that a stream of events follows the ordering rules of the extension protocol:
// response.start (or error) first, item indexes counting up from 0, deltas only into open items of
// the matching kind, every item closed before finish, nothing after the terminal event.
// The zero value is ready to use.
type Sequence struct {
	started bool
	done    bool
	next    int            // index the next item.start must use
	open    map[int]string // open item index -> kind
}

// Check reports whether ev may come next, and records it if so.
func (s *Sequence) Check(ev Event) error {
	if s.done {
		return fmt.Errorf("event %q after the terminal event", ev.Type)
	}
	if !s.started && ev.Type != EventResponseStart && ev.Type != EventError {
		return fmt.Errorf("first event is %q, want %q or %q", ev.Type, EventResponseStart, EventError)
	}
	switch ev.Type {
	case EventResponseStart:
		if s.started {
			return fmt.Errorf("second %q", EventResponseStart)
		}
		if ev.Response == nil {
			return fmt.Errorf("%q without response", ev.Type)
		}
		s.started = true
	case EventItemStart:
		if ev.Item == nil {
			return fmt.Errorf("%q without item", ev.Type)
		}
		switch ev.Item.Kind {
		case ItemMessage, ItemReasoning:
		case ItemToolCall:
			if ev.Item.CallID == "" || ev.Item.Name == "" {
				return fmt.Errorf("tool_call item %d without call_id or name", ev.Index)
			}
		default:
			return fmt.Errorf("item %d has unknown kind %q", ev.Index, ev.Item.Kind)
		}
		if ev.Index != s.next {
			return fmt.Errorf("item.start index %d, want %d", ev.Index, s.next)
		}
		if s.open == nil {
			s.open = map[int]string{}
		}
		s.open[ev.Index] = ev.Item.Kind
		s.next++
	case EventTextDelta, EventReasoningDelta, EventToolInputDelta:
		want := map[string]string{
			EventTextDelta:      ItemMessage,
			EventReasoningDelta: ItemReasoning,
			EventToolInputDelta: ItemToolCall,
		}[ev.Type]
		kind, ok := s.open[ev.Index]
		if !ok {
			return fmt.Errorf("%q into item %d, which is not open", ev.Type, ev.Index)
		}
		if kind != want {
			return fmt.Errorf("%q into %s item %d", ev.Type, kind, ev.Index)
		}
	case EventItemEnd:
		if _, ok := s.open[ev.Index]; !ok {
			return fmt.Errorf("item.end for item %d, which is not open", ev.Index)
		}
		delete(s.open, ev.Index)
	case EventFinish:
		if ev.Finish == nil {
			return fmt.Errorf("%q without finish", ev.Type)
		}
		if len(s.open) > 0 {
			return fmt.Errorf("finish with %d item(s) still open", len(s.open))
		}
		s.done = true
	case EventError:
		if ev.Error == nil || ev.Error.Code == "" {
			return fmt.Errorf("%q without an error code", ev.Type)
		}
		s.done = true
	default:
		return fmt.Errorf("unknown event type %q", ev.Type)
	}
	return nil
}

// Done reports whether the terminal event has been seen.
func (s *Sequence) Done() bool { return s.done }

// NextIndex returns the index the next item.start must use.
func (s *Sequence) NextIndex() int { return s.next }
