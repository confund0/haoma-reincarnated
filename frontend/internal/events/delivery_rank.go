package events

const (
	DeliveryPending   = "pending"
	DeliveryEnqueued  = "enqueued"
	DeliverySent      = "sent"
	DeliveryDelivered = "delivered"
	DeliveryRead      = "read"
	DeliveryFailed    = "failed"
)

func DeliveryRank(state string) (int, bool) {
	switch state {
	case "":
		return 0, true
	case DeliveryPending, DeliveryEnqueued:
		return 1, true
	case DeliverySent:
		return 2, true
	case DeliveryDelivered:
		return 3, true
	case DeliveryRead:
		return 4, true
	}
	return 0, false
}

func IsDeliveryRank(state string) bool {
	_, ok := DeliveryRank(state)
	return ok
}

func DeliveryOutranks(cur, next string) bool {
	curRank, curOK := DeliveryRank(cur)
	nextRank, nextOK := DeliveryRank(next)
	if !curOK || !nextOK {
		return false
	}
	return nextRank > curRank
}

func DeliveryAdvances(cur, next string) bool {
	if next == "" || cur == next {
		return false
	}
	if cur == "" {
		return true
	}
	if next == DeliveryFailed {

		return cur != DeliveryDelivered && cur != DeliveryRead
	}
	return DeliveryOutranks(cur, next)
}

const DeliverySiblingFloor = DeliverySent

func DeliverySeedFromSibling(proposed string) string {
	if DeliveryOutranks(proposed, DeliverySiblingFloor) {
		return DeliverySiblingFloor
	}
	return proposed
}
