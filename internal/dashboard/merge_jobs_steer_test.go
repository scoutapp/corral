package dashboard

import (
	"strings"
	"testing"
)

// drainSteerAcks collects any text/error frames a job emitted to a subscriber,
// non-blocking. Used to assert whether queueSteer gave the user feedback.
func drainSteerAcks(ch chan mergeJobEvent) []chatServerMsg {
	var msgs []chatServerMsg
	for {
		select {
		case ev := <-ch:
			msgs = append(msgs, ev.Msg)
		default:
			return msgs
		}
	}
}

// TestQueueSteer covers the fix for a steer being silently dropped: an idle
// worker gets the prompt with no noise; a busy worker gets the prompt buffered
// AND a visible acknowledgement; a full buffer surfaces an error rather than
// dropping the message without a trace.
func TestQueueSteer(t *testing.T) {
	t.Run("idle worker: queued, no ack line", func(t *testing.T) {
		j := &mergeJob{ID: "w-idle", Status: mergeJobIdle, subscribers: map[chan mergeJobEvent]struct{}{}}
		sub := j.subscribe()
		defer j.unsubscribe(sub)

		j.queueSteer("resolve it the other way")

		// The prompt must be sitting in the steer channel, ready for the run loop.
		select {
		case got := <-j.steerCh():
			if got != "resolve it the other way" {
				t.Fatalf("steer channel got %q", got)
			}
		default:
			t.Fatal("steer was not queued for an idle worker")
		}
		// Idle → the resulting turn is its own acknowledgement; no extra chatter.
		if acks := drainSteerAcks(sub); len(acks) != 0 {
			t.Fatalf("idle worker should not emit an ack line, got %+v", acks)
		}
	})

	t.Run("busy worker: queued AND acknowledged", func(t *testing.T) {
		j := &mergeJob{ID: "w-busy", Status: mergeJobRunning, subscribers: map[chan mergeJobEvent]struct{}{}}
		sub := j.subscribe()
		defer j.unsubscribe(sub)

		j.queueSteer("also check the index")

		select {
		case got := <-j.steerCh():
			if got != "also check the index" {
				t.Fatalf("steer channel got %q", got)
			}
		default:
			t.Fatal("steer was not queued for a busy worker")
		}
		acks := drainSteerAcks(sub)
		if len(acks) == 0 || !strings.Contains(acks[0].Text, "Message received") {
			t.Fatalf("busy worker should emit a 'Message received' ack, got %+v", acks)
		}
	})

	t.Run("full buffer: surfaced, never silently dropped", func(t *testing.T) {
		j := &mergeJob{ID: "w-full", Status: mergeJobRunning, subscribers: map[chan mergeJobEvent]struct{}{}}
		sub := j.subscribe()
		defer j.unsubscribe(sub)

		// Fill the steer buffer (cap 8) so the next queueSteer can't enqueue.
		ch := j.steerCh()
		for i := 0; i < cap(ch); i++ {
			ch <- "backlog"
		}
		_ = drainSteerAcks(sub) // ignore any acks from filling

		j.queueSteer("one too many")

		acks := drainSteerAcks(sub)
		var sawWarning bool
		for _, m := range acks {
			if m.Type == "error" && strings.Contains(m.Text, "Couldn't queue") {
				sawWarning = true
			}
		}
		if !sawWarning {
			t.Fatalf("a full steer buffer must surface an error, got %+v", acks)
		}
	})

	t.Run("blank steer is ignored", func(t *testing.T) {
		j := &mergeJob{ID: "w-blank", Status: mergeJobRunning, subscribers: map[chan mergeJobEvent]struct{}{}}
		sub := j.subscribe()
		defer j.unsubscribe(sub)

		j.queueSteer("   ")

		select {
		case got := <-j.steerCh():
			t.Fatalf("blank steer should not be queued, got %q", got)
		default:
		}
		if acks := drainSteerAcks(sub); len(acks) != 0 {
			t.Fatalf("blank steer should be silent, got %+v", acks)
		}
	})
}
