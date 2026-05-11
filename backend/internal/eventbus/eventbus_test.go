package eventbus

import (
	"sync"
	"testing"
	"time"
)

func recvTopic(t *testing.T, ch <-chan Event, want string) Event {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Topic != want {
			t.Fatalf("topic = %q, want %q", ev.Topic, want)
		}
		return ev
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("no event for %q within 200ms", want)
		return Event{}
	}
}

func recvNone(t *testing.T, ch <-chan Event, d time.Duration) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event: %+v", ev)
	case <-time.After(d):
	}
}

func TestBus_PublishSubscribe_RoundTrip(t *testing.T) {
	var b Bus
	ch, cancel := b.Subscribe("", 4)
	defer cancel()
	b.Publish(TopicInboxReceived, "hello")
	ev := recvTopic(t, ch, TopicInboxReceived)
	if ev.Payload != "hello" {
		t.Errorf("payload = %v, want %q", ev.Payload, "hello")
	}
}

func TestBus_PrefixFilter_OnlyMatchingTopics(t *testing.T) {
	var b Bus
	all, c1 := b.Subscribe("", 4)
	defer c1()
	peerOnly, c2 := b.Subscribe("peer.", 4)
	defer c2()

	b.Publish(TopicInboxReceived, nil)
	b.Publish(TopicPeerPresenceChanged, nil)

	recvTopic(t, all, TopicInboxReceived)
	recvTopic(t, all, TopicPeerPresenceChanged)
	recvTopic(t, peerOnly, TopicPeerPresenceChanged)
	recvNone(t, peerOnly, 50*time.Millisecond)
}

func TestBus_DropOnFull_DoesNotBlockPublisher(t *testing.T) {
	var b Bus
	ch, cancel := b.Subscribe("", 1)
	defer cancel()

	b.Publish(TopicInboxReceived, "first")

	done := make(chan struct{})
	go func() {
		b.Publish(TopicInboxReceived, "second")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Publish blocked on full subscriber")
	}

	ev := recvTopic(t, ch, TopicInboxReceived)
	if ev.Payload != "first" {
		t.Errorf("payload = %v, want %q", ev.Payload, "first")
	}
	recvNone(t, ch, 50*time.Millisecond)
}

func TestBus_MultipleSubscribers_AllReceive(t *testing.T) {
	var b Bus
	ch1, c1 := b.Subscribe("", 4)
	defer c1()
	ch2, c2 := b.Subscribe("", 4)
	defer c2()

	b.Publish(TopicInboxReceived, nil)

	recvTopic(t, ch1, TopicInboxReceived)
	recvTopic(t, ch2, TopicInboxReceived)
}

func TestBus_Cancel_Idempotent(t *testing.T) {
	var b Bus
	_, cancel := b.Subscribe("", 4)
	cancel()
	cancel()
}

func TestBus_Cancel_ClosesChannelAndStopsDelivery(t *testing.T) {
	var b Bus
	ch, cancel := b.Subscribe("", 4)
	cancel()

	if _, ok := <-ch; ok {
		t.Fatal("channel not closed after cancel")
	}

	b.Publish(TopicInboxReceived, nil)
}

func TestBus_Cancel_DoesNotStarveOtherSubscribers(t *testing.T) {
	var b Bus
	chA, cancelA := b.Subscribe("", 4)
	chB, cancelB := b.Subscribe("", 4)
	defer cancelB()

	cancelA()
	b.Publish(TopicInboxReceived, "x")

	if _, ok := <-chA; ok {
		t.Fatal("cancelled subscriber received event")
	}
	recvTopic(t, chB, TopicInboxReceived)
}

func TestBus_ConcurrentPublishSubscribe_NoDeadlock(t *testing.T) {
	var b Bus
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, cancel := b.Subscribe("", 4)
			defer cancel()
			done := make(chan struct{})
			go func() {
				for j := 0; j < 50; j++ {
					b.Publish(TopicSystemIDSEvent, j)
				}
				close(done)
			}()

			for {
				select {
				case <-ch:
				case <-done:
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestBus_NoSubscribers_PublishIsNoop(t *testing.T) {
	var b Bus

	b.Publish(TopicInboxReceived, nil)
}

func TestBus_PrefixExact_MatchesExactTopic(t *testing.T) {
	var b Bus

	ch, cancel := b.Subscribe(TopicSystemIDSEvent, 4)
	defer cancel()

	b.Publish(TopicSystemIDSEvent, nil)
	b.Publish(TopicInboxReceived, nil)

	recvTopic(t, ch, TopicSystemIDSEvent)
	recvNone(t, ch, 50*time.Millisecond)
}
